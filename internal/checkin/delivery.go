package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var directWhatsAppJID = regexp.MustCompile(`^[^@[:space:]]+@s\.whatsapp\.net$`)

type RecipientBinding struct{ OwnerID, PrincipalID, WorkspaceID, AgentID, AccountID, RecipientJID string }

func ResolveRecipientBinding(ctx context.Context, db *pgxpool.Pool, job Job) (RecipientBinding, error) {
	if db == nil {
		return RecipientBinding{}, errors.New("delivery database unavailable")
	}
	var b RecipientBinding
	err := db.QueryRow(ctx, `SELECT b.owner_id,b.principal_id::text,b.workspace_id::text,b.agent_id,b.account_id,b.recipient_jid
FROM mimo.checkin_delivery_bindings b
JOIN mimo.principals p ON p.id=b.principal_id AND p.kind='agent' AND p.slug<>'nara' AND b.agent_id=p.slug
JOIN mimo.workspaces w ON w.id=b.workspace_id AND w.kind='personal'
JOIN mimo.personal_workspace_owners pwo ON pwo.owner_id=b.owner_id AND pwo.workspace_id=b.workspace_id
JOIN mimo.workspace_members hm ON hm.workspace_id=b.workspace_id AND hm.principal_id=pwo.human_principal_id AND hm.role='owner' AND hm.revoked_at IS NULL
JOIN mimo.workspace_members am ON am.workspace_id=b.workspace_id AND am.principal_id=b.principal_id AND am.revoked_at IS NULL
WHERE b.owner_id=$1 AND b.principal_id=$2::uuid AND b.workspace_id=$3::uuid AND b.channel='whatsapp' AND b.enabled AND b.verified`, job.OwnerID, job.PrincipalID, job.WorkspaceID).Scan(&b.OwnerID, &b.PrincipalID, &b.WorkspaceID, &b.AgentID, &b.AccountID, &b.RecipientJID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RecipientBinding{}, errors.New("verified recipient binding unavailable")
		}
		return RecipientBinding{}, errors.New("recipient binding lookup failed")
	}
	if !directWhatsAppJID.MatchString(b.RecipientJID) || strings.Contains(b.RecipientJID, "@g.us") || strings.Contains(b.RecipientJID, "@broadcast") || strings.Contains(b.RecipientJID, "@newsletter") {
		return RecipientBinding{}, errors.New("recipient is not a verified direct WhatsApp DM")
	}
	return b, nil
}

type CommandRunner interface {
	Run(context.Context, string, []string, int) ([]byte, error)
}
type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, name string, args []string, maxBytes int) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, int64(maxBytes)+1))
	errOut, _ := io.ReadAll(io.LimitReader(stderr, 4097))
	waitErr := cmd.Wait()
	if len(out) > maxBytes {
		return nil, errors.New("provider output exceeded limit")
	}
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		if len(errOut) > 0 {
			return nil, errors.New("provider command failed")
		}
		return nil, errors.New("provider command failed")
	}
	return out, nil
}

type OpenClawCLIDelivery struct {
	DB                *pgxpool.Pool
	Binary            string
	Runner            CommandRunner
	Timeout           time.Duration
	MaxOutputBytes    int
	Enabled           bool
	AllowedPrincipals map[string]bool
	Slots             chan struct{}
	RatePerMinute     int
	rateMu            sync.Mutex
	nextSend          time.Time
}

