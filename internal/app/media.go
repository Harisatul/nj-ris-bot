package app

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const assetSelect = `SELECT id::text AS id,bucket_name,object_key,mime_type,size_bytes,caption,source_ref,captured_at,created_at FROM mimo.assets WHERE scope_type='personal' AND scope_id=$1 AND status='active'`

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func (a *App) assetUpload(w http.ResponseWriter, r *http.Request) {
	if a.R2 == nil {
		jsonErr(w, 503, "R2 not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.C.MaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		jsonErr(w, 413, "invalid or oversized multipart upload")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	caption := strings.TrimSpace(r.FormValue("caption"))
	if len(caption) > 4000 {
		jsonErr(w, 400, "caption too long")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, 400, "file form field is required")
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, a.C.MaxUploadBytes+1))
	if err != nil {
		internalErr(w, err)
		return
	}
	if len(content) == 0 || int64(len(content)) > a.C.MaxUploadBytes {
		jsonErr(w, 413, "empty or oversized file")
		return
	}
	mime := http.DetectContentType(content)
	switch mime {
	case "image/jpeg", "image/png", "image/webp", "application/pdf":
	default:
		jsonErr(w, 415, "supported formats: JPG, PNG, WebP, PDF")
		return
	}
	uid, err := newUUID()
	if err != nil {
		internalErr(w, err)
		return
	}
	ext := map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "application/pdf": "pdf"}[mime]
	key := fmt.Sprintf("personal/%s/%s.%s", a.C.OwnerID, uid, ext)
	if err = a.R2.Upload(r.Context(), key, mime, bytes.NewReader(content), int64(len(content))); err != nil {
		internalErr(w, err)
		return
	}
	sum := sha256.Sum256(content)
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		_ = a.R2.Delete(ctx, key)
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	source := "upload:" + filepath.Base(header.Filename)
	var rid string
	err = tx.QueryRow(ctx, `INSERT INTO mimo.assets(scope_type,scope_id,bucket_name,object_key,mime_type,size_bytes,sha256_hex,caption,source_ref,captured_at)
 VALUES('personal',$1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text`, a.C.OwnerID, a.C.R2Bucket, key, mime, len(content), hex.EncodeToString(sum[:]), caption, source, time.Now().UTC()).Scan(&rid)
	if err == nil {
		err = a.index(ctx, tx, "asset", rid, caption+" "+source)
	}
	if err == nil {
		err = a.audit(ctx, tx, "assets.upload", rid)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		_ = a.R2.Delete(ctx, key)
		internalErr(w, err)
		return
	}
	out, err := one(ctx, a.DB, assetSelect+` AND id=$2::uuid`, a.C.OwnerID, rid)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, out)
}
func (a *App) assetList(w http.ResponseWriter, r *http.Request) {
	data, err := many(r.Context(), a.DB, assetSelect+` ORDER BY created_at DESC LIMIT $2`, a.C.OwnerID, limit(r, 30, 100))
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": data})
}
func (a *App) assetGet(w http.ResponseWriter, r *http.Request) {
	out, err := one(r.Context(), a.DB, assetSelect+` AND id=$2::uuid`, a.C.OwnerID, id(r))
	if isNotFound(err) {
		jsonErr(w, 404, "asset not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid asset id")
		return
	}
	jsonOK(w, out)
}
func (a *App) assetDownload(w http.ResponseWriter, r *http.Request) {
	if a.R2 == nil {
		jsonErr(w, 503, "R2 not configured")
		return
	}
	var key, bucket string
	err := a.DB.QueryRow(r.Context(), `SELECT object_key,bucket_name FROM mimo.assets WHERE id=$1::uuid AND scope_type='personal' AND scope_id=$2 AND status='active'`, id(r), a.C.OwnerID).Scan(&key, &bucket)
	if isNotFound(err) {
		jsonErr(w, 404, "asset not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid asset id")
		return
	}
	if bucket != a.C.R2Bucket {
		jsonErr(w, 409, "asset belongs to a different R2 bucket")
		return
	}
	url, err := a.R2.DownloadURL(r.Context(), key)
	if err != nil {
		internalErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOK(w, map[string]any{"url": url, "expires_in_seconds": 300})
}
func (a *App) assetDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var key, bucket string
	err = tx.QueryRow(ctx, `UPDATE mimo.assets SET status='deleted' WHERE id=$1::uuid AND scope_type='personal' AND scope_id=$2 AND status='active' RETURNING object_key,bucket_name`, id(r), a.C.OwnerID).Scan(&key, &bucket)
	if isNotFound(err) {
		jsonErr(w, 404, "asset not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid asset id")
		return
	}
	if err = a.unindex(ctx, tx, "asset", id(r)); err == nil {
		err = a.audit(ctx, tx, "assets.delete", id(r))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	// Safe default: DB record is no longer retrievable. R2 deletion is best-effort;
	// reconcile orphan files with a scheduled retention/cleanup job before multiuser use.
	removed := false
	if a.R2 != nil && a.C.R2Bucket == bucket {
		removed = a.R2.Delete(ctx, key) == nil
	}
	jsonOK(w, map[string]any{"deleted": true, "r2_deleted": removed})
}
