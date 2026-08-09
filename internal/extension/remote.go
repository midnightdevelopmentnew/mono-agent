package extension

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"monoagent/internal/browser"
)

// RemoteSender dispatches Commands through another local process's Server
// (typically the daemon) over its HTTP relay, instead of holding a WebSocket
// connection to the extension itself. This lets a short-lived CLI invocation
// share an already-connected extension session rather than racing another
// process for the fixed extension port.
type RemoteSender struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewRemoteSender creates a sender that relays through the server at baseURL
// (e.g. "http://127.0.0.1:9222"). Callers only construct this after Probe
// confirms a server is already listening there, so its token file is
// already written; if the token can't be read, requests are sent without
// one and the server's handleRelay rejects them with a clear 401 rather
// than proceeding unauthenticated.
func NewRemoteSender(baseURL string) *RemoteSender {
	token, _ := loadToken()
	return &RemoteSender{baseURL: baseURL, token: token, client: &http.Client{Timeout: 90 * time.Second}}
}

// Probe reports whether a Server is actually listening and reachable at
// baseURL. Used to decide whether to relay through an existing process or
// fall back to starting a local server.
func Probe(baseURL string) bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(baseURL + "/monoagent/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (r *RemoteSender) SendCommand(cmd *Command, timeout time.Duration) (*Response, error) {
	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal command: %w", err)
	}
	url := fmt.Sprintf("%s/monoagent/relay?timeout_ms=%d", r.baseURL, timeout.Milliseconds())
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, r.token)
	httpResp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay request: %w", err)
	}
	defer httpResp.Body.Close()

	var resp Response
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("decode relay response: %w", err)
	}
	if !resp.Success {
		return &resp, fmt.Errorf("extension error: %s", resp.Error)
	}
	return &resp, nil
}

// IsConnected reports whether the remote server currently has a live
// extension connection.
func (r *RemoteSender) IsConnected() bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(r.baseURL + "/monoagent/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var status struct {
		Connected bool `json:"connected"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return false
	}
	return status.Connected
}

// CreateTab asks the remote server's extension to open a new tab.
func (r *RemoteSender) CreateTab(url string) (int, error) {
	resp, err := r.SendCommand(&Command{
		Type:   CmdCreateTab,
		Params: map[string]interface{}{"url": url},
	}, 30*time.Second)
	if err != nil {
		return 0, err
	}
	dataMap, _ := resp.Data.(map[string]interface{})
	if dataMap == nil {
		return 0, fmt.Errorf("create_tab response missing data")
	}
	tabIDRaw, ok := dataMap["tabId"]
	if !ok {
		return 0, fmt.Errorf("create_tab response missing tabId")
	}
	tabID, ok := tabIDRaw.(float64)
	if !ok {
		return 0, fmt.Errorf("tabId is not a number: %T", tabIDRaw)
	}
	return int(tabID), nil
}

// CloseTab asks the remote server's extension to close a tab.
func (r *RemoteSender) CloseTab(tabID int) error {
	_, err := r.SendCommand(&Command{
		Type:  CmdCloseTab,
		TabID: tabID,
	}, 30*time.Second)
	return err
}

// RemoteBridge adapts *RemoteSender to satisfy browser.ExtensionBridge, the
// same way ServerBridge adapts a locally-owned *Server.
type RemoteBridge struct {
	Sender *RemoteSender
}

var _ browser.ExtensionBridge = (*RemoteBridge)(nil)

func (b *RemoteBridge) IsConnected() bool {
	return b.Sender.IsConnected()
}

func (b *RemoteBridge) CreateTab(url string) (int, error) {
	return b.Sender.CreateTab(url)
}

func (b *RemoteBridge) NewPage(tabID int) browser.PageInterface {
	return NewExtensionPage(b.Sender, tabID)
}

func (b *RemoteBridge) CloseTab(tabID int) error {
	return b.Sender.CloseTab(tabID)
}
