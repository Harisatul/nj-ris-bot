package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type activityInput struct {
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Category        string     `json:"category"`
	Tags            []string   `json:"tags"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	EventType       string     `json:"event_type"`
	TimePrecision   string     `json:"time_precision"`
	SourceKind      string     `json:"source_kind"`
	SourceMessageID string     `json:"source_message_id"`
}
type activityRecord struct {
	ID              string     `json:"id"`
	OwnerID         string     `json:"owner_id,omitempty"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Category        string     `json:"category"`
	Tags            []string   `json:"tags"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
	EventType       string     `json:"event_type"`
	TimePrecision   string     `json:"time_precision"`
	SourceKind      string     `json:"source_kind"`
	SourceMessageID string     `json:"source_message_id,omitempty"`
	Status          string     `json:"status"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func validateActivity(d activityInput) string {
	if !requireText(d.Title, 500) {
		return "title is required and must be <= 500 characters"
	}
	if len(d.Description) > 10000 {
		return "description is too long"
	}
	if len(d.Tags) > 50 {
		return "too many tags"
	}
	if d.StartedAt.IsZero() {
		return "started_at is required"
	}
	if d.EndedAt != nil && d.EndedAt.Before(d.StartedAt) {
		return "ended_at must be at or after started_at"
	}
	if d.Category == "" {
		d.Category = "other"
	}
	if d.EventType != "" && d.EventType != "activity" && d.EventType != "habit" && d.EventType != "checkin" {
		return "invalid event_type"
	}
	if d.TimePrecision != "" && d.TimePrecision != "exact" && d.TimePrecision != "approximate" && d.TimePrecision != "date_only" && d.TimePrecision != "unknown" {
		return "invalid time_precision"
	}
	if d.SourceKind != "" && d.SourceKind != "whatsapp" && d.SourceKind != "manual" && d.SourceKind != "api" && d.SourceKind != "checkin" && d.SourceKind != "system" {
		return "invalid source_kind"
	}
	return ""
}
func normalizeActivity(d *activityInput) {
	d.Title = strings.TrimSpace(d.Title)
	d.Category = strings.TrimSpace(d.Category)
	if d.Category == "" {
		d.Category = "other"
	}
	if d.EventType == "" {
		d.EventType = "activity"
	}
	if d.TimePrecision == "" {
		d.TimePrecision = "exact"
	}
	if d.SourceKind == "" {
		d.SourceKind = "manual"
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
}
func requestHash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func idemKey(r *http.Request, supplied string) string {
	if v := strings.TrimSpace(r.Header.Get("Idempotency-Key")); v != "" {
		return v
	}
	return strings.TrimSpace(supplied)
}
func idemReplay(ctx context.Context, tx pgx.Tx, owner, key, hash string) (json.RawMessage, int, bool, error) {
	if key == "" {
		return nil, 0, false, nil
	}
	var oldHash string
	var status int
	var body []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,status_code,response_json FROM mimo.api_idempotency WHERE owner_id=$1 AND idempotency_key=$2`, owner, key).Scan(&oldHash, &status, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if oldHash != hash {
		return nil, http.StatusConflict, true, errors.New("idempotency key reused with different payload")
	}
	return body, status, true, nil
}

func lockIdempotency(ctx context.Context, tx pgx.Tx, owner, key string) error {
	if key == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, owner+":"+key)
	return err
}

const activitySelect = `SELECT id::text,title,description,category,tags,started_at,ended_at,event_type,time_precision,source_kind,coalesce(source_message_id,''),status,version,created_at,updated_at FROM mimo.activity_events WHERE owner_id=$1`

func scanActivity(row pgx.Row) (activityRecord, error) {
	var x activityRecord
	err := row.Scan(&x.ID, &x.Title, &x.Description, &x.Category, &x.Tags, &x.StartedAt, &x.EndedAt, &x.EventType, &x.TimePrecision, &x.SourceKind, &x.SourceMessageID, &x.Status, &x.Version, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}
func scanActivities(rows pgx.Rows) ([]activityRecord, error) {
	defer rows.Close()
	out := []activityRecord{}
	for rows.Next() {
		var x activityRecord
		if err := rows.Scan(&x.ID, &x.Title, &x.Description, &x.Category, &x.Tags, &x.StartedAt, &x.EndedAt, &x.EventType, &x.TimePrecision, &x.SourceKind, &x.SourceMessageID, &x.Status, &x.Version, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (a *App) activityBatch(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Activities     []activityInput `json:"activities"`
		IdempotencyKey string          `json:"idempotency_key"`
	}
	if !decode(w, r, &d) || len(d.Activities) == 0 || len(d.Activities) > 100 {
		jsonErr(w, 400, "activities must contain 1..100 items")
		return
	}
	for i := range d.Activities {
		normalizeActivity(&d.Activities[i])
		if e := validateActivity(d.Activities[i]); e != "" {
			jsonErr(w, 400, fmt.Sprintf("activities[%d]: %s", i, e))
			return
		}
	}
	key := idemKey(r, d.IdempotencyKey)
	if len(key) > 200 {
		jsonErr(w, 400, "idempotency key too long")
		return
	}
	a.activityBatchInsert(w, r, d.Activities, key, requestHash(d.Activities))
}

// activityBatchInsert is the actual UUID-safe implementation; kept separate so the public handler stays small.
func (a *App) activityBatchInsert(w http.ResponseWriter, r *http.Request, vals []activityInput, key, hash string) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = lockIdempotency(ctx, tx, a.owner(ctx), key); err != nil {
		internalErr(w, err)
		return
	}
	if body, status, ok, e := idemReplay(ctx, tx, a.owner(ctx), key, hash); ok {
		if e != nil {
			jsonErr(w, status, e.Error())
			return
		}
		_ = tx.Commit(ctx)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	in, _ := identityFrom(ctx)
	out := []activityRecord{}
	for _, v := range vals {
		var x activityRecord
		err = tx.QueryRow(ctx, `INSERT INTO mimo.activity_events(owner_id,principal_id,workspace_id,title,description,category,tags,started_at,ended_at,event_type,time_precision,source_kind,source_message_id) VALUES($1,$2::uuid,nullif($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10,$11,$12,nullif($13,'')) RETURNING id::text,title,description,category,tags,started_at,ended_at,event_type,time_precision,source_kind,coalesce(source_message_id,''),status,version,created_at,updated_at`, a.owner(ctx), in.PrincipalID, in.DefaultWorkspaceID, v.Title, v.Description, v.Category, v.Tags, v.StartedAt, v.EndedAt, v.EventType, v.TimePrecision, v.SourceKind, v.SourceMessageID).Scan(&x.ID, &x.Title, &x.Description, &x.Category, &x.Tags, &x.StartedAt, &x.EndedAt, &x.EventType, &x.TimePrecision, &x.SourceKind, &x.SourceMessageID, &x.Status, &x.Version, &x.CreatedAt, &x.UpdatedAt)
		if err != nil {
			internalErr(w, err)
			return
		}
		out = append(out, x)
	}
	resp := map[string]any{"activities": out}
	body, _ := json.Marshal(resp)
	if key != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO mimo.api_idempotency(owner_id,idempotency_key,request_hash,status_code,response_json) VALUES($1,$2,$3,201,$4::jsonb)`, a.owner(ctx), key, hash, body); err != nil {
			internalErr(w, err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		internalErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(201)
	_, _ = w.Write(body)
}

func (a *App) activityList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limitN := limit(r, 50, 200)
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	args := []any{a.owner(r.Context())}
	where := []string{}
	add := func(s string, v any) { args = append(args, v); where = append(where, fmt.Sprintf(s, len(args))) }
	if v := q.Get("from"); v != "" {
		t, e := time.Parse(time.RFC3339, v)
		if e != nil {
			jsonErr(w, 400, "from must be RFC3339")
			return
		}
		add("started_at >= $%d", t)
	}
	if v := q.Get("to"); v != "" {
		t, e := time.Parse(time.RFC3339, v)
		if e != nil {
			jsonErr(w, 400, "to must be RFC3339")
			return
		}
		add("started_at < $%d", t)
	}
	if v := q.Get("category"); v != "" {
		add("category=$%d", v)
	}
	if v := q.Get("status"); v != "" {
		add("status=$%d", v)
	}
	if v := q.Get("q"); v != "" {
		n := len(args) + 1
		args = append(args, v, v)
		where = append(where, fmt.Sprintf("(title ILIKE '%%'||$%d||'%%' OR description ILIKE '%%'||$%d||'%%')", n, n+1))
	}
	args = append(args, limitN, offset)
	order := "started_at DESC,id DESC"
	if q.Get("sort") == "asc" {
		order = "started_at ASC,id ASC"
	}
	query := activitySelect
	if len(where) > 0 {
		query += " AND " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + order + fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := a.DB.Query(r.Context(), query, args...)
	if err != nil {
		internalErr(w, err)
		return
	}
	items, err := scanActivities(rows)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": items, "limit": limitN, "offset": offset})
}
func (a *App) activityGet(w http.ResponseWriter, r *http.Request) {
	x, e := scanActivity(a.DB.QueryRow(r.Context(), activitySelect+" AND id=$2::uuid", a.owner(r.Context()), id(r)))
	if errors.Is(e, pgx.ErrNoRows) {
		jsonErr(w, 404, "activity not found")
		return
	}
	if e != nil {
		internalErr(w, e)
		return
	}
	jsonOK(w, x)
}

func (a *App) activityPatch(w http.ResponseWriter, r *http.Request) {
	var d struct {
		activityInput
		Version        int    `json:"version"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !decode(w, r, &d) {
		return
	}
	if d.Version < 1 {
		jsonErr(w, 400, "version is required")
		return
	}
	key := idemKey(r, d.IdempotencyKey)
	hash := requestHash(d)
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		internalErr(w, e)
		return
	}
	defer tx.Rollback(ctx)
	if e = lockIdempotency(ctx, tx, a.owner(ctx), key); e != nil {
		internalErr(w, e)
		return
	}
	if body, status, ok, e := idemReplay(ctx, tx, a.owner(ctx), key, hash); ok {
		if e != nil {
			jsonErr(w, status, e.Error())
			return
		}
		_ = tx.Commit(ctx)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	var old []byte
	var x activityRecord
	e = tx.QueryRow(ctx, activitySelect+" AND id=$2::uuid FOR UPDATE", a.owner(ctx), id(r)).Scan(&x.ID, &x.Title, &x.Description, &x.Category, &x.Tags, &x.StartedAt, &x.EndedAt, &x.EventType, &x.TimePrecision, &x.SourceKind, &x.SourceMessageID, &x.Status, &x.Version, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(e, pgx.ErrNoRows) {
		jsonErr(w, 404, "activity not found")
		return
	}
	if e != nil {
		internalErr(w, e)
		return
	}
	if x.Version != d.Version {
		jsonErr(w, 409, "stale activity version")
		return
	}
	if d.Title == "" {
		d.Title = x.Title
	}
	if d.Description == "" {
		d.Description = x.Description
	}
	if d.Category == "" {
		d.Category = x.Category
	}
	if d.Tags == nil {
		d.Tags = x.Tags
	}
	if d.StartedAt.IsZero() {
		d.StartedAt = x.StartedAt
	}
	if d.EndedAt == nil {
		d.EndedAt = x.EndedAt
	}
	if d.EventType == "" {
		d.EventType = x.EventType
	}
	if d.TimePrecision == "" {
		d.TimePrecision = x.TimePrecision
	}
	if d.SourceKind == "" {
		d.SourceKind = x.SourceKind
	}
	normalizeActivity(&d.activityInput)
	if e := validateActivity(d.activityInput); e != "" {
		jsonErr(w, 400, e)
		return
	}
	old, _ = json.Marshal(x)
	in, _ := identityFrom(ctx)
	e = tx.QueryRow(ctx, `UPDATE mimo.activity_events SET title=$3,description=$4,category=$5,tags=$6,started_at=$7,ended_at=$8,event_type=$9,time_precision=$10,source_kind=$11,version=version+1,updated_at=now() WHERE owner_id=$1 AND id=$2::uuid RETURNING id::text,title,description,category,tags,started_at,ended_at,event_type,time_precision,source_kind,coalesce(source_message_id,''),status,version,created_at,updated_at`, a.owner(ctx), id(r), d.Title, d.Description, d.Category, d.Tags, d.StartedAt, d.EndedAt, d.EventType, d.TimePrecision, d.SourceKind).Scan(&x.ID, &x.Title, &x.Description, &x.Category, &x.Tags, &x.StartedAt, &x.EndedAt, &x.EventType, &x.TimePrecision, &x.SourceKind, &x.SourceMessageID, &x.Status, &x.Version, &x.CreatedAt, &x.UpdatedAt)
	if e != nil {
		internalErr(w, e)
		return
	}
	if _, e = tx.Exec(ctx, `INSERT INTO mimo.activity_revisions(activity_id,version,snapshot,changed_by) VALUES($1::uuid,$2,$3::jsonb,$4::uuid)`, id(r), x.Version, old, in.PrincipalID); e != nil {
		internalErr(w, e)
		return
	}
	resp := map[string]any{"activity": x}
	body, _ := json.Marshal(resp)
	if key != "" {
		_, e = tx.Exec(ctx, `INSERT INTO mimo.api_idempotency(owner_id,idempotency_key,request_hash,status_code,response_json) VALUES($1,$2,$3,200,$4::jsonb)`, a.owner(ctx), key, hash, body)
	}
	if e != nil {
		internalErr(w, e)
		return
	}
	if e = tx.Commit(ctx); e != nil {
		internalErr(w, e)
		return
	}
	jsonOK(w, resp)
}
func (a *App) activityVoid(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Version        int    `json:"version"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !decode(w, r, &d) {
		return
	}
	if d.Version < 1 {
		jsonErr(w, 400, "version is required")
		return
	}
	key := idemKey(r, d.IdempotencyKey)
	hash := requestHash(d)
	ctx := r.Context()
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		internalErr(w, e)
		return
	}
	defer tx.Rollback(ctx)
	if e = lockIdempotency(ctx, tx, a.owner(ctx), key); e != nil {
		internalErr(w, e)
		return
	}
	if body, status, ok, e := idemReplay(ctx, tx, a.owner(ctx), key, hash); ok {
		if e != nil {
			jsonErr(w, status, e.Error())
			return
		}
		_ = tx.Commit(ctx)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	tag, e := tx.Exec(ctx, `UPDATE mimo.activity_events SET status='voided',version=version+1,updated_at=now() WHERE owner_id=$1 AND id=$2::uuid AND version=$3 AND status='active'`, a.owner(ctx), id(r), d.Version)
	if e != nil {
		internalErr(w, e)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, 409, "activity not found or stale")
		return
	}
	resp := map[string]any{"voided": true, "id": id(r)}
	body, _ := json.Marshal(resp)
	if key != "" {
		_, e = tx.Exec(ctx, `INSERT INTO mimo.api_idempotency(owner_id,idempotency_key,request_hash,status_code,response_json) VALUES($1,$2,$3,200,$4::jsonb)`, a.owner(ctx), key, hash, body)
	}
	if e != nil {
		internalErr(w, e)
		return
	}
	if e = tx.Commit(ctx); e != nil {
		internalErr(w, e)
		return
	}
	jsonOK(w, resp)
}

type interval struct{ Start, End time.Time }

func mergeIntervals(in []interval) []interval {
	if len(in) == 0 {
		return nil
	}
	out := make([]interval, len(in))
	copy(out, in)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Start.Before(out[j-1].Start); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	merged := []interval{out[0]}
	for _, v := range out[1:] {
		last := &merged[len(merged)-1]
		if !v.Start.After(last.End) {
			if v.End.After(last.End) {
				last.End = v.End
			}
		} else {
			merged = append(merged, v)
		}
	}
	return merged
}
func coveredMinutes(intervals []interval, from, to time.Time) int {
	u := mergeIntervals(intervals)
	n := 0
	for _, v := range u {
		if v.End.After(from) && v.Start.Before(to) {
			s := v.Start
			if s.Before(from) {
				s = from
			}
			e := v.End
			if e.After(to) {
				e = to
			}
			n += int(e.Sub(s).Minutes())
		}
	}
	return n
}
func (a *App) timeline(w http.ResponseWriter, r *http.Request) {
	r.URL.RawQuery = r.URL.RawQuery
	a.activityList(w, r)
}
func (a *App) timelineCoverage(w http.ResponseWriter, r *http.Request) {
	from, to, e := coverageWindow(r)
	if e != "" {
		jsonErr(w, 400, e)
		return
	}
	rows, err := a.DB.Query(r.Context(), `SELECT started_at,coalesce(ended_at,least(now(),$3)) FROM mimo.activity_events WHERE owner_id=$1 AND status='active' AND started_at<$3 AND coalesce(ended_at,least(now(),$3))>$2`, a.owner(r.Context()), from, to)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	all := []interval{}
	for rows.Next() {
		var s, e time.Time
		if rows.Scan(&s, &e) == nil {
			all = append(all, interval{s, e})
		}
	}
	items := []map[string]any{}
	cursor := from
	for cursor.Before(to) {
		end := cursor.Add(time.Hour)
		if end.After(to) {
			end = to
		}
		covered := coveredMinutes(all, cursor, end)
		total := int(end.Sub(cursor).Minutes())
		items = append(items, map[string]any{"start": cursor, "end": end, "covered_minutes": covered, "unknown_minutes": total - covered})
		cursor = end
	}
	total := int(to.Sub(from).Minutes())
	covered := coveredMinutes(all, from, to)
	pct := 0.0
	if total > 0 {
		pct = float64(covered) * 100 / float64(total)
	}
	jsonOK(w, map[string]any{"from": from, "to": to, "covered_minutes": covered, "unknown_minutes": total - covered, "coverage_percent": pct, "buckets": items})
}
func coverageWindow(r *http.Request) (time.Time, time.Time, string) {
	loc := time.FixedZone("WIB", 7*60*60)
	from := time.Now().In(loc)
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	if v := r.URL.Query().Get("from"); v != "" {
		t, e := time.Parse(time.RFC3339, v)
		if e != nil {
			return time.Time{}, time.Time{}, "from must be RFC3339"
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, e := time.Parse(time.RFC3339, v)
		if e != nil {
			return time.Time{}, time.Time{}, "to must be RFC3339"
		}
		to = t
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, "to must be after from"
	}
	return from, to, ""
}
