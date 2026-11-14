package app

import (
	"net/http"
	"strings"
	"time"
)

type memoryInput struct {
	Title      string     `json:"title"`
	Summary    string     `json:"summary"`
	Category   string     `json:"category"`
	Importance int        `json:"importance"`
	OccurredAt *time.Time `json:"occurred_at"`
}

const memorySelect = `SELECT id::text AS id,title,summary,category,importance,occurred_at,created_at FROM mimo.memory_entries WHERE scope_type='personal' AND scope_id=$1 AND status='active' AND (expires_at IS NULL OR expires_at>now())`

func (a *App) memoryCreate(w http.ResponseWriter, r *http.Request) {
	var d memoryInput
	if !decode(w, r, &d) {
		return
	}
	if d.Importance == 0 {
		d.Importance = 3
	}
	if !requireText(d.Title, 300) || !requireText(d.Summary, 8000) || d.Importance < 1 || d.Importance > 5 {
		jsonErr(w, 400, "invalid memory")
		return
	}
	switch d.Category {
	case "fact", "preference", "moment", "plan", "summary", "journal":
	default:
		jsonErr(w, 400, "invalid memory category")
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var rid string
	err = tx.QueryRow(ctx, `INSERT INTO mimo.memory_entries(scope_type,scope_id,title,summary,category,importance,occurred_at,source_kind)
 VALUES('personal',$1,$2,$3,$4,$5,$6,'manual') RETURNING id::text`, a.owner(ctx), strings.TrimSpace(d.Title), strings.TrimSpace(d.Summary), d.Category, d.Importance, d.OccurredAt).Scan(&rid)
	if err == nil {
		err = a.index(ctx, tx, "memory", rid, d.Title+" "+d.Summary)
	}
	if err == nil {
		err = a.audit(ctx, tx, "memories.create", rid)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	out, err := one(ctx, a.DB, memorySelect+` AND id=$2::uuid`, a.owner(ctx), rid)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, out)
}
func (a *App) memoryList(w http.ResponseWriter, r *http.Request) {
	data, err := many(r.Context(), a.DB, memorySelect+` ORDER BY created_at DESC LIMIT $2`, a.owner(r.Context()), limit(r, 30, 100))
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": data})
}
func (a *App) memoryDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE mimo.memory_entries SET status='deleted' WHERE scope_type='personal' AND scope_id=$1 AND id=$2::uuid AND status='active'`, a.owner(ctx), id(r))
	if err != nil {
		jsonErr(w, 400, "invalid memory id")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, 404, "memory not found")
		return
	}
	if err = a.unindex(ctx, tx, "memory", id(r)); err == nil {
		err = a.audit(ctx, tx, "memories.delete", id(r))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"deleted": true})
}
