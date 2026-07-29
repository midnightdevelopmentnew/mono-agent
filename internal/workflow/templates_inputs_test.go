package workflow

import "testing"

func TestTemplateInputs_FromBundledTemplates(t *testing.T) {
	cases := map[string][]string{
		"gemimg":     {"prompt"},
		"gemimgmany": {"prompts"},
	}
	for id, want := range cases {
		wf, ok := GetTemplate(id)
		if !ok {
			t.Fatalf("template %q not found", id)
		}
		got := templateInputs(wf)
		if len(got) != len(want) || (len(got) > 0 && got[0] != want[0]) {
			t.Errorf("%s: got inputs %v, want %v", id, got, want)
		}
	}
}

func TestTemplateInputs_NoTriggerData(t *testing.T) {
	wf, ok := GetTemplate("outlook_email_sync")
	if !ok {
		t.Skip("outlook template not bundled")
	}
	if got := templateInputs(wf); len(got) != 0 {
		t.Errorf("a scheduled template should read no trigger data, got %v", got)
	}
}
