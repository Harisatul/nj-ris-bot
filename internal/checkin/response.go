package checkin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ActivityDraft struct {
	Title, Category, SourceMessageID string
	StartedAt, EndedAt               time.Time
}
type ParsedResponse struct {
	Kind          string
	Activities    []ActivityDraft
	SnoozeUntil   *time.Time
	Clarification string
}

var activityRange = regexp.MustCompile(`(?i)([01]?\d|2[0-3])(?::([0-5]\d))?\s*(?:-|–|—|sampai)\s*([01]?\d|2[0-3])(?::([0-5]\d))?\s+([^,;]+)`)
var snoozeRange = regexp.MustCompile(`(?i)ingatkan\s+lagi\s+(\d+)\s*(menit|jam)`)

func ParseResponse(text string, now time.Time, timezone string) (ParsedResponse, error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return ParsedResponse{}, errors.New("invalid response timezone")
	}
	raw := strings.TrimSpace(text)
	lower := strings.ToLower(strings.Trim(raw, " .!?"))
	if lower == "skip" || lower == "skip dulu" || lower == "skip aja" || lower == "skip dulu ya" {
		return ParsedResponse{Kind: "skip"}, nil
	}
	if lower == "nanti" {
		until := now.Add(time.Hour)
		return ParsedResponse{Kind: "snooze", SnoozeUntil: &until}, nil
	}
	if m := snoozeRange.FindStringSubmatch(raw); len(m) > 0 {
		n, _ := strconv.Atoi(m[1])
		if m[2] == "jam" {
			n *= 60
		}
		until := now.Add(time.Duration(n) * time.Minute)
		return ParsedResponse{Kind: "snooze", SnoozeUntil: &until}, nil
	}
	matches := activityRange.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return ParsedResponse{Kind: "clarification", Clarification: "Jam mulai dan selesai kegiatannya kira-kira kapan?"}, nil
	}
	localNow := now.In(loc)
	base := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	out := make([]ActivityDraft, 0, len(matches))
	previousEndHour := -1
	for _, m := range matches {
		sh, _ := strconv.Atoi(m[1])
		sm := 0
		if m[2] != "" {
			sm, _ = strconv.Atoi(m[2])
		}
		eh, _ := strconv.Atoi(m[3])
		em := 0
		if m[4] != "" {
			em, _ = strconv.Atoi(m[4])
		}
		startHour := sh
		for previousEndHour >= 0 && startHour < previousEndHour {
			startHour += 12
			if startHour < previousEndHour {
				startHour += 24
			}
		}
		endHour := eh
		for endHour <= startHour {
			if endHour+12 > startHour {
				endHour += 12
			} else {
				endHour += 24
			}
		}
		start := base.Add(time.Duration(startHour)*time.Hour + time.Duration(sm)*time.Minute)
		end := base.Add(time.Duration(endHour)*time.Hour + time.Duration(em)*time.Minute)
		if end.After(now.In(loc)) {
			return ParsedResponse{Kind: "clarification", Clarification: "Waktunya terlihat masih di masa depan. Bisa konfirmasi jamnya?"}, nil
		}
		previousEndHour = endHour
		title := strings.TrimSpace(m[5])
		if title == "" {
			return ParsedResponse{Kind: "clarification", Clarification: "Kegiatannya apa pada rentang waktu itu?"}, nil
		}
		out = append(out, ActivityDraft{Title: title, Category: "other", StartedAt: start.UTC(), EndedAt: end.UTC()})
	}
	return ParsedResponse{Kind: "activities", Activities: out}, nil
}

type IncomingMessage struct {
	AccountID, SenderJID, MessageID, ReplyToProviderMessageID, Text string
	ReceivedAt                                                      time.Time
}
type ActivityBatchWriter interface {
	WriteActivityBatch(context.Context, pgx.Tx, string, string, string, []ActivityDraft) ([]string, error)
}
type PostgresActivityWriter struct{}

