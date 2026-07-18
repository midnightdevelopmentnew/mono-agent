package httpnodes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"monoagent/internal/workflow"
)

func runRequest(t *testing.T, config map[string]interface{}) []workflow.NodeOutput {
	t.Helper()
	n := &RequestNode{}
	out, err := n.Execute(context.Background(), workflow.NodeInput{}, config)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	return out
}

func handleItems(out []workflow.NodeOutput, handle string) []workflow.Item {
	for _, o := range out {
		if o.Handle == handle {
			return o.Items
		}
	}
	return nil
}

func TestRequestNode_GetSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("X-Test") != "hello" {
			t.Errorf("custom header not forwarded: %q", r.Header.Get("X-Test"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	out := runRequest(t, map[string]interface{}{
		"method":          "GET",
		"url":             srv.URL,
		"response_format": "json",
		"headers":         map[string]interface{}{"X-Test": "hello"},
	})
	main := handleItems(out, "main")
	if len(main) != 1 {
		t.Fatalf("expected 1 main item, got %d", len(main))
	}
	switch s := main[0].JSON["status"].(type) {
	case int:
		if s != 200 {
			t.Errorf("status = %d, want 200", s)
		}
	case float64:
		if int(s) != 200 {
			t.Errorf("status = %v, want 200", s)
		}
	default:
		t.Errorf("unexpected status type %T (%v)", s, s)
	}
}

func TestRequestNode_ErrorHandleOnBadHost(t *testing.T) {
	// An unroutable URL must route to the "error" handle, not crash or hang.
	out := runRequest(t, map[string]interface{}{
		"method":         "GET",
		"url":            "http://127.0.0.1:1/never",
		"timeout_seconds": 2.0,
	})
	if errs := handleItems(out, "error"); len(errs) == 0 {
		t.Fatalf("expected an error item for an unreachable host, got %v", out)
	}
	if main := handleItems(out, "main"); len(main) != 0 {
		t.Errorf("expected no main items on failure, got %d", len(main))
	}
}

func TestRequestNode_RequiresURL(t *testing.T) {
	n := &RequestNode{}
	_, err := n.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{"method": "GET"})
	if err == nil {
		t.Error("expected an error when url is missing")
	}
}
