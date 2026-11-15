package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mimo/personal-os/internal/config"
)

func TestLifeTimelinePostgresIntegration(t *testing.T) {
	databaseURL := os.Getenv("MIMO_MIGRATION_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set MIMO_MIGRATION_TEST_DATABASE_URL to a disposable local pgvector database")
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		t.Fatalf("refusing non-local integration database host %q", u.Hostname())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `DROP SCHEMA IF EXISTS mimo CASCADE`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		body, e := os.ReadFile(filepath.Join("..", "..", "db", "migrations", migrationName(i)))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); e != nil {
			t.Fatalf("migration %d: %v", i, e)
		}
	}
	seedMultiUser(t, ctx, db)
	a := New(db, config.Config{APIToken: "legacy-test-token-which-is-long-enough", OwnerID: "me"})
	h := a.Routes()

	t.Run("atomic validation and rollback", func(t *testing.T) {
		before := countActivities(t, ctx, db, "me")
		status, _ := activityCall(t, h, "neura-token", http.MethodPost, "/v1/activities/batch", `{"activities":[{"title":"valid","started_at":"2026-01-01T09:00:00+07:00","ended_at":"2026-01-01T10:00:00+07:00"},{"title":"invalid","started_at":"2026-01-01T11:00:00+07:00","ended_at":"2026-01-01T10:00:00+07:00"}]}`, "atomic-invalid")
		if status != http.StatusBadRequest {
			t.Fatalf("status=%d want 400", status)
		}
		if got := countActivities(t, ctx, db, "me"); got != before {
			t.Fatalf("batch partially committed: before=%d after=%d", before, got)
		}
		_, err := db.Exec(ctx, `CREATE FUNCTION mimo.test_fail_timeline_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.title='db-fail' THEN RAISE EXCEPTION 'intentional disposable test failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_fail_timeline_insert BEFORE INSERT ON mimo.activity_events FOR EACH ROW EXECUTE FUNCTION mimo.test_fail_timeline_insert()`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = db.Exec(ctx, `DROP TRIGGER IF EXISTS test_fail_timeline_insert ON mimo.activity_events; DROP FUNCTION IF EXISTS mimo.test_fail_timeline_insert()`)
		}()
		before = countActivities(t, ctx, db, "me")
		status, _ = activityCall(t, h, "neura-token", http.MethodPost, "/v1/activities/batch", `{"activities":[{"title":"transaction-first","started_at":"2026-01-01T12:00:00+07:00","ended_at":"2026-01-01T13:00:00+07:00"},{"title":"db-fail","started_at":"2026-01-01T13:00:00+07:00","ended_at":"2026-01-01T14:00:00+07:00"}]}`, "db-rollback")
		if status != http.StatusInternalServerError {
			t.Fatalf("database failure status=%d want 500", status)
		}
		if got := countActivities(t, ctx, db, "me"); got != before {
			t.Fatalf("transaction did not roll back: before=%d after=%d", before, got)
		}
	})

	t.Run("concurrent idempotency", func(t *testing.T) {
		body := `{"activities":[{"title":"one retry-safe event","category":"work","started_at":"2026-01-02T09:00:00+07:00","ended_at":"2026-01-02T10:00:00+07:00"}]}`
		const n = 8
		statuses := make(chan int, n)
		ids := make(chan string, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status, response := activityCall(t, h, "neura-token", http.MethodPost, "/v1/activities/batch", body, "same-concurrent-key")
				statuses <- status
				var decoded struct {
					Activities []activityRecord `json:"activities"`
				}
				if json.Unmarshal(response, &decoded) == nil && len(decoded.Activities) == 1 {
					ids <- decoded.Activities[0].ID
				}
			}()
		}
		wg.Wait()
		close(statuses)
		close(ids)
		for status := range statuses {
			if status != http.StatusCreated {
				t.Fatalf("concurrent retry status=%d want 201", status)
			}
		}
		var first string
		count := 0
		for got := range ids {
			if first == "" {
				first = got
			}
			if got != first {
				t.Fatalf("retry returned different IDs: %s vs %s", first, got)
			}
			count++
		}
		if count != n {
			t.Fatalf("decoded IDs=%d want=%d", count, n)
		}
		if got := countActivitiesByTitle(t, ctx, db, "me", "one retry-safe event"); got != 1 {
			t.Fatalf("stored duplicate count=%d want=1", got)
		}
	})

	t.Run("revision and stale correction", func(t *testing.T) {
		id, version := createActivity(t, h, "neura-token", "Revision target", "2026-01-03T09:00:00+07:00", "2026-01-03T10:00:00+07:00", "revision-key")
		status, _ := activityCall(t, h, "neura-token", http.MethodPatch, "/v1/activities/"+id, `{"version":1,"title":"Corrected"}`, "revision-correct")
		if status != http.StatusOK {
			t.Fatalf("correction status=%d want 200", status)
		}
		status, _ = activityCall(t, h, "neura-token", http.MethodPatch, "/v1/activities/"+id, fmt.Sprintf(`{"version":%d,"title":"stale"}`, version), "revision-stale")
		if status != http.StatusConflict {
			t.Fatalf("stale status=%d want 409", status)
		}
		var revisions int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM mimo.activity_revisions WHERE activity_id=$1::uuid`, id).Scan(&revisions); err != nil {
			t.Fatal(err)
		}
		if revisions != 1 {
			t.Fatalf("revision count=%d want=1", revisions)
		}
	})

	t.Run("coverage overlap midnight and timezone", func(t *testing.T) {
		_, err := db.Exec(ctx, `INSERT INTO mimo.activity_events(owner_id,principal_id,workspace_id,title,category,started_at,ended_at) VALUES
('me','00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000101','meeting','work','2026-01-04T09:00:00+07','2026-01-04T10:00:00+07'),
('me','00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000101','coding','work','2026-01-04T09:30:00+07','2026-01-04T11:00:00+07'),
('me','00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000101','break','rest','2026-01-04T11:00:00+07','2026-01-04T11:30:00+07'),
('me','00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000101','midnight','work','2026-01-04T23:30:00+07','2026-01-05T00:30:00+07')`)
		if err != nil {
			t.Fatal(err)
		}
		status, response := activityCall(t, h, "neura-token", http.MethodGet, "/v1/timeline/coverage?from=2026-01-04T09:00:00%2B07:00&to=2026-01-04T12:00:00%2B07:00", "", "")
		if status != http.StatusOK {
			t.Fatalf("coverage status=%d body=%s", status, response)
		}
		var day struct {
			Covered int `json:"covered_minutes"`
			Unknown int `json:"unknown_minutes"`
		}
		if err := json.Unmarshal(response, &day); err != nil {
			t.Fatal(err)
		}
		if day.Covered != 150 || day.Unknown != 30 {
			t.Fatalf("day coverage=%+v want covered=150 unknown=30", day)
		}
		status, response = activityCall(t, h, "neura-token", http.MethodGet, "/v1/timeline/coverage?from=2026-01-04T23:00:00%2B07:00&to=2026-01-05T01:00:00%2B07:00", "", "")
		if status != http.StatusOK {
			t.Fatalf("midnight status=%d", status)
		}
		if err := json.Unmarshal(response, &day); err != nil {
			t.Fatal(err)
		}
		if day.Covered != 60 || day.Unknown != 60 {
			t.Fatalf("midnight coverage=%+v want covered=60 unknown=60", day)
		}
	})

	t.Run("pagination beyond fifty", func(t *testing.T) {
		activities := make([]map[string]any, 55)
		for i := range activities {
			activities[i] = map[string]any{"title": fmt.Sprintf("page-%02d", i), "started_at": fmt.Sprintf("2026-02-01T%02d:00:00+07:00", i%24), "ended_at": fmt.Sprintf("2026-02-01T%02d:30:00+07:00", i%24)}
		}
		body, _ := json.Marshal(map[string]any{"activities": activities})
		status, _ := activityCall(t, h, "neura-token", http.MethodPost, "/v1/activities/batch", string(body), "pagination-batch")
		if status != http.StatusCreated {
			t.Fatalf("batch status=%d", status)
		}
		status, response := activityCall(t, h, "neura-token", http.MethodGet, "/v1/activities?from=2026-02-01T00:00:00%2B07:00&to=2026-02-02T00:00:00%2B07:00&limit=50&offset=0", "", "")
		if status != http.StatusOK {
			t.Fatalf("page 1 status=%d", status)
		}
		var first struct {
			Items []activityRecord `json:"items"`
		}
		if json.Unmarshal(response, &first) != nil || len(first.Items) != 50 {
			t.Fatalf("page 1 items=%d want=50", len(first.Items))
		}
		status, response = activityCall(t, h, "neura-token", http.MethodGet, "/v1/activities?from=2026-02-01T00:00:00%2B07:00&to=2026-02-02T00:00:00%2B07:00&limit=50&offset=50", "", "")
		if status != http.StatusOK {
			t.Fatalf("page 2 status=%d", status)
		}
		var second struct {
			Items []activityRecord `json:"items"`
		}
		if json.Unmarshal(response, &second) != nil || len(second.Items) != 5 {
			t.Fatalf("page 2 items=%d want=5", len(second.Items))
		}
	})

	t.Run("habit recalculation after correction", func(t *testing.T) {
		status, response := activityCall(t, h, "neura-token", http.MethodPost, "/v1/habits", `{"name":"Study integration","metric":"duration","target":120,"period":"week","category":"study"}`, "")
		if status != http.StatusCreated {
			t.Fatalf("habit status=%d body=%s", status, response)
		}
		id1, _ := createActivity(t, h, "neura-token", "Study one", "2026-01-05T09:00:00+07:00", "2026-01-05T10:00:00+07:00", "habit-one")
		createActivity(t, h, "neura-token", "Study two", "2026-01-06T09:00:00+07:00", "2026-01-06T09:30:00+07:00", "habit-two")
		status, response = activityCall(t, h, "neura-token", http.MethodGet, "/v1/habits/progress?date=2026-01-05", "", "")
		if status != http.StatusOK {
			t.Fatalf("progress status=%d", status)
		}
		var progress struct {
			Items []struct {
				Value float64 `json:"value"`
			} `json:"items"`
		}
		if json.Unmarshal(response, &progress) != nil || len(progress.Items) == 0 || progress.Items[0].Value != 0 {
			t.Fatalf("category matching unexpectedly counted: %s", response)
		}
		// The activity category is intentionally corrected to the habit category.
		status, _ = activityCall(t, h, "neura-token", http.MethodPatch, "/v1/activities/"+id1, `{"version":1,"category":"study"}`, "habit-correct")
		if status != http.StatusOK {
			t.Fatalf("habit correction status=%d", status)
		}
		status, response = activityCall(t, h, "neura-token", http.MethodGet, "/v1/habits/progress?date=2026-01-05", "", "")
		if status != http.StatusOK {
			t.Fatalf("progress after correction status=%d", status)
		}
		if json.Unmarshal(response, &progress) != nil || len(progress.Items) == 0 || progress.Items[0].Value != 60 {
			t.Fatalf("progress after correction=%s", response)
		}
	})

	t.Run("cross-user and Nara isolation", func(t *testing.T) {
		status, _ := activityCall(t, h, "najwa-token", http.MethodGet, "/v1/activities?limit=200", "", "")
		if status != http.StatusOK {
			t.Fatalf("Najwa list status=%d", status)
		}
		status, _ = activityCall(t, h, "najwa-token", http.MethodPost, "/v1/activities/batch", `{"activities":[{"title":"wrong owner","started_at":"2026-04-01T09:00:00+07:00","ended_at":"2026-04-01T10:00:00+07:00"}]}`, "najwa-own")
		if status != http.StatusCreated {
			t.Fatalf("Najwa own write status=%d", status)
		}
		if countActivities(t, ctx, db, "najwa") == 0 {
			t.Fatal("Najwa activity was not isolated into Najwa owner")
		}
		harisID, _ := createActivity(t, h, "neura-token", "Haris-only target", "2026-04-01T11:00:00+07:00", "2026-04-01T12:00:00+07:00", "haris-only-target")
		status, _ = activityCall(t, h, "najwa-token", http.MethodGet, "/v1/activities/"+harisID, "", "")
		if status != http.StatusNotFound {
			t.Fatalf("cross-user read status=%d want 404", status)
		}
		status, _ = activityCall(t, h, "najwa-token", http.MethodPatch, "/v1/activities/"+harisID, `{"version":1,"title":"hijack"}`, "cross-user-correct")
		if status != http.StatusNotFound {
			t.Fatalf("cross-user correction status=%d want 404", status)
		}
		status, _ = activityCall(t, h, "najwa-token", http.MethodPost, "/v1/activities/"+harisID+"/void", `{"version":1}`, "cross-user-void")
		if status != http.StatusConflict {
			t.Fatalf("cross-user void status=%d want 409", status)
		}
		if _, err := db.Exec(ctx, `INSERT INTO mimo.permissions(principal_id,workspace_id,permission) VALUES('00000000-0000-0000-0000-000000000023','00000000-0000-0000-0000-000000000101','timeline:read'),('00000000-0000-0000-0000-000000000023','00000000-0000-0000-0000-000000000101','timeline:write') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatal(err)
		}
		for _, endpoint := range []struct{ method, path, body string }{{http.MethodGet, "/v1/activities", ""}, {http.MethodGet, "/v1/timeline/coverage", ""}, {http.MethodPost, "/v1/activities/batch", `{"activities":[{"title":"blocked","started_at":"2026-04-02T09:00:00+07:00","ended_at":"2026-04-02T10:00:00+07:00"}]}`}} {
			status, _ = activityCall(t, h, "nara-token", endpoint.method, endpoint.path, endpoint.body, "nara-key")
			if status != http.StatusForbidden {
				t.Fatalf("Nara %s %s status=%d want 403", endpoint.method, endpoint.path, status)
			}
		}
		status, _ = activityCall(t, h, "neura-token", http.MethodPost, "/v1/activities/batch", `{"owner_id":"najwa","activities":[]}`, "scope-injection")
		if status != http.StatusBadRequest {
			t.Fatalf("scope injection status=%d want 400", status)
		}
	})
}

func activityCall(t *testing.T, h http.Handler, token, method, path, body, idem string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}
func createActivity(t *testing.T, h http.Handler, token, title, start, end, idem string) (string, int) {
	status, body := activityCall(t, h, token, http.MethodPost, "/v1/activities/batch", fmt.Sprintf(`{"activities":[{"title":%q,"category":"work","started_at":%q,"ended_at":%q}]}`, title, start, end), idem)
	if status != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", status, body)
	}
	var out struct {
		Activities []activityRecord `json:"activities"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Activities) != 1 {
		t.Fatalf("create response=%s", body)
	}
	return out.Activities[0].ID, out.Activities[0].Version
}
func countActivities(t *testing.T, ctx context.Context, db *pgxpool.Pool, owner string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mimo.activity_events WHERE owner_id=$1`, owner).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func countActivitiesByTitle(t *testing.T, ctx context.Context, db *pgxpool.Pool, owner, title string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mimo.activity_events WHERE owner_id=$1 AND title=$2`, owner, title).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
