package checkin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Clock interface{ Now() time.Time }
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type FixedClock struct{ T time.Time }

func (c FixedClock) Now() time.Time { return c.T }

type Settings struct {
	OwnerID, PrincipalID, WorkspaceID, Timezone                           string
	Enabled                                                               bool
	Strategy                                                              string
	GapThresholdMinutes, CooldownMinutes, MaxPerDay, PauseAfterUnanswered int
	IntervalMinutes                                                       int
	QuietStart, QuietEnd                                                  string
	SnoozedUntil                                                          *time.Time
}
type Job struct {
	ID, OwnerID, PrincipalID, WorkspaceID     string
	Timezone                                  string
	GapStart, GapEnd, RequestedFor, ExpiresAt time.Time
	Attempts                                  int
}
type DeliveryResult struct {
	AttemptID         string
	AccountID         string
	ProviderMessageID string
	ProviderAccepted  bool
	Delivered         bool
	Unknown           bool
}
type DeliveryError struct{ Unknown, Suppress bool }

func (e DeliveryError) Error() string {
	if e.Unknown {
		return "delivery status unknown"
	}
	return "delivery failed"
}

type CheckinDelivery interface {
	Send(context.Context, Job) (DeliveryResult, error)
}

type FakeDelivery struct {
	mu     sync.Mutex
	Jobs   []Job
	Result DeliveryResult
	Err    error
}

func (f *FakeDelivery) Send(_ context.Context, job Job) (DeliveryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Jobs = append(f.Jobs, job)
	return f.Result, f.Err
}
func (f *FakeDelivery) Attempts() []Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Job, len(f.Jobs))
	copy(out, f.Jobs)
	return out
}

type Stats struct{ Evaluated, Eligible, Queued, Suppressed, Duplicates, RecoveredLeases int }
type Scheduler struct {
	DB         *pgxpool.Pool
	Clock      Clock
	InstanceID string
	Lease      time.Duration
}

func NewScheduler(db *pgxpool.Pool, clock Clock, instanceID string) *Scheduler {
	if clock == nil {
		clock = RealClock{}
	}
	if instanceID == "" {
		instanceID = "mimo-checkin"
	}
	return &Scheduler{DB: db, Clock: clock, InstanceID: instanceID, Lease: 2 * time.Minute}
}

func (s *Scheduler) Evaluate(ctx context.Context) (Stats, error) {
	if s.DB == nil {
		return Stats{}, errors.New("check-in scheduler database is nil")
	}
	now := s.Clock.Now().UTC()
	rows, err := s.DB.Query(ctx, `SELECT s.owner_id,s.principal_id::text,s.workspace_id::text,s.enabled,s.strategy,s.gap_threshold_minutes,s.cooldown_minutes,s.max_per_day,s.pause_after_unanswered,s.interval_minutes,to_char(s.quiet_start,'HH24:MI:SS'),to_char(s.quiet_end,'HH24:MI:SS'),s.timezone,s.snoozed_until
FROM mimo.checkin_settings s
JOIN mimo.personal_workspace_owners pwo ON pwo.owner_id=s.owner_id AND pwo.workspace_id=s.workspace_id
JOIN mimo.workspaces w ON w.id=s.workspace_id AND w.kind='personal'
JOIN mimo.principals agent ON agent.id=s.principal_id AND agent.kind='agent' AND agent.slug<>'nara'
JOIN mimo.principals human ON human.id=pwo.human_principal_id AND human.kind='human'
JOIN mimo.workspace_members hm ON hm.workspace_id=s.workspace_id AND hm.principal_id=human.id AND hm.role='owner' AND hm.revoked_at IS NULL
JOIN mimo.workspace_members am ON am.workspace_id=s.workspace_id AND am.principal_id=agent.id AND am.revoked_at IS NULL
JOIN mimo.permissions p ON p.workspace_id=s.workspace_id AND p.principal_id=agent.id AND p.permission='timeline:write'
WHERE s.enabled AND s.workspace_id IS NOT NULL`)
	if err != nil {
		return Stats{}, err
	}
	users := []Settings{}
	for rows.Next() {
		var u Settings
		if err = rows.Scan(&u.OwnerID, &u.PrincipalID, &u.WorkspaceID, &u.Enabled, &u.Strategy, &u.GapThresholdMinutes, &u.CooldownMinutes, &u.MaxPerDay, &u.PauseAfterUnanswered, &u.IntervalMinutes, &u.QuietStart, &u.QuietEnd, &u.Timezone, &u.SnoozedUntil); err != nil {
			rows.Close()
			return Stats{}, err
		}
		users = append(users, u)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return Stats{}, err
	}
	rows.Close()
	var stats Stats
	for _, u := range users {
		stats.Evaluated++
		delta, e := s.evaluateUser(ctx, u, now)
		if e != nil {
			return stats, e
		}
		stats.Eligible += delta.Eligible
		stats.Queued += delta.Queued
		stats.Suppressed += delta.Suppressed
		stats.Duplicates += delta.Duplicates
	}
	return stats, nil
}

