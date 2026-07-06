package workflow

import "testing"

// TestCheckWorkflowProfile is a regression test: engine methods that load a
// workflow by bare ID (SaveWorkflow, ActivateWorkflow, DeactivateWorkflow,
// TriggerWorkflow, RetryExecution, GetWorkflow) must reject workflows that
// belong to a different profile than the engine's active one.
func TestCheckWorkflowProfile(t *testing.T) {
	e := &WorkflowEngine{profileID: "profile-a"}

	cases := []struct {
		name    string
		wf      *Workflow
		wantErr bool
	}{
		{"same profile", &Workflow{ID: "wf-1", ProfileID: "profile-a"}, false},
		{"different profile", &Workflow{ID: "wf-2", ProfileID: "profile-b"}, true},
		{"unset profile (legacy row)", &Workflow{ID: "wf-3", ProfileID: ""}, false},
	}

	for _, tc := range cases {
		err := e.checkWorkflowProfile(tc.wf)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
}
