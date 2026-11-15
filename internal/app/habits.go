package app

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type habitInput struct {
	Name     string  `json:"name"`
	Metric   string  `json:"metric"`
	Target   float64 `json:"target"`
	Period   string  `json:"period"`
	Category string  `json:"category"`
	Active   *bool   `json:"active"`
}
type habitRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Metric    string    `json:"metric"`
	Target    float64   `json:"target"`
	Period    string    `json:"period"`
	Category  *string   `json:"category,omitempty"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const habitSelect = `SELECT id::text,name,metric,target::float8,period,category,active,created_at,updated_at FROM mimo.habit_definitions WHERE owner_id=$1`

func validateHabit(d habitInput) string {
	if !requireText(d.Name, 300) {
		return "name is required"
	}
	if d.Target <= 0 {
		return "target must be > 0"
	}
	if d.Metric != "frequency" && d.Metric != "duration" && d.Metric != "completion" {
		return "metric must be frequency, duration, or completion"
	}
	if d.Period != "day" && d.Period != "week" {
		return "period must be day or week"
	}
	if len(d.Category) > 100 {
		return "category too long"
	}
	return ""
}
func (a *App) habitCreate(w http.ResponseWriter, r *http.Request) {
	var d habitInput
	if !decode(w, r, &d) {
		return
	}
	d.Name = strings.TrimSpace(d.Name)
	if e := validateHabit(d); e != "" {
		jsonErr(w, 400, e)
		return
	}
	if d.Active == nil {
		v := true
		d.Active = &v
	}
	var x habitRecord
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.habit_definitions(owner_id,principal_id,name,metric,target,period,category,active) VALUES($1,$2::uuid,$3,$4,$5,$6,nullif($7,''),$8) RETURNING id::text,name,metric,target::float8,period,category,active,created_at,updated_at`, a.owner(r.Context()), actor(r.Context(), a.C.OwnerID).PrincipalID, d.Name, d.Metric, d.Target, d.Period, d.Category, *d.Active).Scan(&x.ID, &x.Name, &x.Metric, &x.Target, &x.Period, &x.Category, &x.Active, &x.CreatedAt, &x.UpdatedAt)
	if err != nil {
		jsonErr(w, 409, "habit could not be saved")
		return
	}
	jsonCreated(w, x)
}
func (a *App) habitList(w http.ResponseWriter, r *http.Request) {
	rows, e := a.DB.Query(r.Context(), habitSelect+` ORDER BY active DESC,name`, a.owner(r.Context()))
	if e != nil {
		internalErr(w, e)
		return
	}
	defer rows.Close()
	out := []habitRecord{}
	for rows.Next() {
		var x habitRecord
		if e = rows.Scan(&x.ID, &x.Name, &x.Metric, &x.Target, &x.Period, &x.Category, &x.Active, &x.CreatedAt, &x.UpdatedAt); e != nil {
			internalErr(w, e)
			return
		}
		out = append(out, x)
	}
	if e = rows.Err(); e != nil {
		internalErr(w, e)
		return
	}
	jsonOK(w, map[string]any{"items": out})
}
func (a *App) habitPatch(w http.ResponseWriter, r *http.Request) {
	var d habitInput
	if !decode(w, r, &d) {
		return
	}
	if d.Name != "" {
		d.Name = strings.TrimSpace(d.Name)
	}
	if d.Metric == "" {
		d.Metric = "frequency"
	}
	if d.Period == "" {
		d.Period = "week"
	}
	if d.Target == 0 {
		d.Target = 1
	}
	if e := validateHabit(d); e != "" {
		jsonErr(w, 400, e)
		return
	}
	var x habitRecord
	active := true
	if d.Active != nil {
		active = *d.Active
	}
	err := a.DB.QueryRow(r.Context(), `UPDATE mimo.habit_definitions SET name=$3,metric=$4,target=$5,period=$6,category=nullif($7,''),active=$8,updated_at=now() WHERE owner_id=$1 AND id=$2::uuid RETURNING id::text,name,metric,target::float8,period,category,active,created_at,updated_at`, a.owner(r.Context()), id(r), d.Name, d.Metric, d.Target, d.Period, d.Category, active).Scan(&x.ID, &x.Name, &x.Metric, &x.Target, &x.Period, &x.Category, &x.Active, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, 404, "habit not found")
		return
	}
	if err != nil {
		jsonErr(w, 409, "habit could not be updated")
		return
	}
	jsonOK(w, x)
}
func habitBounds(r *http.Request, period string) (time.Time, time.Time, string) {
	loc := time.FixedZone("WIB", 7*60*60)
	now := time.Now().In(loc)
	if v := r.URL.Query().Get("date"); v != "" {
		d, e := time.ParseInLocation("2006-01-02", v, loc)
		if e != nil {
			return time.Time{}, time.Time{}, "date must be YYYY-MM-DD"
		}
		now = d
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if period == "week" {
		delta := (int(start.Weekday()) + 6) % 7
		start = start.AddDate(0, 0, -delta)
	}
	end := start.AddDate(0, 0, 1)
	if period == "week" {
		end = start.AddDate(0, 0, 7)
	}
	return start, end, ""
}
func (a *App) habitProgress(w http.ResponseWriter, r *http.Request) {
	rows, e := a.DB.Query(r.Context(), habitSelect+` AND active ORDER BY name`, a.owner(r.Context()))
	if e != nil {
		internalErr(w, e)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var h habitRecord
		if e = rows.Scan(&h.ID, &h.Name, &h.Metric, &h.Target, &h.Period, &h.Category, &h.Active, &h.CreatedAt, &h.UpdatedAt); e != nil {
			internalErr(w, e)
			return
		}
		from, to, msg := habitBounds(r, h.Period)
		if msg != "" {
			jsonErr(w, 400, msg)
			return
		}
		var value float64
		switch h.Metric {
		case "duration":
			e = a.DB.QueryRow(r.Context(), `SELECT coalesce(sum(extract(epoch FROM (coalesce(ended_at,least(now(),$4))-started_at))/60),0) FROM mimo.activity_events WHERE owner_id=$1 AND status='active' AND started_at<$4 AND coalesce(ended_at,least(now(),$4))>$2 AND (category=$3 OR lower(title)=lower($5))`, a.owner(r.Context()), from, nullableString(h.Category), to, h.Name).Scan(&value)
		case "frequency":
			e = a.DB.QueryRow(r.Context(), `SELECT count(*)::float8 FROM mimo.activity_events WHERE owner_id=$1 AND status='active' AND started_at>=$2 AND started_at<$3 AND (category=$4 OR lower(title)=lower($5))`, a.owner(r.Context()), from, to, nullableString(h.Category), h.Name).Scan(&value)
		default:
			e = a.DB.QueryRow(r.Context(), `SELECT coalesce(sum(value),0)::float8 FROM mimo.habit_observations WHERE owner_id=$1 AND habit_id=$2::uuid AND observed_on >= $3::date AND observed_on < $4::date`, a.owner(r.Context()), h.ID, from, to).Scan(&value)
		}
		if e != nil {
			internalErr(w, e)
			return
		}
		pct := value / h.Target * 100
		if pct > 100 {
			pct = 100
		}
		out = append(out, map[string]any{"habit": h, "period_start": from, "period_end": to, "value": value, "target": h.Target, "progress_percent": pct, "evidence": value > 0})
	}
	jsonOK(w, map[string]any{"items": out})
}
func nullableString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

type checkinSettings struct {
	Enabled              bool       `json:"enabled"`
	Strategy             string     `json:"strategy"`
	GapThresholdMinutes  int        `json:"gap_threshold_minutes"`
	IntervalMinutes      int        `json:"interval_minutes"`
	CooldownMinutes      int        `json:"cooldown_minutes"`
	MaxPerDay            int        `json:"max_per_day"`
	QuietStart           string     `json:"quiet_start"`
	QuietEnd             string     `json:"quiet_end"`
	Timezone             string     `json:"timezone"`
	PauseAfterUnanswered int        `json:"pause_after_unanswered"`
	SnoozedUntil         *time.Time `json:"snoozed_until,omitempty"`
}

func (a *App) checkinSettingsGet(w http.ResponseWriter, r *http.Request) {
	var x checkinSettings
	err := a.DB.QueryRow(r.Context(), `SELECT enabled,strategy,gap_threshold_minutes,interval_minutes,cooldown_minutes,max_per_day,to_char(quiet_start,'HH24:MI'),to_char(quiet_end,'HH24:MI'),timezone,pause_after_unanswered,snoozed_until FROM mimo.checkin_settings WHERE owner_id=$1`, a.owner(r.Context())).Scan(&x.Enabled, &x.Strategy, &x.GapThresholdMinutes, &x.IntervalMinutes, &x.CooldownMinutes, &x.MaxPerDay, &x.QuietStart, &x.QuietEnd, &x.Timezone, &x.PauseAfterUnanswered, &x.SnoozedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonOK(w, checkinSettings{Strategy: "adaptive", GapThresholdMinutes: 150, IntervalMinutes: 150, CooldownMinutes: 150, MaxPerDay: 4, QuietStart: "23:00", QuietEnd: "07:00", Timezone: "Asia/Jakarta", PauseAfterUnanswered: 2})
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, x)
}
func (a *App) checkinSettingsPatch(w http.ResponseWriter, r *http.Request) {
	var d checkinSettings
	if !decode(w, r, &d) {
		return
	}
	if d.Strategy == "" {
		d.Strategy = "adaptive"
	}
	if d.GapThresholdMinutes == 0 {
		d.GapThresholdMinutes = 150
	}
	if d.IntervalMinutes == 0 {
		d.IntervalMinutes = 150
	}
	if d.CooldownMinutes == 0 {
		d.CooldownMinutes = 150
	}
	if d.MaxPerDay == 0 {
		d.MaxPerDay = 4
	}
	if d.QuietStart == "" {
		d.QuietStart = "23:00"
	}
	if d.QuietEnd == "" {
		d.QuietEnd = "07:00"
	}
	if d.Timezone == "" {
		d.Timezone = "Asia/Jakarta"
	}
	if d.PauseAfterUnanswered == 0 {
		d.PauseAfterUnanswered = 2
	}
	if d.Strategy != "adaptive" || d.GapThresholdMinutes < 15 || d.GapThresholdMinutes > 1440 || d.IntervalMinutes < 15 || d.IntervalMinutes > 1440 || d.CooldownMinutes < 0 || d.MaxPerDay < 0 || d.MaxPerDay > 24 {
		jsonErr(w, 400, "invalid check-in settings")
		return
	}
	in := actor(r.Context(), a.C.OwnerID)
	var x checkinSettings
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.checkin_settings(owner_id,principal_id,workspace_id,enabled,strategy,gap_threshold_minutes,interval_minutes,cooldown_minutes,max_per_day,quiet_start,quiet_end,timezone,pause_after_unanswered,snoozed_until) VALUES($1,$2::uuid,nullif($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10::time,$11::time,$12,$13,$14) ON CONFLICT(owner_id) DO UPDATE SET principal_id=EXCLUDED.principal_id,workspace_id=EXCLUDED.workspace_id,enabled=EXCLUDED.enabled,strategy=EXCLUDED.strategy,gap_threshold_minutes=EXCLUDED.gap_threshold_minutes,interval_minutes=EXCLUDED.interval_minutes,cooldown_minutes=EXCLUDED.cooldown_minutes,max_per_day=EXCLUDED.max_per_day,quiet_start=EXCLUDED.quiet_start,quiet_end=EXCLUDED.quiet_end,timezone=EXCLUDED.timezone,pause_after_unanswered=EXCLUDED.pause_after_unanswered,snoozed_until=EXCLUDED.snoozed_until,updated_at=now() RETURNING enabled,strategy,gap_threshold_minutes,interval_minutes,cooldown_minutes,max_per_day,to_char(quiet_start,'HH24:MI'),to_char(quiet_end,'HH24:MI'),timezone,pause_after_unanswered,snoozed_until`, a.owner(r.Context()), in.PrincipalID, in.DefaultWorkspaceID, d.Enabled, d.Strategy, d.GapThresholdMinutes, d.IntervalMinutes, d.CooldownMinutes, d.MaxPerDay, d.QuietStart, d.QuietEnd, d.Timezone, d.PauseAfterUnanswered, d.SnoozedUntil).Scan(&x.Enabled, &x.Strategy, &x.GapThresholdMinutes, &x.IntervalMinutes, &x.CooldownMinutes, &x.MaxPerDay, &x.QuietStart, &x.QuietEnd, &x.Timezone, &x.PauseAfterUnanswered, &x.SnoozedUntil)
	if err != nil {
		jsonErr(w, 400, "settings could not be saved")
		return
	}
	jsonOK(w, x)
}