func (PostgresActivityWriter) WriteActivityBatch(ctx context.Context, tx pgx.Tx, owner, principal, workspace string, drafts []ActivityDraft) ([]string, error) {
	ids := make([]string, 0, len(drafts))
	for _, d := range drafts {
		if strings.TrimSpace(d.Title) == "" || d.EndedAt.Before(d.StartedAt) {
			return nil, errors.New("invalid activity draft")
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO mimo.activity_events(owner_id,principal_id,workspace_id,title,category,started_at,ended_at,event_type,time_precision,source_kind,source_message_id) VALUES($1,$2::uuid,$3::uuid,$4,$5,$6,$7,'checkin','exact','checkin',nullif($8,'')) RETURNING id::text`, owner, principal, workspace, d.Title, d.Category, d.StartedAt, d.EndedAt, d.SourceMessageID).Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type ResponseResult struct {
	Outcome, CheckinID, Clarification string
	ActivityIDs                       []string
	Duplicate                         bool
}
type ResponseProcessor struct {
	DB     *pgxpool.Pool
	Clock  Clock
	Writer ActivityBatchWriter
}

func (p *ResponseProcessor) Process(ctx context.Context, msg IncomingMessage) (ResponseResult, error) {
	if p.DB == nil {
		return ResponseResult{}, errors.New("response database unavailable")
	}
	if p.Clock == nil {
		p.Clock = RealClock{}
	}
	if msg.ReceivedAt.IsZero() {
		msg.ReceivedAt = p.Clock.Now()
	}
	if strings.TrimSpace(msg.MessageID) == "" || strings.TrimSpace(msg.SenderJID) == "" || strings.TrimSpace(msg.AccountID) == "" {
		return ResponseResult{}, errors.New("inbound message identity is incomplete")
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return ResponseResult{}, err
	}
	defer tx.Rollback(ctx)
	var owner, principal, workspace, timezone string
	err = tx.QueryRow(ctx, `SELECT b.owner_id,b.principal_id::text,b.workspace_id::text,s.timezone FROM mimo.checkin_delivery_bindings b JOIN mimo.checkin_settings s ON s.owner_id=b.owner_id AND s.principal_id=b.principal_id AND s.workspace_id=b.workspace_id JOIN mimo.principals p ON p.id=b.principal_id AND p.kind='agent' AND p.slug<>'nara' AND b.agent_id=p.slug JOIN mimo.workspaces w ON w.id=b.workspace_id AND w.kind='personal' JOIN mimo.personal_workspace_owners pwo ON pwo.owner_id=b.owner_id AND pwo.workspace_id=b.workspace_id JOIN mimo.workspace_members hm ON hm.workspace_id=b.workspace_id AND hm.principal_id=pwo.human_principal_id AND hm.role='owner' AND hm.revoked_at IS NULL JOIN mimo.workspace_members am ON am.workspace_id=b.workspace_id AND am.principal_id=b.principal_id AND am.revoked_at IS NULL WHERE b.account_id=$1 AND b.recipient_jid=$2 AND b.channel='whatsapp' AND b.enabled AND b.verified`, msg.AccountID, msg.SenderJID).Scan(&owner, &principal, &workspace, &timezone)
	if err != nil {
		return ResponseResult{}, errors.New("inbound sender is not a verified personal DM")
	}
	sum := sha256.Sum256([]byte(msg.Text))
	hash := hex.EncodeToString(sum[:])
	var responseID string
	err = tx.QueryRow(ctx, `INSERT INTO mimo.checkin_inbound_responses(owner_id,principal_id,workspace_id,channel,account_id,sender_jid,message_id,reply_to_provider_message_id,response_hash,outcome,received_at) VALUES($1,$2::uuid,$3::uuid,'whatsapp',$4,$5,$6,nullif($7,''),$8,'rejected',$9) ON CONFLICT(owner_id,message_id) DO NOTHING RETURNING id::text`, owner, principal, workspace, msg.AccountID, msg.SenderJID, msg.MessageID, msg.ReplyToProviderMessageID, hash, msg.ReceivedAt).Scan(&responseID)
	if errors.Is(err, pgx.ErrNoRows) {
		var outcome string
		if e := tx.QueryRow(ctx, `SELECT outcome FROM mimo.checkin_inbound_responses WHERE owner_id=$1 AND message_id=$2`, owner, msg.MessageID).Scan(&outcome); e != nil {
			return ResponseResult{}, e
		}
		if e := tx.Commit(ctx); e != nil {
			return ResponseResult{}, e
		}
		return ResponseResult{Outcome: outcome, Duplicate: true}, nil
	}
	if err != nil {
		return ResponseResult{}, err
	}
	var requestID, state string
	query := `SELECT id::text,state FROM mimo.checkin_requests WHERE owner_id=$1 AND principal_id=$2::uuid AND workspace_id=$3::uuid AND `
	args := []any{owner, principal, workspace}
	if msg.ReplyToProviderMessageID != "" {
		query += `provider_message_id=$4 AND state IN ('sent','snoozed')`
		args = append(args, msg.ReplyToProviderMessageID)
	} else {
		query += `state='sent'`
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return ResponseResult{}, err
	}
	matches := []struct{ ID, State string }{}
	for rows.Next() {
		var m struct{ ID, State string }
		if rows.Scan(&m.ID, &m.State) == nil {
			matches = append(matches, struct{ ID, State string }{m.ID, m.State})
		}
	}
	rows.Close()
	if len(matches) != 1 {
		outcome := "rejected"
		clarification := "Tidak menemukan satu check-in yang jelas. Bisa balas langsung ke pesan check-in?"
		if len(matches) > 1 {
			outcome = "clarification"
		}
		_, err = tx.Exec(ctx, `UPDATE mimo.checkin_inbound_responses SET outcome=$2 WHERE id=$1::uuid`, responseID, outcome)
		if err == nil {
			err = tx.Commit(ctx)
		}
		return ResponseResult{Outcome: outcome, Clarification: clarification}, err
	}
	requestID, state = matches[0].ID, matches[0].State
	parsed, err := ParseResponse(msg.Text, msg.ReceivedAt, timezone)
	if err != nil {
		return ResponseResult{}, err
	}
	if parsed.Kind == "clarification" {
		_, err = tx.Exec(ctx, `UPDATE mimo.checkin_inbound_responses SET checkin_request_id=$2::uuid,outcome='clarification' WHERE id=$1::uuid`, responseID, requestID)
		if err == nil {
			err = tx.Commit(ctx)
		}
		return ResponseResult{Outcome: "clarification", CheckinID: requestID, Clarification: parsed.Clarification}, err
	}
	if parsed.Kind == "skip" || parsed.Kind == "snooze" {
		target := "skipped"
		if parsed.Kind == "snooze" {
			target = "snoozed"
		}
		if _, err = tx.Exec(ctx, `UPDATE mimo.checkin_requests SET state=$2,snoozed_until=$3,closed_at=CASE WHEN $2='skipped' THEN $4 ELSE closed_at END,answered_at=CASE WHEN $2='skipped' THEN $4 ELSE answered_at END,updated_at=$4 WHERE id=$1::uuid AND state=$5`, requestID, target, parsed.SnoozeUntil, msg.ReceivedAt, state); err != nil {
			return ResponseResult{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE mimo.checkin_inbound_responses SET checkin_request_id=$2::uuid,outcome=$3 WHERE id=$1::uuid`, responseID, requestID, parsed.Kind)
		if err == nil {
			err = tx.Commit(ctx)
		}
		return ResponseResult{Outcome: parsed.Kind, CheckinID: requestID}, err
	}
	for i := range parsed.Activities {
		parsed.Activities[i].SourceMessageID = "inbound:" + msg.MessageID
	}
	writer := p.Writer
	if writer == nil {
		writer = PostgresActivityWriter{}
	}
	ids, err := writer.WriteActivityBatch(ctx, tx, owner, principal, workspace, parsed.Activities)
	if err != nil {
		return ResponseResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE mimo.checkin_requests SET state='answered',answered_at=$2,closed_at=$2,updated_at=$2 WHERE id=$1::uuid AND state=$3`, requestID, msg.ReceivedAt, state); err != nil {
		return ResponseResult{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE mimo.checkin_inbound_responses SET checkin_request_id=$2::uuid,outcome='activities' WHERE id=$1::uuid`, responseID, requestID)
	if err != nil {
		return ResponseResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ResponseResult{}, err
	}
	return ResponseResult{Outcome: "activities", CheckinID: requestID, ActivityIDs: ids}, nil
}
