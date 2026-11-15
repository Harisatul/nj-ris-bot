package app

import (
	"testing"
	"time"
)

func TestMergeIntervalsOverlapsAndTouches(t *testing.T) {
	loc := time.UTC
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, loc)
	in := []interval{
		{base.Add(30 * time.Minute), base.Add(120 * time.Minute)},
		{base, base.Add(time.Hour)},
		{base.Add(3 * time.Hour), base.Add(4 * time.Hour)},
		{base.Add(4 * time.Hour), base.Add(5 * time.Hour)},
	}
	out := mergeIntervals(in)
	if len(out) != 2 {
		t.Fatalf("merged intervals = %d, want 2: %#v", len(out), out)
	}
	if got := out[0].End.Sub(out[0].Start); got != 2*time.Hour {
		t.Fatalf("first duration = %v", got)
	}
	if got := out[1].End.Sub(out[1].Start); got != 2*time.Hour {
		t.Fatalf("second duration = %v", got)
	}
}

func TestCoveredMinutesClipsAndDoesNotDoubleCount(t *testing.T) {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	in := []interval{{base, base.Add(time.Hour)}, {base.Add(30 * time.Minute), base.Add(2 * time.Hour)}}
	if got := coveredMinutes(in, base.Add(15*time.Minute), base.Add(90*time.Minute)); got != 75 {
		t.Fatalf("covered = %d, want 75", got)
	}
}

func TestCoveredMinutesEmptyIsUnknown(t *testing.T) {
	base := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if got := coveredMinutes(nil, base, base.Add(time.Hour)); got != 0 {
		t.Fatalf("covered = %d, want 0", got)
	}
}
