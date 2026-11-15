package checkin

import (
	"context"
	"testing"
	"time"
)

type recordingRunner struct {
	output []byte
	err    error
	name   string
	args   []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args []string, _ int) ([]byte, error) {
	r.name = name
	r.args = append([]string(nil), args...)
	return r.output, r.err
}

func TestParseProviderAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name, body, id string
		accepted       bool
	}{
		{name: "accepted", body: `{"accepted":true,"message_id":"wamid-1"}`, id: "wamid-1", accepted: true},
		{name: "queued", body: `{"status":"queued","id":"wamid-2"}`, id: "wamid-2", accepted: true},
		{name: "confirmed failure", body: `{"accepted":false}`, accepted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, accepted, err := parseProviderAcceptance([]byte(tc.body))
			if err != nil || id != tc.id || accepted != tc.accepted {
				t.Fatalf("id=%q accepted=%v err=%v", id, accepted, err)
			}
		})
	}
	if _, _, err := parseProviderAcceptance([]byte(`not-json`)); err == nil {
		t.Fatal("invalid provider output was accepted")
	}
}

func TestRenderCheckinMessageUsesJobTimezone(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	job := Job{GapStart: time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC), GapEnd: time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC), Timezone: loc.String()}
	got := renderCheckinMessage(job)
	want := "Bro, dari jam 12:00 sampai 15:00 belum ada catatan aktivitas. Tadi ngapain aja? Bisa recap singkat."
	if got != want {
		t.Fatalf("message=%q want=%q", got, want)
	}
}

func TestDirectWhatsAppJIDRejectsGroupsAndBroadcasts(t *testing.T) {
	if !directWhatsAppJID.MatchString("123@s.whatsapp.net") {
		t.Fatal("valid direct JID rejected")
	}
	for _, jid := range []string{"123@g.us", "status@broadcast", "foo@newsletter"} {
		if directWhatsAppJID.MatchString(jid) {
			t.Fatalf("non-DM JID accepted: %q", jid)
		}
	}
}