func NewOpenClawCLIDelivery(db *pgxpool.Pool, binary string, enabled bool, allowed []string, maxPerMinute int) *OpenClawCLIDelivery {
	if binary == "" {
		binary = "openclaw"
	}
	if maxPerMinute < 1 {
		maxPerMinute = 1
	}
	m := map[string]bool{}
	for _, v := range allowed {
		if x := strings.TrimSpace(v); x != "" {
			m[x] = true
		}
	}
	return &OpenClawCLIDelivery{DB: db, Binary: binary, Runner: OSCommandRunner{}, Timeout: 30 * time.Second, MaxOutputBytes: 64 << 10, Enabled: enabled, AllowedPrincipals: m, Slots: make(chan struct{}, maxPerMinute), RatePerMinute: maxPerMinute}
}
func (d *OpenClawCLIDelivery) Send(ctx context.Context, job Job) (DeliveryResult, error) {
	if !d.Enabled {
		return DeliveryResult{}, DeliveryError{Suppress: true}
	}
	if len(d.AllowedPrincipals) == 0 || !d.AllowedPrincipals[job.PrincipalID] {
		return DeliveryResult{}, DeliveryError{Suppress: true}
	}
	binding, err := ResolveRecipientBinding(ctx, d.DB, job)
	if err != nil {
		return DeliveryResult{}, DeliveryError{Suppress: true}
	}
	if d.Runner == nil {
		d.Runner = OSCommandRunner{}
	}
	if d.Timeout <= 0 {
		d.Timeout = 30 * time.Second
	}
	if d.MaxOutputBytes <= 0 {
		d.MaxOutputBytes = 64 << 10
	}
	select {
	case d.Slots <- struct{}{}:
		defer func() { <-d.Slots }()
	case <-ctx.Done():
		return DeliveryResult{Unknown: true}, ctx.Err()
	}
	if err := d.waitRate(ctx); err != nil {
		return DeliveryResult{Unknown: true}, DeliveryError{Unknown: true}
	}
	attemptID, attemptErr := d.startAttempt(ctx, job, binding)
	if attemptErr != nil {
		return DeliveryResult{}, attemptErr
	}
	msg := renderCheckinMessage(job)
	runCtx, cancel := context.WithTimeout(ctx, d.Timeout)
	defer cancel()
	raw, runErr := d.Runner.Run(runCtx, d.Binary, []string{"message", "send", "--channel", "whatsapp", "--account", binding.AccountID, "--target", binding.RecipientJID, "--message", msg, "--json"}, d.MaxOutputBytes)
	if runErr != nil {
		unknown := errors.Is(runCtx.Err(), context.DeadlineExceeded) || errors.Is(runErr, context.DeadlineExceeded)
		status := "confirmed_failed"
		if unknown {
			status = "unknown"
		}
		_ = d.finishAttempt(ctx, attemptID, status, "provider_timeout_or_unknown", "")
		if unknown {
			return DeliveryResult{AttemptID: attemptID, AccountID: binding.AccountID, Unknown: true}, DeliveryError{Unknown: true}
		}
		return DeliveryResult{AttemptID: attemptID, AccountID: binding.AccountID}, DeliveryError{Unknown: false}
	}
	providerID, accepted, parseErr := parseProviderAcceptance(raw)
	if parseErr != nil {
		_ = d.finishAttempt(ctx, attemptID, "unknown", "unrecognized_provider_response", "")
		return DeliveryResult{AttemptID: attemptID, AccountID: binding.AccountID, Unknown: true}, DeliveryError{Unknown: true}
	}
	status := "confirmed_failed"
	if accepted {
		status = "provider_accepted"
	}
	if err = d.finishAttempt(ctx, attemptID, status, "", providerID); err != nil {
		return DeliveryResult{AttemptID: attemptID, AccountID: binding.AccountID, ProviderMessageID: providerID, ProviderAccepted: accepted, Unknown: true}, DeliveryError{Unknown: true}
	}
	return DeliveryResult{AttemptID: attemptID, AccountID: binding.AccountID, ProviderMessageID: providerID, ProviderAccepted: accepted, Delivered: accepted}, nil
}

func (d *OpenClawCLIDelivery) waitRate(ctx context.Context) error {
	d.rateMu.Lock()
	wait := time.Duration(0)
	now := time.Now()
	if d.RatePerMinute > 0 && now.Before(d.nextSend) {
		wait = time.Until(d.nextSend)
	}
	if d.RatePerMinute > 0 {
		base := now
		if d.nextSend.After(base) {
			base = d.nextSend
		}
		d.nextSend = base.Add(time.Minute / time.Duration(d.RatePerMinute))
	}
	d.rateMu.Unlock()
	if wait <= 0 {
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *OpenClawCLIDelivery) startAttempt(ctx context.Context, job Job, b RecipientBinding) (string, error) {
	var id string
	err := d.DB.QueryRow(ctx, `INSERT INTO mimo.checkin_delivery_attempts(checkin_request_id,attempt_number,account_id,recipient_jid,status) VALUES($1::uuid,$2,$3,$4,'started') RETURNING id::text`, job.ID, job.Attempts, b.AccountID, b.RecipientJID).Scan(&id)
	return id, err
}
func (d *OpenClawCLIDelivery) finishAttempt(ctx context.Context, id, status, errorClass, providerID string) error {
	_, err := d.DB.Exec(ctx, `UPDATE mimo.checkin_delivery_attempts SET status=$2,error_class=nullif($3,''),provider_message_id=nullif($4,''),completed_at=now() WHERE id=$1::uuid`, id, status, errorClass, providerID)
	return err
}
func renderCheckinMessage(job Job) string {
	loc, err := time.LoadLocation(job.Timezone)
	if err != nil {
		loc = time.FixedZone("WIB", 7*60*60)
	}
	a := job.GapStart.In(loc)
	b := job.GapEnd.In(loc)
	return fmt.Sprintf("Bro, dari jam %s sampai %s belum ada catatan aktivitas. Tadi ngapain aja? Bisa recap singkat.", a.Format("15:04"), b.Format("15:04"))
}
func parseProviderAcceptance(raw []byte) (string, bool, error) {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return "", false, errors.New("invalid provider response")
	}
	id := ""
	for _, key := range []string{"provider_message_id", "message_id", "id"} {
		if x, ok := v[key].(string); ok {
			id = x
			break
		}
	}
	accepted := false
	if x, ok := v["accepted"].(bool); ok {
		accepted = x
	}
	if x, ok := v["status"].(string); ok {
		accepted = accepted || x == "accepted" || x == "queued" || x == "sent"
	}
	return id, accepted, nil
}
