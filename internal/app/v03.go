package app

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"
)

type workspaceRequest struct {
	Workspace string `json:"workspace"`
}

func (a *App) workspace(w http.ResponseWriter, r *http.Request, slug, perm string) (string, bool) {
	if slug == "" {
		jsonErr(w, 400, "workspace is required")
		return "", false
	}
	if !a.requirePermission(w, r, slug, perm) {
		return "", false
	}
	var id string
	if err := a.DB.QueryRow(r.Context(), `SELECT id::text FROM mimo.workspaces WHERE slug=$1`, slug).Scan(&id); err != nil {
		jsonErr(w, 404, "workspace not found")
		return "", false
	}
	return id, true
}
func (a *App) knowledgeSourceCreate(w http.ResponseWriter, r *http.Request) {
	var d struct {
		workspaceRequest
		SourceType      string         `json:"source_type"`
		Title           string         `json:"title"`
		URL             string         `json:"url"`
		DOI             string         `json:"doi"`
		ISBN            string         `json:"isbn"`
		Description     string         `json:"description"`
		Authors         []string       `json:"authors"`
		PublicationDate *time.Time     `json:"publication_date"`
		Metadata        map[string]any `json:"metadata"`
	}
	if !decode(w, r, &d) || !requireText(d.Title, 500) {
		return
	}
	wid, ok := a.workspace(w, r, d.Workspace, "knowledge:write")
	if !ok {
		return
	}
	in, _ := identityFrom(r.Context())
	if d.Metadata == nil {
		d.Metadata = map[string]any{}
	}
	if d.Authors == nil {
		d.Authors = []string{}
	}
	var id string
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.knowledge_sources(workspace_id,source_type,title,authors,url,doi,isbn,publication_date,description,metadata,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11) RETURNING id::text`, wid, d.SourceType, strings.TrimSpace(d.Title), d.Authors, d.URL, d.DOI, d.ISBN, d.PublicationDate, d.Description, d.Metadata, in.PrincipalID).Scan(&id)
	if err != nil {
		log.Printf("knowledge source insert failed: %v", err)
		jsonErr(w, 409, "source could not be saved")
		return
	}
	jsonCreated(w, map[string]any{"id": id, "workspace": d.Workspace})
}
func (a *App) knowledgeNoteCreate(w http.ResponseWriter, r *http.Request) {
	var d struct {
		workspaceRequest
		SourceID       string `json:"source_id"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		NoteType       string `json:"note_type"`
		Visibility     string `json:"visibility"`
		AuthoredBy     string `json:"authored_by"`
		SourceVerified bool   `json:"source_verified"`
	}
	if !decode(w, r, &d) || !requireText(d.Title, 500) || len(d.Body) > 100000 {
		return
	}
	wid, ok := a.workspace(w, r, d.Workspace, "knowledge:write")
	if !ok {
		return
	}
	in, _ := identityFrom(r.Context())
	if d.SourceID != "" {
		var sourceWorkspace string
		if err := a.DB.QueryRow(r.Context(), `SELECT workspace_id::text FROM mimo.knowledge_sources WHERE id=$1::uuid`, d.SourceID).Scan(&sourceWorkspace); err != nil || sourceWorkspace != wid {
			jsonErr(w, 403, "source is outside the workspace")
			return
		}
	}
	if d.NoteType == "" {
		d.NoteType = "interpretation"
	}
	if d.Visibility == "" {
		d.Visibility = "workspace"
	}
	if d.AuthoredBy == "" {
		d.AuthoredBy = "human"
	}
	var id string
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.knowledge_notes(source_id,workspace_id,author_principal_id,title,body,note_type,visibility,authored_by,source_verified) VALUES(nullif($1,'')::uuid,$2,$3::uuid,$4,$5,$6,$7,$8,$9) RETURNING id::text`, d.SourceID, wid, in.PrincipalID, d.Title, d.Body, d.NoteType, d.Visibility, d.AuthoredBy, d.SourceVerified).Scan(&id)
	if err != nil {
		jsonErr(w, 400, "note could not be saved")
		return
	}
	if err := a.indexWorkspace(r.Context(), wid, "knowledge_note", id, d.Title+"\n"+d.Body); err != nil {
		log.Printf("knowledge note indexing deferred: %v", err)
	}
	jsonCreated(w, map[string]any{"id": id, "workspace": d.Workspace, "author_principal_id": in.PrincipalID, "authored_by": d.AuthoredBy})
}
func (a *App) knowledgeSearch(w http.ResponseWriter, r *http.Request) {
	var d struct {
		workspaceRequest
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !decode(w, r, &d) {
		return
	}
	if len(strings.TrimSpace(d.Query)) < 2 {
		jsonErr(w, 400, "query is required")
		return
	}
	wid, ok := a.workspace(w, r, d.Workspace, "knowledge:read")
	if !ok {
		return
	}
	if d.Limit < 1 {
		d.Limit = 10
	}
	if d.Limit > 30 {
		d.Limit = 30
	}
	rows, err := a.DB.Query(r.Context(), `SELECT c.source_type,c.source_id::text,left(c.body,800),c.updated_at FROM mimo.search_chunks c WHERE c.owner_id=$1 AND c.source_type IN ('knowledge_note','knowledge_discussion','moment') AND (c.source_type='knowledge_note' AND EXISTS (SELECT 1 FROM mimo.knowledge_notes n WHERE n.id=c.source_id AND n.workspace_id=$1::uuid AND n.visibility='workspace') OR c.source_type='knowledge_discussion' AND EXISTS (SELECT 1 FROM mimo.knowledge_discussions d WHERE d.id=c.source_id AND d.workspace_id=$1::uuid) OR c.source_type='moment' AND EXISTS (SELECT 1 FROM mimo.moments m WHERE m.id=c.source_id AND m.workspace_id=$1::uuid AND m.visibility='workspace')) AND (c.search_tsv @@ plainto_tsquery('simple',$2) OR c.body ILIKE '%'||$2||'%') ORDER BY c.updated_at DESC LIMIT $3`, wid, d.Query, d.Limit)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var sourceType, sourceID, body string
		var at time.Time
		if rows.Scan(&sourceType, &sourceID, &body, &at) == nil {
			items = append(items, map[string]any{"source_type": sourceType, "id": sourceID, "snippet": body, "created_at": at, "lexical": true})
		}
	}
	semantic := false
	if a.Embed != nil {
		if vector, e := a.Embed.Embed(r.Context(), d.Query); e == nil {
			semanticRows, e := a.DB.Query(r.Context(), `SELECT c.source_type,c.source_id::text,left(c.body,800),(1-(e.embedding <=> (($3::text)::vector)))::float8 FROM mimo.semantic_embeddings e JOIN mimo.search_chunks c ON c.id=e.chunk_id AND c.content_hash=e.content_hash WHERE c.owner_id=$1 AND e.model_id=$2 AND e.dimensions=$4 AND c.source_type IN ('knowledge_note','knowledge_discussion','moment') AND (c.source_type='knowledge_note' AND EXISTS (SELECT 1 FROM mimo.knowledge_notes n WHERE n.id=c.source_id AND n.workspace_id=$1::uuid AND n.visibility='workspace') OR c.source_type='knowledge_discussion' AND EXISTS (SELECT 1 FROM mimo.knowledge_discussions d WHERE d.id=c.source_id AND d.workspace_id=$1::uuid) OR c.source_type='moment' AND EXISTS (SELECT 1 FROM mimo.moments m WHERE m.id=c.source_id AND m.workspace_id=$1::uuid AND m.visibility='workspace')) ORDER BY e.embedding <=> (($3::text)::vector) LIMIT $5`, wid, a.C.EmbeddingModel, vectorText(vector), a.C.EmbeddingDimensions, d.Limit)
			if e == nil {
				defer semanticRows.Close()
				for semanticRows.Next() {
					var typ, sid, snippet string
					var score float64
					if semanticRows.Scan(&typ, &sid, &snippet, &score) == nil {
						items = append(items, map[string]any{"source_type": typ, "id": sid, "snippet": snippet, "score": score, "semantic": true})
					}
				}
				semantic = true
			}
		}
	}
	jsonOK(w, map[string]any{"items": items, "workspace": d.Workspace, "semantic_enabled": semantic})
}

func (a *App) knowledgeDiscussionCreate(w http.ResponseWriter, r *http.Request) {
	var d struct {
		workspaceRequest
		SourceID     string     `json:"source_id"`
		Title        string     `json:"title"`
		Body         string     `json:"body"`
		Synthesis    string     `json:"synthesis"`
		AuthoredBy   string     `json:"authored_by"`
		Perspectives []any      `json:"perspectives"`
		SourceRefs   []any      `json:"source_refs"`
		Questions    []string   `json:"unresolved_questions"`
		DiscussedAt  *time.Time `json:"discussed_at"`
	}
	if !decode(w, r, &d) || !requireText(d.Title, 500) || len(d.Body) > 100000 {
		return
	}
	wid, ok := a.workspace(w, r, d.Workspace, "knowledge:write")
	if !ok {
		return
	}
	in, _ := identityFrom(r.Context())
	if d.Perspectives == nil {
		d.Perspectives = []any{}
	}
	if d.SourceRefs == nil {
		d.SourceRefs = []any{}
	}
	if d.Questions == nil {
		d.Questions = []string{}
	}
	if d.SourceID != "" {
		var sourceWorkspace string
		if err := a.DB.QueryRow(r.Context(), `SELECT workspace_id::text FROM mimo.knowledge_sources WHERE id=$1::uuid`, d.SourceID).Scan(&sourceWorkspace); err != nil || sourceWorkspace != wid {
			jsonErr(w, 403, "source is outside the workspace")
			return
		}
	}
	if d.AuthoredBy == "" {
		d.AuthoredBy = "human"
	}
	if d.AuthoredBy != "human" && d.AuthoredBy != "ai" {
		jsonErr(w, 400, "authored_by must be human or ai")
		return
	}
	var id string
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.knowledge_discussions(workspace_id,source_id,title,body,perspectives,synthesis,unresolved_questions,authored_by,created_by,discussed_at,source_refs) VALUES($1,nullif($2,'')::uuid,$3,$4,$5::jsonb,nullif($6,''),$7,$8,$9::uuid,coalesce($10,now()),$11::jsonb) RETURNING id::text`, wid, d.SourceID, d.Title, d.Body, d.Perspectives, d.Synthesis, d.Questions, d.AuthoredBy, in.PrincipalID, d.DiscussedAt, d.SourceRefs).Scan(&id)
	if err != nil {
		log.Printf("knowledge discussion insert failed: %v", err)
		jsonErr(w, 400, "discussion could not be saved")
		return
	}
	if err := a.indexWorkspace(r.Context(), wid, "knowledge_discussion", id, d.Title+"\n"+d.Body+"\n"+d.Synthesis); err != nil {
		log.Printf("knowledge discussion indexing deferred: %v", err)
	}
	jsonCreated(w, map[string]any{"id": id, "workspace": d.Workspace, "authored_by": d.AuthoredBy, "created_by": in.PrincipalID})
}

