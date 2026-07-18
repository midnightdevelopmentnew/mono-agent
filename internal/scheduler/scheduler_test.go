package scheduler

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestNextPeriod_BeforeWindowReturnsStart(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	start := now.Add(30 * time.Minute)
	end := now.Add(60 * time.Minute)
	got := NextPeriod(start, end, 15)
	if got == nil || !got.Equal(start) {
		t.Fatalf("NextPeriod before window = %v, want start %v", got, start)
	}
}

func TestNextPeriod_AfterWindowReturnsNil(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	start := now.Add(-60 * time.Minute)
	end := now.Add(-30 * time.Minute)
	if got := NextPeriod(start, end, 15); got != nil {
		t.Fatalf("NextPeriod after window = %v, want nil", got)
	}
}

func TestNextPeriod_WithinWindowAdvances(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	start := now.Add(-10 * time.Minute)
	end := now.Add(60 * time.Minute)
	got := NextPeriod(start, end, 15)
	if got == nil {
		t.Fatal("NextPeriod within window = nil, want a future time")
	}
	if got.Before(now) {
		t.Fatalf("NextPeriod within window = %v, is before now %v", got, now)
	}
}

func TestNextPeriod_NextIntervalPastEndReturnsNil(t *testing.T) {
	now := time.Now().Truncate(time.Minute)
	start := now.Add(-10 * time.Minute)
	end := now.Add(2 * time.Minute) // next 15-min boundary lands past end
	if got := NextPeriod(start, end, 15); got != nil {
		t.Fatalf("NextPeriod = %v, want nil when next interval exceeds end", got)
	}
}

func TestAddWorkflowJob_InvalidSpec(t *testing.T) {
	s := NewScheduler(zerolog.Nop())
	if _, err := s.AddWorkflowJob("not a cron spec", func() {}); err == nil {
		t.Error("expected an error for an invalid cron spec")
	}
}

func TestAddAndRemoveJob(t *testing.T) {
	s := NewScheduler(zerolog.Nop())
	id, err := s.AddWorkflowJob("@every 1h", func() {})
	if err != nil {
		t.Fatalf("AddWorkflowJob valid spec: %v", err)
	}
	s.RemoveJob(id) // must not panic
}
