package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mimo/personal-os/internal/config"
)

func TestMultiUserIsolationIntegration(t *testing.T) {
	url := os.Getenv("MIMO_MIGRATION_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set MIMO_MIGRATION_TEST_DATABASE_URL to a disposable local pgvector database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(ctx, `DROP SCHEMA IF EXISTS mimo CASCADE`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		body, e := os.ReadFile(filepath.Join("..", "..", "db", "migrations", migrationName(i)))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); e != nil {
			t.Fatalf("migration %d: %v", i, e)
		}
	}
	seedMultiUser(t, ctx, db)
	a := New(db, config.Config{APIToken: "legacy-test-token-which-is-long-enough", OwnerID: "haris"})
	h := a.Routes()

	harisNote := apiJSON(t, h, "neura-token", http.MethodPost, "/v1/notes", `{"title":"Haris private","content":"private"}`, http.StatusCreated)
	noteID := harisNote["id"].(string)
	apiJSON(t, h, "najwa-token", http.MethodGet, "/v1/notes/"+noteID, "", http.StatusNotFound)
	apiJSON(t, h, "neura-token", http.MethodGet, "/v1/notes/"+noteID, "", http.StatusOK)
	apiJSON(t, h, "najwa-token", http.MethodPost, "/v1/notes", `{"title":"Najwa private","content":"private"}`, http.StatusCreated)
	harisAccount := apiJSON(t, h, "neura-token", http.MethodPost, "/v1/finance/accounts", `{"name":"Haris cash","kind":"cash"}`, http.StatusCreated)

	apiJSON(t, h, "nara-token", http.MethodGet, "/v1/finance/accounts", "", http.StatusForbidden)
	apiJSON(t, h, "nara-token", http.MethodPost, "/v1/memory/search", `{"query":"private"}`, http.StatusForbidden)
	apiJSON(t, h, "najwa-token", http.MethodGet, "/v1/finance/accounts/"+harisAccount["id"].(string), "", http.StatusNotFound)

	source := apiJSON(t, h, "nara-token", http.MethodPost, "/v1/knowledge/sources", `{"workspace":"shared","source_type":"book","title":"Shared book"}`, http.StatusCreated)
	apiJSON(t, h, "nara-token", http.MethodPost, "/v1/moments", `{"workspace":"shared","title":"Shared dinner","description":"A moment"}`, http.StatusCreated)
	apiJSON(t, h, "nara-token", http.MethodPost, "/v1/knowledge/notes", `{"workspace":"shared","source_id":"`+source["id"].(string)+`","title":"Nara note","body":"shared evidence","authored_by":"ai"}`, http.StatusCreated)
	apiJSON(t, h, "nara-token", http.MethodPost, "/v1/knowledge/discussions", `{"workspace":"shared","title":"Discussion","body":"perspectives","perspectives":[{"author":"haris","argument":"A"},{"author":"najwa","argument":"B"}],"synthesis":"AI synthesis","authored_by":"ai"}`, http.StatusCreated)
	apiJSON(t, h, "nara-token", http.MethodPost, "/v1/knowledge/search", `{"workspace":"shared","query":"evidence"}`, http.StatusOK)
	apiJSON(t, h, "neura-token", http.MethodPost, "/v1/knowledge/search", `{"workspace":"shared","query":"evidence"}`, http.StatusForbidden)
	var indexed int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mimo.search_chunks WHERE owner_id=(SELECT id::text FROM mimo.workspaces WHERE slug='shared') AND source_type IN ('knowledge_note','knowledge_discussion','moment')`).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if indexed != 3 {
		t.Fatalf("workspace index count=%d want=3", indexed)
	}
	collection := apiJSON(t, h, "nara-token", http.MethodPost, "/v1/legacy/collections", `{"workspace":"shared","slug":"values","title":"Our Values"}`, http.StatusCreated)
	apiJSON(t, h, "haris-human-token", http.MethodPost, "/v1/legacy/collections/"+collection["id"].(string)+"/approve?workspace=shared", "", http.StatusOK)
	approval := apiJSON(t, h, "najwa-human-token", http.MethodPost, "/v1/legacy/collections/"+collection["id"].(string)+"/approve?workspace=shared", "", http.StatusOK)
	if approval["approved"] != true {
		t.Fatalf("expected dual approval, got %#v", approval)
	}

	if _, err = db.Exec(ctx, `UPDATE mimo.workspace_members SET revoked_at=now() WHERE workspace_id=(SELECT id FROM mimo.workspaces WHERE slug='shared') AND principal_id=(SELECT id FROM mimo.principals WHERE slug='nara')`); err != nil {
		t.Fatal(err)
	}
	apiJSON(t, h, "nara-token", http.MethodPost, "/v1/knowledge/search", `{"workspace":"shared","query":"evidence"}`, http.StatusForbidden)
}

func migrationName(i int) string {
	return map[int]string{1: "001_init.sql", 2: "002_finance_liabilities.sql", 3: "003_embedding.sql", 4: "004_multiuser_knowledge_moments.sql"}[i]
}

func seedMultiUser(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	_, err := db.Exec(ctx, `INSERT INTO mimo.principals(id,slug,kind,display_name) VALUES
('00000000-0000-0000-0000-000000000011','haris','human','Haris'),('00000000-0000-0000-0000-000000000012','najwa','human','Najwa'),('00000000-0000-0000-0000-000000000021','neura','agent','Neura'),('00000000-0000-0000-0000-000000000022','najwa-agent','agent','Najwa agent'),('00000000-0000-0000-0000-000000000023','nara','agent','Nara');
INSERT INTO mimo.workspaces(id,slug,name,kind) VALUES
('00000000-0000-0000-0000-000000000101','personal-haris','Haris private','personal'),('00000000-0000-0000-0000-000000000102','personal-najwa','Najwa private','personal'),('00000000-0000-0000-0000-000000000103','shared','Shared couple','shared');
INSERT INTO mimo.workspace_members(workspace_id,principal_id,role) VALUES
('00000000-0000-0000-0000-000000000101','00000000-0000-0000-0000-000000000011','owner'),('00000000-0000-0000-0000-000000000101','00000000-0000-0000-0000-000000000021','agent'),('00000000-0000-0000-0000-000000000102','00000000-0000-0000-0000-000000000012','owner'),('00000000-0000-0000-0000-000000000102','00000000-0000-0000-0000-000000000022','agent'),('00000000-0000-0000-0000-000000000103','00000000-0000-0000-0000-000000000011','owner'),('00000000-0000-0000-0000-000000000103','00000000-0000-0000-0000-000000000012','owner'),('00000000-0000-0000-0000-000000000103','00000000-0000-0000-0000-000000000023','agent');
INSERT INTO mimo.permissions(principal_id,workspace_id,permission) VALUES
('00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000101','memory:read'),('00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000101','memory:write'),('00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000101','finance:read'),('00000000-0000-0000-0000-000000000021','00000000-0000-0000-0000-000000000101','finance:write'),
('00000000-0000-0000-0000-000000000022','00000000-0000-0000-0000-000000000102','memory:read'),('00000000-0000-0000-0000-000000000022','00000000-0000-0000-0000-000000000102','memory:write'),('00000000-0000-0000-0000-000000000022','00000000-0000-0000-0000-000000000102','finance:read'),('00000000-0000-0000-0000-000000000022','00000000-0000-0000-0000-000000000102','finance:write'),
('00000000-0000-0000-0000-000000000023','00000000-0000-0000-0000-000000000103','knowledge:read'),('00000000-0000-0000-0000-000000000023','00000000-0000-0000-0000-000000000103','knowledge:write'),('00000000-0000-0000-0000-000000000023','00000000-0000-0000-0000-000000000103','moments:read'),('00000000-0000-0000-0000-000000000023','00000000-0000-0000-0000-000000000103','moments:write'),('00000000-0000-0000-0000-000000000023','00000000-0000-0000-0000-000000000103','legacy:manage'),
('00000000-0000-0000-0000-000000000011','00000000-0000-0000-0000-000000000103','legacy:manage'),('00000000-0000-0000-0000-000000000012','00000000-0000-0000-0000-000000000103','legacy:manage');
INSERT INTO mimo.agent_credentials(name,agent_principal_id,owner_id,token_hash) VALUES
('neura','00000000-0000-0000-0000-000000000021','haris', $1),('najwa','00000000-0000-0000-0000-000000000022','najwa', $2),('nara','00000000-0000-0000-0000-000000000023','nara', $3),('haris-human','00000000-0000-0000-0000-000000000011','haris', $4),('najwa-human','00000000-0000-0000-0000-000000000012','najwa', $5)`, pgx.QueryExecModeSimpleProtocol, tokenHash("neura-token"), tokenHash("najwa-token"), tokenHash("nara-token"), tokenHash("haris-human-token"), tokenHash("najwa-human-token"))
	if err != nil {
		t.Fatal(err)
	}
}

func apiJSON(t *testing.T, h http.Handler, token, method, path, body string, want int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, rec.Code, want, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		return map[string]any{}
	}
	return out
}