func (a *App) momentCapture(w http.ResponseWriter, r *http.Request) {
	var d struct {
		workspaceRequest
		Title        string     `json:"title"`
		Description  string     `json:"description"`
		Location     string     `json:"location"`
		OccurredAt   *time.Time `json:"occurred_at"`
		Participants []string   `json:"participants"`
		Tags         []string   `json:"tags"`
		Visibility   string     `json:"visibility"`
		SourceRefs   []any      `json:"source_refs"`
	}
	if !decode(w, r, &d) || !requireText(d.Title, 500) {
		return
	}
	wid, ok := a.workspace(w, r, d.Workspace, "moments:write")
	if !ok {
		return
	}
	in, _ := identityFrom(r.Context())
	if d.SourceRefs == nil {
		d.SourceRefs = []any{}
	}
	if d.Participants == nil {
		d.Participants = []string{}
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	if d.Visibility == "" {
		d.Visibility = "workspace"
	}
	var id string
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.moments(workspace_id,title,description,occurred_at,created_by,participants,location,tags,visibility,source_refs) VALUES($1,$2,$3,$4,$5::uuid,$6,$7,$8,$9,$10::jsonb) RETURNING id::text`, wid, d.Title, d.Description, d.OccurredAt, in.PrincipalID, d.Participants, d.Location, d.Tags, d.Visibility, d.SourceRefs).Scan(&id)
	if err != nil {
		jsonErr(w, 400, "moment could not be saved")
		return
	}
	if err := a.indexWorkspace(r.Context(), wid, "moment", id, d.Title+"\n"+d.Description+"\n"+strings.Join(d.Tags, " ")); err != nil {
		log.Printf("moment indexing deferred: %v", err)
	}
	jsonCreated(w, map[string]any{"id": id, "workspace": d.Workspace, "recorded_at": time.Now().UTC()})
}
func (a *App) momentSearch(w http.ResponseWriter, r *http.Request) {
	var d struct {
		workspaceRequest
		Query string     `json:"query"`
		From  *time.Time `json:"from"`
		To    *time.Time `json:"to"`
		Limit int        `json:"limit"`
	}
	if !decode(w, r, &d) {
		return
	}
	wid, ok := a.workspace(w, r, d.Workspace, "moments:read")
	if !ok {
		return
	}
	if d.Limit < 1 {
		d.Limit = 30
	}
	if d.Limit > 100 {
		d.Limit = 100
	}
	rows, err := a.DB.Query(r.Context(), `SELECT id::text,title,description,occurred_at,recorded_at,created_by::text,participants,tags,location FROM mimo.moments WHERE workspace_id=$1 AND ($2='' OR title ILIKE '%'||$2||'%' OR description ILIKE '%'||$2||'%') AND ($3::timestamptz IS NULL OR occurred_at >= $3) AND ($4::timestamptz IS NULL OR occurred_at < $4) ORDER BY occurred_at DESC NULLS LAST,recorded_at DESC LIMIT $5`, wid, d.Query, d.From, d.To, d.Limit)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, title, desc, creator string
		var occurred, recorded *time.Time
		var p, tags []string
		var loc *string
		if rows.Scan(&id, &title, &desc, &occurred, &recorded, &creator, &p, &tags, &loc) == nil {
			items = append(items, map[string]any{"id": id, "title": title, "description": desc, "occurred_at": occurred, "recorded_at": recorded, "created_by": creator, "participants": p, "tags": tags, "location": loc})
		}
	}
	jsonOK(w, map[string]any{"items": items, "workspace": d.Workspace})
}

func (a *App) legacyCollectionCreate(w http.ResponseWriter, r *http.Request) {
	var d struct {
		workspaceRequest
		Slug        string `json:"slug"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if !decode(w, r, &d) || !requireText(d.Slug, 120) || !requireText(d.Title, 500) {
		return
	}
	wid, ok := a.workspace(w, r, d.Workspace, "legacy:manage")
	if !ok {
		return
	}
	in, _ := identityFrom(r.Context())
	var id string
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.legacy_collections(workspace_id,slug,title,description,created_by) VALUES($1,$2,$3,$4,$5::uuid) RETURNING id::text`, wid, d.Slug, d.Title, d.Description, in.PrincipalID).Scan(&id)
	if err != nil {
		jsonErr(w, 409, "collection already exists")
		return
	}
	jsonCreated(w, map[string]any{"id": id, "status": "draft"})
}

func (a *App) legacyCollectionApprove(w http.ResponseWriter, r *http.Request) {
	workspace := r.URL.Query().Get("workspace")
	wid, ok := a.workspace(w, r, workspace, "legacy:manage")
	if !ok {
		return
	}
	in, ok := identityFrom(r.Context())
	if !ok {
		jsonErr(w, 401, "authentication required")
		return
	}
	var human bool
	if err := a.DB.QueryRow(r.Context(), `SELECT kind='human' FROM mimo.principals WHERE id=$1::uuid`, in.PrincipalID).Scan(&human); err != nil || !human {
		jsonErr(w, 403, "only human principals may approve legacy content")
		return
	}
	var collectionWorkspace string
	if err := a.DB.QueryRow(r.Context(), `SELECT workspace_id::text FROM mimo.legacy_collections WHERE id=$1::uuid`, id(r)).Scan(&collectionWorkspace); err != nil || collectionWorkspace != wid {
		jsonErr(w, 404, "collection not found")
		return
	}
	if _, err := a.DB.Exec(r.Context(), `INSERT INTO mimo.legacy_approvals(collection_id,principal_id) VALUES($1::uuid,$2::uuid) ON CONFLICT DO NOTHING`, id(r), in.PrincipalID); err != nil {
		internalErr(w, err)
		return
	}
	var humans, approvals int
	err := a.DB.QueryRow(r.Context(), `SELECT count(*) FILTER (WHERE p.kind='human'), count(DISTINCT la.principal_id) FILTER (WHERE p2.kind='human') FROM mimo.workspace_members wm JOIN mimo.principals p ON p.id=wm.principal_id LEFT JOIN mimo.legacy_approvals la ON la.collection_id=$1::uuid AND la.principal_id=wm.principal_id LEFT JOIN mimo.principals p2 ON p2.id=la.principal_id WHERE wm.workspace_id=$2::uuid AND wm.revoked_at IS NULL`, id(r), wid).Scan(&humans, &approvals)
	if err != nil {
		internalErr(w, err)
		return
	}
	if humans != 2 {
		jsonErr(w, 409, "joint legacy approval requires exactly two active human members")
		return
	}
	if approvals < humans {
		jsonOK(w, map[string]any{"approved": false, "approvals": approvals, "required": humans})
		return
	}
	if _, err = a.DB.Exec(r.Context(), `UPDATE mimo.legacy_collections SET status='approved',updated_at=now() WHERE id=$1::uuid AND workspace_id=$2::uuid`, id(r), wid); err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"approved": true, "approvals": approvals, "required": humans})
}

func (a *App) sharingManage(w http.ResponseWriter, r *http.Request) {
	var d struct {
		workspaceRequest
		Action     string `json:"action"`
		Principal  string `json:"principal"`
		Role       string `json:"role"`
		Permission string `json:"permission"`
	}
	if !decode(w, r, &d) {
		return
	}
	wid, ok := a.workspace(w, r, d.Workspace, "legacy:manage")
	if !ok {
		return
	}
	in, ok := identityFrom(r.Context())
	if !ok {
		jsonErr(w, 401, "authentication required")
		return
	}
	var owner bool
	if err := a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mimo.workspace_members WHERE workspace_id=$1::uuid AND principal_id=$2::uuid AND role='owner' AND revoked_at IS NULL)`, wid, in.PrincipalID).Scan(&owner); err != nil || !owner {
		jsonErr(w, 403, "workspace owner required")
		return
	}
	var pid string
	if err := a.DB.QueryRow(r.Context(), `SELECT id::text FROM mimo.principals WHERE slug=$1`, d.Principal).Scan(&pid); err != nil {
		jsonErr(w, 404, "principal not found")
		return
	}
	if d.Action == "revoke" {
		if _, err := a.DB.Exec(r.Context(), `UPDATE mimo.workspace_members SET revoked_at=now() WHERE workspace_id=$1::uuid AND principal_id=$2::uuid`, wid, pid); err != nil {
			internalErr(w, err)
			return
		}
		jsonOK(w, map[string]any{"revoked": true})
		return
	}
	if d.Action != "grant" || (d.Role != "owner" && d.Role != "editor" && d.Role != "reader" && d.Role != "agent") {
		jsonErr(w, 400, "action and role are invalid")
		return
	}
	if _, err := a.DB.Exec(r.Context(), `INSERT INTO mimo.workspace_members(workspace_id,principal_id,role) VALUES($1::uuid,$2::uuid,$3) ON CONFLICT(workspace_id,principal_id) DO UPDATE SET role=EXCLUDED.role,revoked_at=NULL`, wid, pid, d.Role); err != nil {
		internalErr(w, err)
		return
	}
	if d.Permission != "" {
		if _, err := a.DB.Exec(r.Context(), `INSERT INTO mimo.permissions(principal_id,workspace_id,permission) VALUES($1::uuid,$2::uuid,$3) ON CONFLICT DO NOTHING`, pid, wid, d.Permission); err != nil {
			internalErr(w, err)
			return
		}
	}
	jsonOK(w, map[string]any{"granted": true, "principal": d.Principal, "role": d.Role})
}
func (a *App) exportLegacy(w http.ResponseWriter, r *http.Request) {
	id := id(r)
	workspace := r.URL.Query().Get("workspace")
	wid, ok := a.workspace(w, r, workspace, "legacy:manage")
	if !ok {
		return
	}
	var title, desc, status string
	if err := a.DB.QueryRow(r.Context(), `SELECT title,description,status FROM mimo.legacy_collections WHERE id=$1::uuid AND workspace_id=$2::uuid`, id, wid).Scan(&title, &desc, &status); err != nil {
		jsonErr(w, 404, "collection not found")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=legacy-"+hex.EncodeToString(sha256.New().Sum(nil))+".md")
	_, _ = w.Write([]byte("# " + title + "\n\n" + desc + "\n\nStatus: " + status + "\n"))
}
