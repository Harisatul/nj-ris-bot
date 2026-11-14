package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mimo/personal-os/internal/config"
	"github.com/mimo/personal-os/internal/embedding"
	"github.com/mimo/personal-os/internal/r2"
)

type App struct {
	DB    *pgxpool.Pool
	C     config.Config
	R2    *r2.Client
	Embed *embedding.Client
}

func New(db *pgxpool.Pool, c config.Config) *App {
	a := &App{DB: db, C: c}
	if c.HasR2() {
		a.R2 = r2.New(c.R2AccountID, c.R2AccessKey, c.R2SecretKey, c.R2Bucket)
	}
	if c.HasEmbedding() {
		a.Embed = embedding.New(c.EmbeddingURL, c.EmbeddingKey, c.EmbeddingModel, c.EmbeddingDimensions)
	}
	return a
}
func (a *App) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{"ok": true, "service": "mimo-api"})
	})
	r.Group(func(r chi.Router) {
		r.Use(a.authorize)
		r.Post("/v1/notes", a.noteCreate)
		r.Get("/v1/notes", a.noteList)
		r.Get("/v1/notes/{id}", a.noteGet)
		r.Patch("/v1/notes/{id}", a.notePatch)
		r.Delete("/v1/notes/{id}", a.noteDelete)
		r.Post("/v1/objects", a.objectCreate)
		r.Get("/v1/objects", a.objectList)
		r.Get("/v1/objects/{id}", a.objectGet)
		r.Patch("/v1/objects/{id}", a.objectPatch)
		r.Delete("/v1/objects/{id}", a.objectDelete)
		r.Post("/v1/objects/{id}/assets", a.objectAttachAsset)
		r.Post("/v1/assets/upload", a.assetUpload)
		r.Get("/v1/assets", a.assetList)
		r.Get("/v1/assets/{id}", a.assetGet)
		r.Get("/v1/assets/{id}/download", a.assetDownload)
		r.Delete("/v1/assets/{id}", a.assetDelete)
		r.Post("/v1/memories", a.memoryCreate)
		r.Get("/v1/memories", a.memoryList)
		r.Delete("/v1/memories/{id}", a.memoryDelete)
		r.Post("/v1/memory/search", a.memorySemanticSearch)
		r.Post("/v1/search", a.search)
		r.Post("/v1/knowledge/sources", a.knowledgeSourceCreate)
		r.Post("/v1/knowledge/notes", a.knowledgeNoteCreate)
		r.Post("/v1/knowledge/discussions", a.knowledgeDiscussionCreate)
		r.Post("/v1/knowledge/search", a.knowledgeSearch)
		r.Post("/v1/moments", a.momentCapture)
		r.Post("/v1/moments/search", a.momentSearch)
		r.Post("/v1/legacy/collections", a.legacyCollectionCreate)
		r.Post("/v1/legacy/collections/{id}/approve", a.legacyCollectionApprove)
		r.Get("/v1/legacy/collections/{id}/export", a.exportLegacy)
		r.Post("/v1/sharing", a.sharingManage)
		r.Post("/v1/finance/accounts", a.financeAccountCreate)
		r.Get("/v1/finance/accounts", a.financeAccounts)
		r.Get("/v1/finance/accounts/{id}", a.financeAccountGet)
		r.Post("/v1/finance/categories", a.financeCategoryCreate)
		r.Get("/v1/finance/categories", a.financeCategories)
		r.Post("/v1/finance/transactions", a.financeTransactionCreate)
		r.Get("/v1/finance/transactions", a.financeTransactions)
		r.Get("/v1/finance/transactions/{id}", a.financeTransactionGet)
		r.Patch("/v1/finance/transactions/{id}", a.financeTransactionPatch)
		r.Delete("/v1/finance/transactions/{id}", a.financeTransactionDelete)
		r.Get("/v1/finance/summary", a.financeSummary)
		r.Post("/v1/finance/liability-purchases", a.financeLiabilityPurchase)
		r.Get("/v1/finance/installments", a.financeInstallments)
		r.Post("/v1/finance/installments/{id}/payments", a.financeInstallmentPay)
	})
	return r
}
func jsonOK(w http.ResponseWriter, x any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(x)
}
func jsonCreated(w http.ResponseWriter, x any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(x)
}
func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		jsonErr(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		jsonErr(w, 400, "unexpected trailing JSON")
		return false
	}
	return true
}
func limit(r *http.Request, def, max int) int {
	x := def
	if v := r.URL.Query().Get("limit"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			x = n
		}
	}
	if x > max {
		x = max
	}
	return x
}
func internalErr(w http.ResponseWriter, err error) {
	log.Printf("request failed: %v", err)
	jsonErr(w, 500, "internal server error")
}
func requireText(s string, max int) bool { return strings.TrimSpace(s) != "" && len([]rune(s)) <= max }
func id(r *http.Request) string          { return chi.URLParam(r, "id") }

