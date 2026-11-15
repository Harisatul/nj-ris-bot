package checkin

import (
	"context"
	"testing"
	"time"
)

func TestQuietHoursCrossMidnight(t *testing.T) {
	start, end := parseClock("23:00"), parseClock("07:00")
	loc := time.FixedZone("WIB", 7*60*60)
	for _, tc := range []struct {
		hour  int
		quiet bool
	}{{23, true}, {2, true}, {6, true}, {7, false}, {12, false}} {
		got := inQuiet(time.Date(2026, 1, 2, tc.hour, 0, 0, 0, loc), start, end)
		if got != tc.quiet {
			t.Errorf("hour %d quiet=%v want %v", tc.hour, got, tc.quiet)
		}
	}
}

func TestActiveDurationExcludesQuietWindow(t *testing.T) {
	loc := time.FixedZone("WIB", 7*60*60)
	from := time.Date(2026, 1, 2, 6, 0, 0, 0, loc)
	to := time.Date(2026, 1, 2, 10, 0, 0, 0, loc)
	got := activeDuration(from, to, loc, parseClock("23:00"), parseClock("07:00"))
	if got != 3*time.Hour {
		t.Fatalf("active duration=%v want 3h", got)
	}
}

func TestMergeAndCovers(t *testing.T) {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	merged := merge([]Interval{{base.Add(30 * time.Minute), base.Add(2 * time.Hour)}, {base, base.Add(time.Hour)}})
	if len(merged) != 1 || !covers(merged, base.Add(15*time.Minute), base.Add(90*time.Minute)) {
		t.Fatalf("merged=%#v does not cover expected range", merged)
	}
	if covers(merged, base, base.Add(3*time.Hour)) {
		t.Fatal("partial coverage reported as complete")
	}
}

func TestFakeDeliveryRecordsOnlyJobMetadata(t *testing.T) {
	fake := &FakeDelivery{Result: DeliveryResult{Delivered: true, ProviderMessageID: "provider-1"}}
	job := Job{ID: "job-1", OwnerID: "owner-1", GapStart: time.Unix(1, 0).UTC(), GapEnd: time.Unix(2, 0).UTC()}
	result, err := fake.Send(context.Background(), job)
	if err != nil || !result.Delivered || len(fake.Attempts()) != 1 {
		t.Fatalf("fake delivery result=%+v err=%v attempts=%d", result, err, len(fake.Attempts()))
	}
}
