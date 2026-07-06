package workflow

import (
	"testing"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
)

// fakeScheduler captures the spec passed to AddWorkflowJob without registering a real cron job.
type fakeScheduler struct {
	lastSpec string
}

func (f *fakeScheduler) AddWorkflowJob(spec string, fn func()) (cron.EntryID, error) {
	f.lastSpec = spec
	return 1, nil
}

func (f *fakeScheduler) RemoveJob(id cron.EntryID) {}

// TestActivateScheduleAppliesTimezone is a regression test: the "timezone" config
// field on trigger.schedule nodes was previously read nowhere, so a schedule
// configured for a specific IANA timezone silently ran in the server's local
// time zone instead.
func TestActivateScheduleAppliesTimezone(t *testing.T) {
	sched := &fakeScheduler{}
	tm := NewTriggerManager(nil, nil, sched, func(string, string, []Item) {}, zerolog.Nop())

	node := &WorkflowNode{
		ID: "n1",
		Config: map[string]interface{}{
			"cron":     "0 0 9 * * *",
			"timezone": "America/New_York",
		},
	}

	if err := tm.activateSchedule("wf1", node); err != nil {
		t.Fatalf("activateSchedule: %v", err)
	}

	want := "CRON_TZ=America/New_York 0 0 9 * * *"
	if sched.lastSpec != want {
		t.Errorf("spec = %q, want %q", sched.lastSpec, want)
	}
}

// TestActivateScheduleDefaultUTCOmitsPrefix verifies the default/UTC timezone
// does not get an unnecessary CRON_TZ prefix.
func TestActivateScheduleDefaultUTCOmitsPrefix(t *testing.T) {
	sched := &fakeScheduler{}
	tm := NewTriggerManager(nil, nil, sched, func(string, string, []Item) {}, zerolog.Nop())

	node := &WorkflowNode{
		ID: "n2",
		Config: map[string]interface{}{
			"cron":     "0 0 9 * * *",
			"timezone": "UTC",
		},
	}

	if err := tm.activateSchedule("wf1", node); err != nil {
		t.Fatalf("activateSchedule: %v", err)
	}

	if sched.lastSpec != "0 0 9 * * *" {
		t.Errorf("spec = %q, want unprefixed cron", sched.lastSpec)
	}
}
