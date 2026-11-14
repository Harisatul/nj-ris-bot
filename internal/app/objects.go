package app

import (
	"encoding/json"
	"net/http"
	"strings"
)

type objectInput struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Category    string         `json:"category"`
	Metadata    map[string]any `json:"metadata"`
}

const objectSelect = `SELECT id::text AS id,name,description,category,metadata,created_at,updated_at FROM mimo.objects WHERE owner_id=$1 AND deleted_at IS NULL`

func validObject(o objectInput) bool {
	return requireText(o.Name, 300) && len(o.Description) <= 20000 && len(o.Category) <= 120
}
func (a *App) objectCreate(w http.ResponseWriter, r *http.Request) {
	var d objectInput
	if !decode(w, r, &d) {
		return
	}
	if !validObject(d) {
		jsonErr(w, 400, "invalid object")
		return
	}
	metadata, _ := json.Marshal(d.Metadata)
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var rid string
	err = tx.QueryRow(ctx, `INSERT INTO mimo.objects(owner_id,name,description,category,metadata) VALUES($1,$2,$3,$4,$5::jsonb) RETURNING id::text`, a.owner(ctx), strings.TrimSpace(d.Name), d.Description, d.Category, string(metadata)).Scan(&rid)
	if err == nil {
		err = a.index(ctx, tx, "object", rid, d.Name+" "+d.Category+" "+d.Description)
	}
	if err == nil {
		err = a.audit(ctx, tx, "objects.create", rid)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	out, err := one(ctx, a.DB, objectSelect+` AND id=$2::uuid`, a.owner(ctx), rid)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, out)
}
func (a *App) objectList(w http.ResponseWriter, r *http.Request) {
	data, err := many(r.Context(), a.DB, objectSelect+` ORDER BY updated_at DESC LIMIT $2`, a.owner(r.Context()), limit(r, 30, 100))
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": data})
}
func (a *App) objectGet(w http.ResponseWriter, r *http.Request) {
	data, err := one(r.Context(), a.DB, `SELECT o.id::text AS id,o.name,o.description,o.category,o.metadata,o.created_at,o.updated_at,
 (SELECT coalesce(json_agg(json_build_object('id',a.id,'caption',a.caption,'mime_type',a.mime_type)),'[]'::json) FROM mimo.object_assets oa JOIN mimo.assets a ON a.id=oa.asset_id WHERE oa.object_id=o.id AND a.scope_type='personal' AND a.scope_id=$1 AND a.status='active') AS assets
	FROM mimo.objects o WHERE o.owner_id=$1 AND o.deleted_at IS NULL AND o.id=$2::uuid`, a.owner(r.Context()), id(r))
	if isNotFound(err) {
		jsonErr(w, 404, "object not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid object id")
		return
	}
	jsonOK(w, data)
}
func (a *App) objectPatch(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Name        *string         `json:"name"`
		Description *string         `json:"description"`
		Category    *string         `json:"category"`
		Metadata    *map[string]any `json:"metadata"`
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
	var old objectInput
	var metadata []byte
	err = tx.QueryRow(ctx, `SELECT name,description,coalesce(category,''),metadata FROM mimo.objects WHERE owner_id=$1 AND id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, a.owner(ctx), id(r)).Scan(&old.Name, &old.Description, &old.Category, &metadata)
	if isNotFound(err) {
		jsonErr(w, 404, "object not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid object id")
		return
	}
	_ = json.Unmarshal(metadata, &old.Metadata)
	if d.Name != nil {
		old.Name = *d.Name
	}
	if d.Description != nil {
		old.Description = *d.Description
	}
	if d.Category != nil {
		old.Category = *d.Category
	}
	if d.Metadata != nil {
		old.Metadata = *d.Metadata
	}
	if !validObject(old) {
		jsonErr(w, 400, "invalid object fields")
		return
	}
	meta, _ := json.Marshal(old.Metadata)
	_, err = tx.Exec(ctx, `UPDATE mimo.objects SET name=$1,description=$2,category=$3,metadata=$4::jsonb WHERE owner_id=$5 AND id=$6::uuid`, old.Name, old.Description, old.Category, string(meta), a.owner(ctx), id(r))
	if err == nil {
		err = a.index(ctx, tx, "object", id(r), old.Name+" "+old.Category+" "+old.Description)
	}
	if err == nil {
		err = a.audit(ctx, tx, "objects.update", id(r))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	out, err := one(ctx, a.DB, objectSelect+` AND id=$2::uuid`, a.owner(ctx), id(r))
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, out)
}
func (a *App) objectDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE mimo.objects SET deleted_at=now() WHERE owner_id=$1 AND id=$2::uuid AND deleted_at IS NULL`, a.owner(ctx), id(r))
	if err != nil {
		jsonErr(w, 400, "invalid object id")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, 404, "object not found")
		return
	}
	if err = a.unindex(ctx, tx, "object", id(r)); err == nil {
		err = a.audit(ctx, tx, "objects.delete", id(r))
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
func (a *App) objectAttachAsset(w http.ResponseWriter, r *http.Request) {
	var d struct {
		AssetID string `json:"asset_id"`
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
	tag, err := tx.Exec(ctx, `INSERT INTO mimo.object_assets(object_id,asset_id)
 SELECT o.id,a.id FROM mimo.objects o JOIN mimo.assets a ON a.id=$3::uuid
 WHERE o.id=$2::uuid AND o.owner_id=$1 AND o.deleted_at IS NULL AND a.scope_type='personal' AND a.scope_id=$1 AND a.status='active'
	 ON CONFLICT DO NOTHING`, a.owner(ctx), id(r), d.AssetID)
	if err != nil {
		jsonErr(w, 400, "invalid ids")
		return
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mimo.object_assets oa JOIN mimo.objects o ON o.id=oa.object_id JOIN mimo.assets a ON a.id=oa.asset_id WHERE oa.object_id=$1::uuid AND oa.asset_id=$2::uuid AND o.owner_id=$3 AND o.deleted_at IS NULL AND a.scope_type='personal' AND a.scope_id=$3 AND a.status='active')`, id(r), d.AssetID, a.owner(ctx)).Scan(&exists)
		if err != nil || !exists {
			jsonErr(w, 404, "object or asset not found")
			return
		}
	}
	if err = a.audit(ctx, tx, "objects.attach_asset", id(r)); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"attached": true})
}