func (s *Scheduler) evaluateUser(ctx context.Context, u Settings, now time.Time) (Stats, error) {
	var stats Stats
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return stats, fmt.Errorf("invalid check-in timezone")
	}
	local := now.In(loc)
	qs, qe := parseClock(u.QuietStart), parseClock(u.QuietEnd)
	if qs < 0 || qe < 0 {
		return stats, fmt.Errorf("invalid check-in quiet hours")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback(ctx)
	stats.Suppressed += s.suppressBackfilled(ctx, tx, u, now)
	if tag, e := tx.Exec(ctx, `UPDATE mimo.checkin_requests SET state='suppressed',closed_at=$2,updated_at=$2 WHERE owner_id=$1 AND state IN ('pending','queued') AND expires_at IS NOT NULL AND expires_at <= $2`, u.OwnerID, now); e == nil {
		stats.Suppressed += int(tag.RowsAffected())
	}
	_, _ = tx.Exec(ctx, `UPDATE mimo.checkin_requests SET state='pending',snoozed_until=NULL,updated_at=$2 WHERE owner_id=$1 AND state='snoozed' AND snoozed_until IS NOT NULL AND snoozed_until <= $2`, u.OwnerID, now)
	if !u.Enabled || u.Strategy != "adaptive" || inQuiet(local, qs, qe) || (u.SnoozedUntil != nil && u.SnoozedUntil.After(now)) {
		if err = tx.Commit(ctx); err != nil {
			return stats, err
		}
		return stats, nil
	}
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.AddDate(0, 0, 1)
	intervals, err := loadIntervals(ctx, tx, u.OwnerID, dayStart, now)
	if err != nil {
		return stats, err
	}
	gapStart := dayStart
	if len(intervals) > 0 {
		gapStart = merge(intervals)[len(merge(intervals))-1].End
	}
	if gapStart.After(now) {
		gapStart = now
	}
	activeGap := activeDuration(gapStart, now, loc, qs, qe)
	threshold := u.GapThresholdMinutes
	if threshold <= 0 {
		threshold = 150
	}
	if activeGap < time.Duration(threshold)*time.Minute {
		if err = tx.Commit(ctx); err != nil {
			return stats, err
		}
		return stats, nil
	}
	stats.Eligible++
	var last *time.Time
	var count, unanswered int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state<>'suppressed'),max(created_at),count(*) FILTER (WHERE state='sent' AND sent_at IS NOT NULL) FROM mimo.checkin_requests WHERE owner_id=$1 AND created_at >= $2 AND created_at < $3`, u.OwnerID, dayStart, dayEnd).Scan(&count, &last, &unanswered)
	if err != nil {
		return stats, err
	}
	if count >= u.MaxPerDay || (last != nil && now.Sub(*last) < time.Duration(u.CooldownMinutes)*time.Minute) || unanswered >= u.PauseAfterUnanswered {
		if err = tx.Commit(ctx); err != nil {
			return stats, err
		}
		return stats, nil
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mimo.checkin_requests WHERE owner_id=$1 AND state IN ('pending','queued','sending','sent','snoozed') AND activity_gap_start < $3 AND coalesce(activity_gap_end,$3) > $2)`, u.OwnerID, gapStart, now).Scan(&active)
	if err != nil {
		return stats, err
	}
	if active {
		stats.Duplicates++
		if err = tx.Commit(ctx); err != nil {
			return stats, err
		}
		return stats, nil
	}
	dedupe := fmt.Sprintf("%s:%s", local.Format("2006-01-02"), gapStart.UTC().Format(time.RFC3339Nano))
	expires := now.Add(15 * time.Minute)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO mimo.checkin_requests(owner_id,principal_id,workspace_id,requested_for,state,activity_gap_start,activity_gap_end,dedupe_key,expires_at) VALUES($1,$2::uuid,$3::uuid,$4,'pending',$5,$4,$6,$7) ON CONFLICT(owner_id,dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING RETURNING id::text`, u.OwnerID, u.PrincipalID, u.WorkspaceID, now, gapStart, dedupe, expires).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		stats.Duplicates++
		if err = tx.Commit(ctx); err != nil {
			return stats, err
		}
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	if _, err = tx.Exec(ctx, `UPDATE mimo.checkin_requests SET state='queued',queued_at=$2,updated_at=$2 WHERE id=$1::uuid`, id, now); err != nil {
		return stats, err
	}
	stats.Queued++
	if err = tx.Commit(ctx); err != nil {
		return stats, err
	}
	return stats, nil
}

func (s *Scheduler) suppressBackfilled(ctx context.Context, tx pgx.Tx, u Settings, now time.Time) int {
	rows, err := tx.Query(ctx, `SELECT id::text,activity_gap_start,activity_gap_end FROM mimo.checkin_requests WHERE owner_id=$1 AND state IN ('pending','queued') AND activity_gap_start IS NOT NULL AND activity_gap_end IS NOT NULL FOR UPDATE`, u.OwnerID)
	if err != nil {
		return 0
	}
	type reminderGap struct {
		ID       string
		From, To time.Time
	}
	gaps := []reminderGap{}
	for rows.Next() {
		var g reminderGap
		if rows.Scan(&g.ID, &g.From, &g.To) == nil {
			gaps = append(gaps, g)
		}
	}
	rows.Close()
	n := 0
	for _, g := range gaps {
		iv, e := loadIntervals(ctx, tx, u.OwnerID, g.From, g.To)
		if e == nil && covers(merge(iv), g.From, g.To) {
			if _, e = tx.Exec(ctx, `UPDATE mimo.checkin_requests SET state='suppressed',closed_at=$2,updated_at=$2 WHERE id=$1::uuid`, g.ID, now); e == nil {
				n++
			}
		}
	}
	return n
}

func loadIntervals(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, owner string, from, to time.Time) ([]Interval, error) {
	rows, err := q.Query(ctx, `SELECT greatest(started_at,$2),least(coalesce(ended_at,$3),$3) FROM mimo.activity_events WHERE owner_id=$1 AND status='active' AND started_at<$3 AND coalesce(ended_at,$3)>$2`, owner, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Interval{}
	for rows.Next() {
		var a, b time.Time
		if err = rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		if b.After(a) {
			out = append(out, Interval{a, b})
		}
	}
	return out, rows.Err()
}

type Interval struct{ Start, End time.Time }

func merge(in []Interval) []Interval {
	if len(in) == 0 {
		return nil
	}
	out := append([]Interval(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	m := []Interval{out[0]}
	for _, v := range out[1:] {
		last := &m[len(m)-1]
		if !v.Start.After(last.End) {
			if v.End.After(last.End) {
				last.End = v.End
			}
		} else {
			m = append(m, v)
		}
	}
	return m
}
func covers(in []Interval, from, to time.Time) bool {
	if !to.After(from) {
		return true
	}
	cursor := from
	for _, v := range in {
		if v.End.Before(cursor) || v.Start.After(cursor) {
			continue
		}
		if v.Start.After(cursor) {
			return false
		}
		if v.End.After(cursor) {
			cursor = v.End
		}
		if !cursor.Before(to) {
			return true
		}
	}
	return !cursor.Before(to)
}
func parseClock(v string) time.Duration {
	t, e := time.Parse("15:04:05", v)
	if e != nil {
		t, e = time.Parse("15:04", v)
	}
	if e != nil {
		return -1
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second
}
func inQuiet(t time.Time, start, end time.Duration) bool {
	if start < 0 || end < 0 || start == end {
		return false
	}
	m := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}
func activeDuration(from, to time.Time, loc *time.Location, start, end time.Duration) time.Duration {
	if !to.After(from) {
		return 0
	}
	quiet := time.Duration(0)
	first := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -1)
	last := time.Date(to.In(loc).Year(), to.In(loc).Month(), to.In(loc).Day(), 0, 0, 0, 0, loc)
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		var q0, q1 time.Time
		if start < end {
			q0 = d.Add(start)
			q1 = d.Add(end)
		} else if start > end {
			q0 = d.Add(start)
			q1 = d.AddDate(0, 0, 1).Add(end)
		} else {
			continue
		}
		a, b := q0, q1
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		if b.After(a) {
			quiet += b.Sub(a)
		}
	}
	total := to.Sub(from) - quiet
	if total < 0 {
		return 0
	}
	return total
}

func (s *Scheduler) ClaimOne(ctx context.Context) (*Job, error) {
	now := s.Clock.Now().UTC()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var j Job
	err = tx.QueryRow(ctx, `SELECT r.id::text,r.owner_id,r.principal_id::text,r.workspace_id::text,s.timezone,r.activity_gap_start,r.activity_gap_end,r.requested_for,coalesce(r.expires_at,$1),r.attempts FROM mimo.checkin_requests r JOIN mimo.checkin_settings s ON s.owner_id=r.owner_id WHERE ((r.state='queued' AND (r.lease_until IS NULL OR r.lease_until <= $1)) OR (r.state='sending' AND r.lease_until IS NOT NULL AND r.lease_until <= $1)) AND (r.expires_at IS NULL OR r.expires_at>$1) ORDER BY r.requested_for,r.id LIMIT 1 FOR UPDATE OF r SKIP LOCKED`, now).Scan(&j.ID, &j.OwnerID, &j.PrincipalID, &j.WorkspaceID, &j.Timezone, &j.GapStart, &j.GapEnd, &j.RequestedFor, &j.ExpiresAt, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lease := now.Add(s.Lease)
	if _, err = tx.Exec(ctx, `UPDATE mimo.checkin_requests SET state='sending',attempts=attempts+1,lease_owner=$2,lease_until=$3,sending_at=coalesce(sending_at,$4),updated_at=$4 WHERE id=$1::uuid`, j.ID, s.InstanceID, lease, now); err != nil {
		return nil, err
	}
	j.Attempts++
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &j, nil
}

func (s *Scheduler) ProcessOne(ctx context.Context, d CheckinDelivery) (bool, error) {
	if d == nil {
		return false, errors.New("check-in delivery adapter is nil")
	}
	job, err := s.ClaimOne(ctx)
	if err != nil || job == nil {
		return job != nil, err
	}
	result, sendErr := d.Send(ctx, *job)
	state := "failed"
	if sendErr != nil {
		if result.Unknown {
			state = "delivery_unknown"
		} else if de, ok := sendErr.(DeliveryError); ok && de.Suppress {
			state = "suppressed"
		} else if de, ok := sendErr.(*DeliveryError); ok && de.Suppress {
			state = "suppressed"
		} else if de, ok := sendErr.(DeliveryError); ok && de.Unknown {
			state = "delivery_unknown"
		} else if de, ok := sendErr.(*DeliveryError); ok && de.Unknown {
			state = "delivery_unknown"
		}
	} else if result.Unknown {
		state = "delivery_unknown"
	} else if result.Delivered {
		state = "sent"
	}
	now := s.Clock.Now().UTC()
	_, err = s.DB.Exec(ctx, `UPDATE mimo.checkin_requests SET state=$2,provider_message_id=nullif($3,''),sent_at=CASE WHEN $2='sent' THEN $4 ELSE sent_at END,last_error_at=CASE WHEN $2<>'sent' THEN $4 ELSE last_error_at END,error_message=CASE WHEN $2='failed' THEN 'delivery_failed' WHEN $2='delivery_unknown' THEN 'delivery_unknown' ELSE NULL END,lease_owner=NULL,lease_until=NULL,updated_at=$4 WHERE id=$1::uuid AND state='sending'`, job.ID, state, result.ProviderMessageID, now)
	return true, err
}

func Transition(ctx context.Context, db *pgxpool.Pool, id, from, to string, at time.Time, snoozeUntil *time.Time) error {
	if db == nil {
		return errors.New("check-in database is nil")
	}
	set := `state=$3,updated_at=$4`
	args := []any{id, from, to, at}
	if to == "snoozed" {
		set += `,snoozed_until=$5`
		args = append(args, snoozeUntil)
	}
	if to == "answered" {
		set += `,answered_at=$4,closed_at=$4`
	}
	if to == "skipped" || to == "expired" || to == "suppressed" || to == "failed" || to == "delivery_unknown" {
		set += `,closed_at=$4`
	}
	tag, err := db.Exec(ctx, `UPDATE mimo.checkin_requests SET `+set+` WHERE id=$1::uuid AND state=$2`, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("invalid or stale check-in transition")
	}
	return nil
}
