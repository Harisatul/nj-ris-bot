package checkin

import (
	"testing"
	"time"
)

func TestParseResponseMultipleActivities(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	now := time.Date(2026, 1, 5, 15, 0, 0, 0, loc)
	parsed, err := ParseResponse("Jam 12-1 makan, 1-2 tidur, 2-3 coding", now, "Asia/Jakarta")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Kind != "activities" || len(parsed.Activities) != 3 {
		t.Fatalf("parsed=%+v", parsed)
	}
	if parsed.Activities[0].StartedAt.In(loc).Hour() != 12 || parsed.Activities[2].EndedAt.In(loc).Hour() != 15 {
		t.Fatalf("times=%+v", parsed.Activities)
	}
}

func TestParseResponseSkipAndSnooze(t *testing.T) {
	now := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	parsed, err := ParseResponse("Skip dulu.", now, "UTC")
	if err != nil || parsed.Kind != "skip" {
		t.Fatalf("skip=%+v err=%v", parsed, err)
	}
	parsed, err = ParseResponse("Ingatkan lagi 1 jam.", now, "UTC")
	if err != nil || parsed.Kind != "snooze" || parsed.SnoozeUntil == nil || !parsed.SnoozeUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("snooze=%+v err=%v", parsed, err)
	}
}

func TestParseResponseAmbiguousRequestsClarification(t *testing.T) {
	parsed, err := ParseResponse("Tadi meeting, tapi lupa jam pastinya.", time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC), "UTC")
	if err != nil || parsed.Kind != "clarification" || parsed.Clarification == "" {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
}
