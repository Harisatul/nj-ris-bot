package checkin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
)

const testOwner = "me"
const testAgent = "00000000-0000-0000-0000-000000000021"
const testNara = "00000000-0000-0000-0000-000000000023"
const testWorkspace = "00000000-0000-0000-0000-000000000101"

func TestSchedulerPostgresIntegration(t *testing.T) {
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
	seedScheduler(t, ctx, db)
	loc, _ := time.LoadLocation("Asia/Jakarta")
	base := time.Date(2026, 1, 5, 12, 0, 0, 0, loc).UTC()
	s := NewScheduler(db, FixedClock{T: base}, "scheduler-a")

	t.Run("disabled and short gap do not queue", func(t *testing.T) {
		if _, err := db.Exec(ctx, `UPDATE mimo.checkin_settings SET enabled=false`); err != nil {
			t.Fatal(err)
		}
		stats, err := s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Queued != 0 || requestCount(t, ctx, db) != 0 {
			t.Fatalf("disabled queued stats=%+v count=%d", stats, requestCount(t, ctx, db))
		}
		if _, err := db.Exec(ctx, `UPDATE mimo.checkin_settings SET enabled=true`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO mimo.activity_events(owner_id,principal_id,workspace_id,title,started_at,ended_at) VALUES($1,$2::uuid,$3::uuid,'recent',$4,$5)`, testOwner, testAgent, testWorkspace, base.Add(-30*time.Minute), base); err != nil {
			t.Fatal(err)
		}
		stats, err = s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Queued != 0 {
			t.Fatalf("short gap queued=%d", stats.Queued)
		}
	})

	t.Run("threshold, dedupe, and competing workers", func(t *testing.T) {
		if _, err := db.Exec(ctx, `DELETE FROM mimo.activity_events; DELETE FROM mimo.checkin_requests`, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
		stats, err := s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Queued != 1 {
			t.Fatalf("threshold queued stats=%+v", stats)
		}
		stats, err = s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Queued != 0 || requestCount(t, ctx, db) != 1 {
			t.Fatalf("repeat created duplicate stats=%+v count=%d", stats, requestCount(t, ctx, db))
		}
		if _, err := db.Exec(ctx, `DELETE FROM mimo.checkin_requests`); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		results := make(chan Stats, 2)
		for _, name := range []string{"scheduler-a", "scheduler-b"} {
			wg.Add(1)
			go func(n string) {
				defer wg.Done()
				x, e := NewScheduler(db, FixedClock{T: base}, n).Evaluate(ctx)
				if e != nil {
					t.Errorf("competing scheduler: %v", e)
				}
				results <- x
			}(name)
		}
		wg.Wait()
		close(results)
		if requestCount(t, ctx, db) != 1 {
			t.Fatalf("competing schedulers created %d requests", requestCount(t, ctx, db))
		}
	})

	t.Run("backfill suppresses queued reminder", func(t *testing.T) {
		if _, err := db.Exec(ctx, `DELETE FROM mimo.activity_events; DELETE FROM mimo.checkin_requests`, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
		if stats, err := s.Evaluate(ctx); err != nil || stats.Queued != 1 {
			t.Fatalf("initial queue stats=%+v err=%v", stats, err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO mimo.activity_events(owner_id,principal_id,workspace_id,title,started_at,ended_at) VALUES($1,$2::uuid,$3::uuid,'backfill',$4,$5)`, testOwner, testAgent, testWorkspace, base.Add(-12*time.Hour), base); err != nil {
			t.Fatal(err)
		}
		stats, err := s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Suppressed != 1 {
			t.Fatalf("suppressed=%d want 1", stats.Suppressed)
		}
		if got := requestState(t, ctx, db); got != "suppressed" {
			t.Fatalf("state=%s want suppressed", got)
		}
	})

	t.Run("quiet hours daily limit cooldown and unanswered", func(t *testing.T) {
		if _, err := db.Exec(ctx, `DELETE FROM mimo.activity_events; DELETE FROM mimo.checkin_requests; UPDATE mimo.checkin_settings SET quiet_start='23:00',quiet_end='07:00',max_per_day=1,cooldown_minutes=150,pause_after_unanswered=2`, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
		quiet := time.Date(2026, 1, 5, 2, 0, 0, 0, loc).UTC()
		qstats, err := NewScheduler(db, FixedClock{T: quiet}, "quiet").Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if qstats.Queued != 0 {
			t.Fatalf("quiet queued=%d", qstats.Queued)
		}
		stats, err := s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Queued != 1 {
			t.Fatalf("first daily queue=%d", stats.Queued)
		}
		stats, err = s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Queued != 0 {
			t.Fatalf("daily limit queue=%d", stats.Queued)
		}
	})

	t.Run("claim success unknown and lease recovery", func(t *testing.T) {
		if _, err := db.Exec(ctx, `DELETE FROM mimo.activity_events; DELETE FROM mimo.checkin_requests; UPDATE mimo.checkin_settings SET max_per_day=4,cooldown_minutes=0,pause_after_unanswered=2,quiet_start='23:00',quiet_end='07:00'`, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
		fake := &FakeDelivery{Result: DeliveryResult{Delivered: true, ProviderMessageID: "fake-1"}}
		did, err := s.ProcessOne(ctx, fake)
		if err != nil || !did {
			t.Fatalf("process success did=%v err=%v", did, err)
		}
		if got := requestState(t, ctx, db); got != "sent" {
			t.Fatalf("state=%s want sent", got)
		}
		if len(fake.Attempts()) != 1 {
			t.Fatal("fake did not record send")
		}
		if _, err := db.Exec(ctx, `INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,dedupe_key,expires_at,lease_until,attempts) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'sending',$4::timestamptz-$5::interval,$4::timestamptz,'unknown-job',$4::timestamptz+interval '1 hour',$4::timestamptz-interval '1 minute',1)`, testOwner, testAgent, testWorkspace, base, "150 minutes"); err != nil {
			t.Fatal(err)
		}
		unknown := &FakeDelivery{Result: DeliveryResult{Unknown: true}}
		did, err = s.ProcessOne(ctx, unknown)
		if err != nil || !did {
			t.Fatalf("unknown process did=%v err=%v", did, err)
		}
		if got := requestStateByKey(t, ctx, db, "unknown-job"); got != "delivery_unknown" {
			t.Fatalf("unknown state=%s", got)
		}
		if _, err := db.Exec(ctx, `DELETE FROM mimo.checkin_requests WHERE dedupe_key='recovery-job'; INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,dedupe_key,expires_at,lease_until,attempts) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'sending',$4::timestamptz-$5::interval,$4::timestamptz,'recovery-job',$4::timestamptz+interval '1 hour',$4::timestamptz-interval '1 minute',1)`, pgx.QueryExecModeSimpleProtocol, testOwner, testAgent, testWorkspace, base, "150 minutes"); err != nil {
			t.Fatal(err)
		}
		recovered, err := NewScheduler(db, FixedClock{T: base}, "recovery").ClaimOne(ctx)
		if err != nil || recovered == nil {
			t.Fatalf("lease recovery job=%+v err=%v", recovered, err)
		}
		if recovered.Attempts != 2 {
			t.Fatalf("attempts=%d want 2", recovered.Attempts)
		}
		if _, err := db.Exec(ctx, `DELETE FROM mimo.activity_events; DELETE FROM mimo.checkin_requests; UPDATE mimo.checkin_settings SET max_per_day=4,cooldown_minutes=0,pause_after_unanswered=2`, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := db.Exec(ctx, `INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,dedupe_key,expires_at,sent_at) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'sent',$4::timestamptz-$5::interval,$4::timestamptz,$6,$4::timestamptz+interval '1 hour',$4::timestamptz)`, pgx.QueryExecModeSimpleProtocol, testOwner, testAgent, testWorkspace, base, "150 minutes", fmt.Sprintf("unanswered-%d", i)); err != nil {
				t.Fatal(err)
			}
		}
		stats, err := s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Queued != 0 {
			t.Fatalf("unanswered pause queued=%d", stats.Queued)
		}
	})

	t.Run("database state transition guard and Nara exclusion", func(t *testing.T) {
		if _, err := db.Exec(ctx, `DELETE FROM mimo.checkin_requests; INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,dedupe_key,expires_at) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'pending',$4::timestamptz-interval '150 minutes',$4::timestamptz,'guard-job',$4::timestamptz+interval '1 hour')`, pgx.QueryExecModeSimpleProtocol, testOwner, testAgent, testWorkspace, base); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE mimo.checkin_requests SET state='sent' WHERE dedupe_key='guard-job'`); err == nil {
			t.Fatal("invalid pending -> sent transition was accepted")
		}
		if _, err := db.Exec(ctx, `UPDATE mimo.checkin_settings SET principal_id=$1::uuid`, testNara); err != nil {
			t.Fatal(err)
		}
		stats, err := s.Evaluate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Evaluated != 0 {
			t.Fatalf("Nara settings evaluated=%d", stats.Evaluated)
		}
		if _, err := db.Exec(ctx, `UPDATE mimo.checkin_settings SET principal_id=$1::uuid`, testAgent); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("inbound response correlation and atomic activity write", func(t *testing.T) {
		if _, err := db.Exec(ctx, `DELETE FROM mimo.checkin_inbound_responses; DELETE FROM mimo.activity_events; DELETE FROM mimo.checkin_requests`, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
		var requestID string
		if err := db.QueryRow(ctx, `INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,provider_message_id,sent_at) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'sent',$4::timestamptz-interval '3 hours',$4::timestamptz,'provider-1',$4::timestamptz) RETURNING id::text`, testOwner, testAgent, testWorkspace, base).Scan(&requestID); err != nil {
			t.Fatal(err)
		}
		processor := &ResponseProcessor{DB: db, Clock: FixedClock{T: base}}
		msg := IncomingMessage{AccountID: "test-account", SenderJID: "628123@s.whatsapp.net", MessageID: "inbound-1", ReplyToProviderMessageID: "provider-1", Text: "Jam 9-10 makan, 10-11 tidur, 11-12 coding", ReceivedAt: base}
		result, err := processor.Process(ctx, msg)
		if err != nil || result.Outcome != "activities" || len(result.ActivityIDs) != 3 || result.CheckinID != requestID {
			t.Fatalf("response=%+v err=%v", result, err)
		}
		var state string
		if err := db.QueryRow(ctx, `SELECT state FROM mimo.checkin_requests WHERE id=$1::uuid`, requestID).Scan(&state); err != nil || state != "answered" {
			t.Fatalf("request state=%s err=%v", state, err)
		}
		var activityCount int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM mimo.activity_events WHERE owner_id=$1 AND source_message_id='inbound:inbound-1'`, testOwner).Scan(&activityCount); err != nil || activityCount != 3 {
			t.Fatalf("activity count=%d err=%v", activityCount, err)
		}
		duplicate, err := processor.Process(ctx, msg)
		if err != nil || !duplicate.Duplicate || duplicate.Outcome != "activities" {
			t.Fatalf("duplicate=%+v err=%v", duplicate, err)
		}

		var skipID string
		if err := db.QueryRow(ctx, `INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,provider_message_id,sent_at) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'sent',$4::timestamptz-interval '3 hours',$4::timestamptz,'provider-skip',$4::timestamptz) RETURNING id::text`, testOwner, testAgent, testWorkspace, base).Scan(&skipID); err != nil {
			t.Fatal(err)
		}
		skip, err := processor.Process(ctx, IncomingMessage{AccountID: "test-account", SenderJID: "628123@s.whatsapp.net", MessageID: "inbound-skip", ReplyToProviderMessageID: "provider-skip", Text: "Skip dulu.", ReceivedAt: base})
		if err != nil || skip.Outcome != "skip" {
			t.Fatalf("skip=%+v err=%v", skip, err)
		}
		if err := db.QueryRow(ctx, `SELECT state FROM mimo.checkin_requests WHERE id=$1::uuid`, skipID).Scan(&state); err != nil || state != "skipped" {
			t.Fatalf("skip state=%s err=%v", state, err)
		}

		var snoozeID string
		if err := db.QueryRow(ctx, `INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,provider_message_id,sent_at) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'sent',$4::timestamptz-interval '3 hours',$4::timestamptz,'provider-snooze',$4::timestamptz) RETURNING id::text`, testOwner, testAgent, testWorkspace, base).Scan(&snoozeID); err != nil {
			t.Fatal(err)
		}
		snooze, err := processor.Process(ctx, IncomingMessage{AccountID: "test-account", SenderJID: "628123@s.whatsapp.net", MessageID: "inbound-snooze", ReplyToProviderMessageID: "provider-snooze", Text: "Ingatkan lagi 1 jam.", ReceivedAt: base})
		if err != nil || snooze.Outcome != "snooze" {
			t.Fatalf("snooze=%+v err=%v", snooze, err)
		}
		if err := db.QueryRow(ctx, `SELECT state FROM mimo.checkin_requests WHERE id=$1::uuid`, snoozeID).Scan(&state); err != nil || state != "snoozed" {
			t.Fatalf("snooze state=%s err=%v", state, err)
		}
		if _, err := processor.Process(ctx, IncomingMessage{AccountID: "test-account", SenderJID: "628999@s.whatsapp.net", MessageID: "inbound-wrong", ReplyToProviderMessageID: "provider-snooze", Text: "skip", ReceivedAt: base}); err == nil {
			t.Fatal("unbound sender was accepted")
		}
	})

	t.Run("CLI adapter persists provider acceptance and trusted target", func(t *testing.T) {
		if _, err := db.Exec(ctx, `DELETE FROM mimo.checkin_delivery_attempts; DELETE FROM mimo.checkin_requests`, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,dedupe_key) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'queued',$4::timestamptz-interval '3 hours',$4::timestamptz,'adapter-job')`, testOwner, testAgent, testWorkspace, base); err != nil {
			t.Fatal(err)
		}
		runner := &recordingRunner{output: []byte(`{"accepted":true,"message_id":"wamid-test"}`)}
		delivery := NewOpenClawCLIDelivery(db, "openclaw", true, []string{testAgent}, 60)
		delivery.Runner = runner
		did, err := s.ProcessOne(ctx, delivery)
		if err != nil || !did {
			t.Fatalf("process did=%v err=%v", did, err)
		}
		var state, providerID, attemptStatus, account, recipient string
		if err := db.QueryRow(ctx, `SELECT state,coalesce(provider_message_id,'') FROM mimo.checkin_requests WHERE dedupe_key='adapter-job'`).Scan(&state, &providerID); err != nil {
			t.Fatal(err)
		}
		if state != "sent" || providerID != "wamid-test" {
			t.Fatalf("request state=%s provider=%s", state, providerID)
		}
		if err := db.QueryRow(ctx, `SELECT status,account_id,recipient_jid FROM mimo.checkin_delivery_attempts a JOIN mimo.checkin_requests r ON r.id=a.checkin_request_id WHERE r.dedupe_key='adapter-job'`).Scan(&attemptStatus, &account, &recipient); err != nil {
			t.Fatal(err)
		}
		if attemptStatus != "provider_accepted" || account != "test-account" || recipient != "628123@s.whatsapp.net" {
			t.Fatalf("attempt status=%s account=%s recipient=%s", attemptStatus, account, recipient)
		}
		if runner.name != "openclaw" || len(runner.args) == 0 || runner.args[0] != "message" {
			t.Fatalf("unexpected command=%q args=%v", runner.name, runner.args)
		}
	})

	t.Run("signed gateway accepts duplicate safely and rejects bad signature", func(t *testing.T) {
		if _, err := db.Exec(ctx, `DELETE FROM mimo.checkin_inbound_responses; DELETE FROM mimo.checkin_requests`, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,provider_message_id,sent_at) VALUES($1,$2::uuid,$3::uuid,$4::timestamptz,'sent',$4::timestamptz-interval '3 hours',$4::timestamptz,'provider-gateway',$4::timestamptz)`, testOwner, testAgent, testWorkspace, base); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"event_id":"gateway-1","channel":"whatsapp","account_id":"test-account","sender_jid":"628123@s.whatsapp.net","message_id":"gateway-msg-1","reply_to_provider_message_id":"provider-gateway","text":"skip","received_at":"%s"}`, base.Format(time.RFC3339Nano))
		secret := []byte("staging-secret")
		timestamp := fmt.Sprintf("%d", base.Unix())
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(timestamp + "."))
		mac.Write([]byte(body))
		signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		gateway := GatewayHandler{DB: db, Secret: secret, Enabled: true, Clock: FixedClock{T: base}}
		req := httptest.NewRequest("POST", "/internal/v1/checkin/inbound", strings.NewReader(body))
		req.Header.Set("X-Mimo-Timestamp", timestamp)
		req.Header.Set("X-Mimo-Signature", signature)
		rec := httptest.NewRecorder()
		gateway.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("valid gateway status=%d body=%s", rec.Code, rec.Body.String())
		}
		duplicateReq := httptest.NewRequest("POST", "/internal/v1/checkin/inbound", strings.NewReader(body))
		duplicateReq.Header.Set("X-Mimo-Timestamp", timestamp)
		duplicateReq.Header.Set("X-Mimo-Signature", signature)
		duplicateRec := httptest.NewRecorder()
		gateway.ServeHTTP(duplicateRec, duplicateReq)
		if duplicateRec.Code != 200 || !strings.Contains(duplicateRec.Body.String(), `"duplicate":true`) {
			t.Fatalf("duplicate gateway status=%d body=%s", duplicateRec.Code, duplicateRec.Body.String())
		}
		badReq := httptest.NewRequest("POST", "/internal/v1/checkin/inbound", strings.NewReader(body))
		badReq.Header.Set("X-Mimo-Timestamp", timestamp)
		badReq.Header.Set("X-Mimo-Signature", "sha256="+strings.Repeat("0", 64))
		badRec := httptest.NewRecorder()
		gateway.ServeHTTP(badRec, badReq)
		if badRec.Code != 401 {
			t.Fatalf("bad signature status=%d body=%s", badRec.Code, badRec.Body.String())
		}
	})
}

func migrationName(i int) string {
	return map[int]string{1: "001_init.sql", 2: "002_finance_liabilities.sql", 3: "003_embedding.sql", 4: "004_multiuser_knowledge_moments.sql", 5: "005_personal_owner_binding.sql", 6: "006_life_timeline_habits.sql", 7: "007_adaptive_checkin_scheduler.sql", 8: "008_checkin_delivery_bindings.sql"}[i]
}
func seedScheduler(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	_, err := db.Exec(ctx, `INSERT INTO mimo.principals(id,slug,kind,display_name) VALUES('00000000-0000-0000-0000-000000000011','haris','human','Haris'),($1,'neura','agent','Neura'),($2,'nara','agent','Nara'); INSERT INTO mimo.workspaces(id,slug,name,kind) VALUES($3,'personal-haris','Haris private','personal'); INSERT INTO mimo.workspace_members(workspace_id,principal_id,role) VALUES($3,'00000000-0000-0000-0000-000000000011','owner'),($3,$1,'agent'),($3,$2,'agent'); INSERT INTO mimo.personal_workspace_owners(workspace_id,owner_id,human_principal_id) VALUES($3,$4,'00000000-0000-0000-0000-000000000011'); INSERT INTO mimo.permissions(principal_id,workspace_id,permission) VALUES($1,$3,'timeline:write'),($2,$3,'timeline:write'); INSERT INTO mimo.checkin_settings(owner_id,principal_id,workspace_id,enabled,strategy,gap_threshold_minutes,cooldown_minutes,max_per_day,quiet_start,quiet_end,timezone,pause_after_unanswered) VALUES($4,$1,$3,true,'adaptive',150,150,4,'23:00','07:00','Asia/Jakarta',2)`, pgx.QueryExecModeSimpleProtocol, testAgent, testNara, testWorkspace, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `INSERT INTO mimo.checkin_delivery_bindings(owner_id,principal_id,workspace_id,agent_id,account_id,recipient_jid,verified,enabled,verified_at) VALUES($3,$1,$2,'neura','test-account','628123@s.whatsapp.net',true,true,now())`, testAgent, testWorkspace, testOwner)
	if err != nil {
		t.Fatal(err)
	}
}
func requestCount(t *testing.T, ctx context.Context, db *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mimo.checkin_requests WHERE owner_id=$1`, testOwner).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func requestState(t *testing.T, ctx context.Context, db *pgxpool.Pool) string {
	return requestStateByQuery(t, ctx, db, `SELECT state FROM mimo.checkin_requests WHERE owner_id=$1 ORDER BY created_at DESC LIMIT 1`, testOwner)
}
func requestStateByKey(t *testing.T, ctx context.Context, db *pgxpool.Pool, key string) string {
	return requestStateByQuery(t, ctx, db, `SELECT state FROM mimo.checkin_requests WHERE dedupe_key=$1`, key)
}
func requestStateByQuery(t *testing.T, ctx context.Context, db *pgxpool.Pool, q string, arg string) string {
	t.Helper()
	var state string
	if err := db.QueryRow(ctx, q, arg).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
