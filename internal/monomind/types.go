// Package monomind is the mono-agent client for monomind's Agent Exec
// Protocol (doc/agent-exec-protocol.md in the monomind repo, v1/rev 4): the
// subprocess contract monoagentcli uses to delegate every AI interaction to
// a locally-installed monomind, which in turn drives the installed agent
// CLIs. mono-agent never learns agent-CLI wire formats — only this protocol.
package monomind

import "encoding/json"

// Protocol version implemented by this client (handshake-checked).
const ProtocolVersion = 1

// MinMonomindVersion is the minimum monomind release this client requires.
const MinMonomindVersion = "2.10.0"

// RequiredCapabilities are the handshake capabilities mono-agent relies on.
var RequiredCapabilities = []string{"agent-exec", "agent-scan"}

// Event is one NDJSON event from `monomind agent exec` (protocol §3.2).
// Exactly one Type* constant is set in Type; unknown event types are
// ignored per the spec's forward-compatibility rule.
type Event struct {
	V    int    `json:"v"`
	Type string `json:"type"`

	// start
	Runtime string `json:"runtime,omitempty"`
	Model   string `json:"model,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Resume  string `json:"resume,omitempty"`
	Pid     int    `json:"pid,omitempty"`

	// session
	SessionID string `json:"session_id,omitempty"`

	// assistant
	Text string `json:"text,omitempty"`

	// tool_call / tool_result
	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Result *struct {
		Text string `json:"text"`
	} `json:"result,omitempty"`

	// usage / result
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`

	// result
	Subtype    string `json:"subtype,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`

	// error
	Code       string `json:"code,omitempty"`
	ErrMessage string `json:"message,omitempty"`
	Fatal      bool   `json:"fatal,omitempty"`

	// done
	ExitCode int `json:"exit_code,omitempty"`
}

// Event types (protocol §3.2).
const (
	EventStart      = "start"
	EventSession    = "session"
	EventAssistant  = "assistant"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
	EventUsage      = "usage"
	EventResult     = "result"
	EventError      = "error"
	EventDone       = "done"
)

// Error codes (protocol §3.4). Unknown codes must be treated as non-fatal.
const (
	ErrAuth          = "auth"
	ErrQuota         = "quota"
	ErrMissingBinary = "missing-binary"
	ErrNoRunner      = "no-runner"
	ErrBudget        = "budget"
	ErrRunnerError   = "runner-error"
	ErrTimeout       = "timeout"
	ErrCancelled     = "cancelled"
	ErrBadFrame      = "bad-frame"
)

// Stop reasons (protocol §3.2 result.stop_reason).
const (
	StopEndTurn      = "end_turn"
	StopMaxTurns     = "max_turns"
	StopToolRoundCap = "tool_round_cap"
	StopCancelled    = "cancelled"
	StopTimeout      = "timeout"
)

// ProtocolError is a terminal `error` event from an exec turn.
type ProtocolError struct {
	Code    string
	Message string
	Fatal   bool
	// ExitCode is the process exit code that accompanied the error, when
	// the turn terminated without a success result.
	ExitCode int
}

func (e *ProtocolError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// ToolSpec is a tool definition for --tools-file (protocol §4.1): JSON
// Schema {type:"object", properties, required}.
type ToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Schema      map[string]interface{} `json:"schema,omitempty"`
}

// VersionInfo is the `monomind --version --json` handshake payload (§2).
type VersionInfo struct {
	V            int      `json:"v"`
	Version      string   `json:"version"`
	MinCaller    string   `json:"min_caller"`
	Capabilities []string `json:"capabilities"`
}

// HasCapability reports whether the handshake advertised the capability.
func (vi *VersionInfo) HasCapability(cap string) bool {
	for _, c := range vi.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// ScanEntry is one runtime detection result from `agent scan` (§6).
type ScanEntry struct {
	ID          string  `json:"id"`
	Installed   bool    `json:"installed"`
	Binary      *string `json:"binary"`
	Version     *string `json:"version"`
	InstallHint string  `json:"install_hint"`
}

// ScanResult is the `agent scan --json` payload (§6).
type ScanResult struct {
	V      int         `json:"v"`
	Agents []ScanEntry `json:"agents"`
}

// Installed returns only the installed runtimes, ordered as scanned.
func (s *ScanResult) Installed() []ScanEntry {
	out := make([]ScanEntry, 0, len(s.Agents))
	for _, a := range s.Agents {
		if a.Installed {
			out = append(out, a)
		}
	}
	return out
}

// Find returns a runtime by id from the scan result (nil when absent).
func (s *ScanResult) Find(id string) *ScanEntry {
	for i := range s.Agents {
		if s.Agents[i].ID == id {
			return &s.Agents[i]
		}
	}
	return nil
}
