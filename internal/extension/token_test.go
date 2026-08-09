package extension

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// withTempHome points os.UserHomeDir (via $HOME) at a temp dir for the
// duration of the test, so token.go's file operations never touch the
// real ~/.monoagent state.
func withTempHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func TestGenerateAndLoadToken(t *testing.T) {
	withTempHome(t)

	generated, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if generated == "" {
		t.Fatal("generateToken returned an empty value")
	}

	loaded, err := loadToken()
	if err != nil {
		t.Fatalf("loadToken: %v", err)
	}
	if loaded != generated {
		t.Fatalf("loadToken returned a different value than generateToken wrote")
	}

	path, err := tokenPath()
	if err != nil {
		t.Fatalf("tokenPath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file mode = %o, want 0600", perm)
	}
}

func TestGenerateTokenIsRandomPerCall(t *testing.T) {
	withTempHome(t)

	first, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	second, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	if first == second {
		t.Fatal("generateToken produced the same value twice")
	}
}

func relayRequestBody(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(&Command{Type: CmdEval, Params: map[string]interface{}{"js": "1+1"}})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	return body
}

func TestHandleRelayRejectsMissingOrWrongValue(t *testing.T) {
	withTempHome(t)
	expected, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	other, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	s := &Server{}
	s.token = expected

	cases := []struct {
		name    string
		headerV string
		setHdr  bool
	}{
		{"missing", "", false},
		{"mismatched", other, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/monoagent/relay", bytes.NewReader(relayRequestBody(t)))
			if tc.setHdr {
				req.Header.Set(tokenHeader, tc.headerV)
			}
			w := httptest.NewRecorder()

			s.handleRelay(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestHandleRelayAcceptsMatchingValue(t *testing.T) {
	withTempHome(t)
	expected, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken: %v", err)
	}
	s := &Server{}
	s.token = expected
	s.pending = make(map[string]chan *Response)

	req := httptest.NewRequest(http.MethodPost, "/monoagent/relay", bytes.NewReader(relayRequestBody(t)))
	req.Header.Set(tokenHeader, expected)
	w := httptest.NewRecorder()

	s.handleRelay(w, req)

	// No extension is connected in this test, so the request should get past
	// the auth check and fail for that unrelated reason (never unauthorized).
	if w.Code == http.StatusUnauthorized {
		t.Fatalf("matching value was rejected as unauthorized")
	}
}
