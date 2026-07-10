package workflow

import (
	"embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed templates/*.json
var templateFS embed.FS

// Template describes a bundled, ready-to-use workflow definition that a user
// can instantiate with one command/click instead of building it node-by-node.
type Template struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
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
		out = append(out, Template{ID: id, Name: wf.Name, Description: wf.Description})
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
