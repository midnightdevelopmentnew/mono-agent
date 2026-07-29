package workflow

import (
	"embed"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

//go:embed templates/*.json
var templateFS embed.FS

// Template describes a bundled, ready-to-use workflow definition that a user
// can instantiate with one command/click instead of building it node-by-node.
type Template struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Inputs      []string `json:"inputs"` // trigger-data keys the template reads, e.g. ["prompts"]
}

// triggerInputPattern matches references to trigger data in node configs —
// {{ $json.prompt }} and the ($node["Trigger"].json.prompts) form the
// multi-node templates use.
var triggerInputPattern = regexp.MustCompile(`\$(?:json|node\[[^\]]*\])\.json\.([A-Za-z_][A-Za-z0-9_]*)|\$json\.([A-Za-z_][A-Za-z0-9_]*)`)

// templateInputs returns the trigger-data keys a template reads, sorted and
// deduplicated. These are exactly the keys that belong in
// `workflow templates run <id> --input '{...}'`, derived from the template
// itself so the docs can never drift from the definition.
func templateInputs(wf WorkflowFile) []string {
	seen := map[string]bool{}
	for _, n := range wf.Nodes {
		raw, err := json.Marshal(n.Config)
		if err != nil {
			continue
		}
		for _, m := range triggerInputPattern.FindAllStringSubmatch(string(raw), -1) {
			for _, group := range m[1:] {
				if group != "" {
					seen[group] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var templateFiles = loadTemplateFiles()

func loadTemplateFiles() map[string]WorkflowFile {
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return nil
	}
	out := make(map[string]WorkflowFile, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			continue
		}
		var wf WorkflowFile
		if err := json.Unmarshal(data, &wf); err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		out[id] = wf
	}
	return out
}

// ListTemplates returns metadata for all bundled workflow templates, sorted by name.
func ListTemplates() []Template {
	out := make([]Template, 0, len(templateFiles))
	for id, wf := range templateFiles {
		out = append(out, Template{
			ID:          id,
			Name:        wf.Name,
			Description: wf.Description,
			Inputs:      templateInputs(wf),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetTemplate returns the workflow definition for a bundled template, keyed by
// the ID returned from ListTemplates. Returns false if templateID is unknown.
// The returned WorkflowFile has no ID/ProfileID/timestamps set — callers
// instantiate it into a real workflow via their own save path (fresh node
// IDs, profile_id, etc.), matching how `workflow import` already turns a
// WorkflowFile into a saved Workflow.
func GetTemplate(templateID string) (WorkflowFile, bool) {
	wf, ok := templateFiles[templateID]
	return wf, ok
}