// Fixed SQL only: the agent never supplies SQL or owner identity.
func one(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, q string, args ...any) (json.RawMessage, error) {
	var b []byte
	err := db.QueryRow(ctx, "SELECT row_to_json(t) FROM ("+q+") AS t", args...).Scan(&b)
	return json.RawMessage(b), err
}
func many(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, q string, args ...any) (json.RawMessage, error) {
	var b []byte
	err := db.QueryRow(ctx, "SELECT coalesce(json_agg(row_to_json(t)),'[]'::json) FROM ("+q+") AS t", args...).Scan(&b)
	return json.RawMessage(b), err
}
func isNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
func (a *App) audit(ctx context.Context, tx pgx.Tx, action, resID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO mimo.audit_events(principal,scope_type,scope_id,action,resource_id,outcome) VALUES($1,'personal',$1,$2,$3::uuid,'allowed')`, a.owner(ctx), action, resID)
	return err
}
func (a *App) index(ctx context.Context, tx pgx.Tx, source, id, body string) error {
	hash := embedding.ContentHash(body)
	var chunkID string
	err := tx.QueryRow(ctx, `INSERT INTO mimo.search_chunks(owner_id,source_type,source_id,body,content_hash)
 VALUES($1,$2,$3::uuid,$4,$5)
 ON CONFLICT(owner_id,source_type,source_id) DO UPDATE SET body=EXCLUDED.body,content_hash=EXCLUDED.content_hash,updated_at=now()
 RETURNING id::text`, a.owner(ctx), source, id, body, hash).Scan(&chunkID)
	if err != nil {
		return err
	}
	if a.Embed != nil {
		_, err = tx.Exec(ctx, `INSERT INTO mimo.ingestion_jobs(chunk_id,model_id,content_hash,status,attempts,next_run_at)
    VALUES($1::uuid,$2,$3,'pending',0,now())
    ON CONFLICT(chunk_id,model_id) DO UPDATE SET content_hash=EXCLUDED.content_hash,status='pending',attempts=0,next_run_at=now(),updated_at=now()`, chunkID, a.C.EmbeddingModel, hash)
	}
	return err
}

// indexWorkspace keeps shared-domain indexing isolated by workspace UUID. It is
// intentionally best-effort: a slow or unavailable embedding worker must not
// make an otherwise valid knowledge or moment write fail.
func (a *App) indexWorkspace(ctx context.Context, workspaceID, source, sourceID, body string) error {
	hash := embedding.ContentHash(body)
	_, err := a.DB.Exec(ctx, `INSERT INTO mimo.search_chunks(owner_id,source_type,source_id,body,content_hash)
VALUES($1,$2,$3::uuid,$4,$5)
ON CONFLICT(owner_id,source_type,source_id) DO UPDATE SET body=EXCLUDED.body,content_hash=EXCLUDED.content_hash,updated_at=now()`, workspaceID, source, sourceID, body, hash)
	if err != nil {
		return err
	}
	if a.Embed == nil {
		return nil
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO mimo.ingestion_jobs(chunk_id,model_id,content_hash,status,attempts,next_run_at)
SELECT id,$2,$3,'pending',0,now() FROM mimo.search_chunks WHERE owner_id=$1 AND source_type=$4 AND source_id=$5::uuid
ON CONFLICT(chunk_id,model_id) DO UPDATE SET content_hash=EXCLUDED.content_hash,status='pending',attempts=0,next_run_at=now(),updated_at=now()`, workspaceID, a.C.EmbeddingModel, hash, source, sourceID)
	return err
}
func (a *App) unindex(ctx context.Context, tx pgx.Tx, source, id string) error {
	_, err := tx.Exec(ctx, `DELETE FROM mimo.search_chunks WHERE owner_id=$1 AND source_type=$2 AND source_id=$3::uuid`, a.owner(ctx), source, id)
	return err
}
