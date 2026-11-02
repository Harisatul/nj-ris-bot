package app

import (
	"net/http"
	"strings"
)

type noteInput struct {
	Title   string   `json:"title"`
	Content string   `json:"content"`
	Kind    string   `json:"kind"`
	Tags    []string `json:"tags"`
}

const noteSelect = `SELECT id::text AS id,kind,title,content,tags,created_at,updated_at FROM mimo.notes WHERE owner_id=$1 AND deleted_at IS NULL`

func (a *App) noteCreate(w http.ResponseWriter, r *http.Request) {
	var d noteInput
	if !decode(w, r, &d) {
		return
	}
	if d.Kind == "" {
		d.Kind = "general"
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if !validNote(d) {
		jsonErr(w, 400, "invalid title/kind/content/tags")
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
	err = tx.QueryRow(ctx, `INSERT INTO mimo.notes(owner_id,kind,title,content,tags) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, a.C.OwnerID, d.Kind, strings.TrimSpace(d.Title), d.Content, d.Tags).Scan(&rid)
	if err == nil {
		err = a.index(ctx, tx, "note", rid, d.Title+"\n"+d.Content)
	}
	if err == nil {
		err = a.audit(ctx, tx, "notes.create", rid)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	data, err := one(ctx, a.DB, noteSelect+` AND id=$2::uuid`, a.C.OwnerID, rid)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, data)
}
func validNote(d noteInput) bool {
	if !requireText(d.Title, 300) || len(d.Content) > 100000 {
		return false
	}
	switch d.Kind {
	case "general", "work", "idea", "journal":
	default:
		return false
	}
	if len(d.Tags) > 30 {
		return false
	}
	for _, t := range d.Tags {
		if !requireText(t, 50) {
			return false
		}
	}
	return true
}
func (a *App) noteList(w http.ResponseWriter, r *http.Request) {
	data, err := many(r.Context(), a.DB, noteSelect+` ORDER BY updated_at DESC LIMIT $2`, a.C.OwnerID, limit(r, 30, 100))
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": data})
}
func (a *App) noteGet(w http.ResponseWriter, r *http.Request) {
	data, err := one(r.Context(), a.DB, noteSelect+` AND id=$2::uuid`, a.C.OwnerID, id(r))
	if isNotFound(err) {
		jsonErr(w, 404, "note not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid note id")
		return
	}
	jsonOK(w, data)
}
func (a *App) notePatch(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Title   *string   `json:"title"`
		Content *string   `json:"content"`
		Kind    *string   `json:"kind"`
		Tags    *[]string `json:"tags"`
	}
	if !decode(w, r, &d) {
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var old noteInput
	err = tx.QueryRow(ctx, `SELECT title,content,kind,tags FROM mimo.notes WHERE owner_id=$1 AND id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, a.C.OwnerID, id(r)).Scan(&old.Title, &old.Content, &old.Kind, &old.Tags)
	if isNotFound(err) {
		jsonErr(w, 404, "note not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid note id")
		return
	}
	dnext := old
	if d.Title != nil {
		dnext.Title = *d.Title
	}
	if d.Content != nil {
		dnext.Content = *d.Content
	}
	if d.Kind != nil {
		dnext.Kind = *d.Kind
	}
	if d.Tags != nil {
		dnext.Tags = *d.Tags
	}
	if !validNote(dnext) {
		jsonErr(w, 400, "invalid note fields")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO mimo.note_revisions(note_id,previous_title,previous_content) VALUES($1::uuid,$2,$3)`, id(r), old.Title, old.Content)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE mimo.notes SET title=$1,content=$2,kind=$3,tags=$4 WHERE id=$5::uuid AND owner_id=$6`, dnext.Title, dnext.Content, dnext.Kind, dnext.Tags, id(r), a.C.OwnerID)
	}
	if err == nil {
		err = a.index(ctx, tx, "note", id(r), dnext.Title+"\n"+dnext.Content)
	}
	if err == nil {
		err = a.audit(ctx, tx, "notes.update", id(r))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	data, err := one(ctx, a.DB, noteSelect+` AND id=$2::uuid`, a.C.OwnerID, id(r))
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, data)
}
func (a *App) noteDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE mimo.notes SET deleted_at=now() WHERE owner_id=$1 AND id=$2::uuid AND deleted_at IS NULL`, a.C.OwnerID, id(r))
	if err != nil {
		jsonErr(w, 400, "invalid note id")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, 404, "note not found")
		return
	}
	if err = a.unindex(ctx, tx, "note", id(r)); err == nil {
		err = a.audit(ctx, tx, "notes.delete", id(r))
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
