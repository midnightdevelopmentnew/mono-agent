package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// Server is a WebSocket server that accepts a single connection from the
// Chrome Extension and dispatches commands/responses.
type Server struct {
	addr    string
	conn    *websocket.Conn
	connMu  sync.Mutex
	writeMu sync.Mutex // serializes writes; gorilla/websocket forbids concurrent writers
	pending map[string]chan *Response
	pendMu  sync.Mutex

	connected chan struct{} // closed when first connection arrives
	connOnce  sync.Once

	logger zerolog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	server *http.Server
}

var upgrader = websocket.Upgrader{
	CheckOrigin: checkOrigin,
}

// pongWait is how long the server waits for a pong (or any message) before
// giving up on a connection; pingInterval is how often it probes, kept well
// under pongWait so a healthy extension always has time to reply. Without
// this, a connection that dies without a clean TCP close (extension service
// worker suspended, laptop sleep, network drop) leaves s.conn non-nil
// forever: IsConnected() keeps reporting true while the extension is
// actually gone.
const (
	pongWait     = 30 * time.Second
	pingInterval = (pongWait * 9) / 10
)

// checkOrigin restricts the extension control channel to same-machine callers:
// native clients that send no Origin, the browser extension itself
// (chrome-extension:// / moz-extension://), and loopback origins. Arbitrary
// websites the user visits carry a public Origin and are rejected, so a page
// cannot open ws://127.0.0.1:9222/monoagent and impersonate the extension.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme == "chrome-extension" || u.Scheme == "moz-extension" {
		return true
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// loopbackAddr forces a missing or wildcard host to bind to loopback only, so
// the unauthenticated extension control channel is never reachable from other
// hosts on the network.
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// NewServer creates a new extension WebSocket server. Addr should be a
// host:port string such as ":9222".
func NewServer(addr string, logger zerolog.Logger) *Server {
	return &Server{
		addr:      addr,
		pending:   make(map[string]chan *Response),
		connected: make(chan struct{}),
		logger:    logger.With().Str("component", "extension-server").Logger(),
	}
}

// Start starts the HTTP/WebSocket server and blocks until the context is
// cancelled or the server shuts down.
func (s *Server) Start(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/monoagent", s.handleWS)
	mux.HandleFunc("/monoagent/health", s.handleHealth)
	mux.HandleFunc("/monoagent/relay", s.handleRelay)

	addr := loopbackAddr(s.addr)
	s.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-s.ctx.Done()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutCancel()
		_ = s.server.Shutdown(shutCtx)
	}()

	s.logger.Info().Str("addr", addr).Msg("extension server listening")
	err := s.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// StartAsync starts the server in a background goroutine.
func (s *Server) StartAsync(ctx context.Context) {
	go func() {
		if err := s.Start(ctx); err != nil {
			s.logger.Error().Err(err).Msg("extension server error")
		}
	}()
}

// WaitForConnection blocks until the Chrome extension connects or the timeout
// expires.
func (s *Server) WaitForConnection(timeout time.Duration) error {
	select {
	case <-s.connected:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("extension did not connect within %s", timeout)
	}
}

// IsConnected returns true if the Chrome extension is currently connected.
func (s *Server) IsConnected() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn != nil
}

// SendCommand sends a command to the Chrome extension and waits for the
// matching response. If the response indicates failure, an error is returned.
func (s *Server) SendCommand(cmd *Command, timeout time.Duration) (*Response, error) {
	if cmd.ID == "" {
		cmd.ID = uuid.New().String()
	}

	ch := make(chan *Response, 1)
	s.pendMu.Lock()
	s.pending[cmd.ID] = ch
	s.pendMu.Unlock()

	defer func() {
		s.pendMu.Lock()
		delete(s.pending, cmd.ID)
		s.pendMu.Unlock()
	}()

	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal command: %w", err)
	}

	s.connMu.Lock()
	conn := s.conn
	s.connMu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("no extension connected")
	}

	s.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, data)
	s.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write command: %w", err)
	}

	s.logger.Debug().Str("id", cmd.ID).Str("type", cmd.Type).Msg("command sent")

	select {
	case resp := <-ch:
		if !resp.Success {
			return resp, fmt.Errorf("extension error: %s", resp.Error)
		}
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("command %s timed out after %s", cmd.Type, timeout)
	}
}

// CreateTab asks the extension to open a new tab with the given URL and returns
// the tab ID.
func (s *Server) CreateTab(url string) (int, error) {
	resp, err := s.SendCommand(&Command{
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

// CloseTab asks the extension to close the tab with the given ID.
func (s *Server) CloseTab(tabID int) error {
	_, err := s.SendCommand(&Command{
		Type:  CmdCloseTab,
		TabID: tabID,
	}, 30*time.Second)
	return err
}

// Close gracefully shuts down the server and closes the WebSocket connection.
func (s *Server) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	s.connMu.Lock()
	conn := s.conn
	s.conn = nil
	s.connMu.Unlock()

	if conn != nil {
		return conn.Close()
	}
	return nil
}

// handleWS upgrades an incoming HTTP request to a WebSocket connection.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error().Err(err).Msg("websocket upgrade failed")
		return
	}

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Replace any existing connection.
	s.connMu.Lock()
	old := s.conn
	s.conn = conn
	s.connMu.Unlock()

	if old != nil {
		s.logger.Warn().Msg("replacing existing extension connection")
		_ = old.Close()
	}

	s.connOnce.Do(func() { close(s.connected) })
	s.logger.Info().Str("remote", conn.RemoteAddr().String()).Msg("extension connected")

	done := make(chan struct{})
	go s.pingLoop(conn, done)
	s.readLoop(conn)
	close(done)
}

// pingLoop periodically pings the connection so a dead peer is detected via
// the read deadline in handleWS/readLoop instead of hanging indefinitely.
func (s *Server) pingLoop(conn *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.writeMu.Lock()
			err := conn.WriteMessage(websocket.PingMessage, nil)
			s.writeMu.Unlock()
			if err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// handleHealth reports whether this server is alive and whether it currently
// holds a live extension connection. Other local monoagentcli processes probe
// this before deciding whether to relay through this server instead of
// starting their own (which would otherwise race for the same port and leave
// the extension connected to only one of them).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"connected": s.IsConnected()})
}

// handleRelay lets another local monoagentcli process dispatch a Command
// through this server's live extension connection and get the Response back,
// without needing to own the WebSocket connection itself. This is what makes
// it safe for a short-lived CLI invocation to share the daemon's already-
// connected extension instead of starting a second, competing server.
func (s *Server) handleRelay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var cmd Command
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, fmt.Sprintf("invalid command: %v", err), http.StatusBadRequest)
		return
	}
	timeout := defaultTimeout
	if ms := r.URL.Query().Get("timeout_ms"); ms != "" {
		if n, err := time.ParseDuration(ms + "ms"); err == nil {
			timeout = n
		}
	}
	resp, err := s.SendCommand(&cmd, timeout)
	w.Header().Set("Content-Type", "application/json")
	if resp == nil {
		resp = &Response{ID: cmd.ID}
	}
	if err != nil && resp.Error == "" {
		resp.Error = err.Error()
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// readLoop reads messages from the WebSocket and dispatches responses to
// waiting callers.
func (s *Server) readLoop(conn *websocket.Conn) {
	defer func() {
		s.connMu.Lock()
		if s.conn == conn {
			s.conn = nil
		}
		s.connMu.Unlock()
		_ = conn.Close()
		s.logger.Info().Msg("extension disconnected")
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				s.logger.Error().Err(err).Msg("websocket read error")
			}
			return
		}

		var resp Response
		if err := json.Unmarshal(msg, &resp); err != nil {
			s.logger.Error().Err(err).Str("raw", string(msg)).Msg("invalid response JSON")
			continue
		}

		// Debug: log raw message for eval responses to diagnose data flow.
		if resp.ID != "" {
			rawStr := string(msg)
			if len(rawStr) > 500 {
				rawStr = rawStr[:500] + "..."
			}
			s.logger.Debug().Str("raw", rawStr).Msg("raw response")
		}

		s.logger.Debug().Str("id", resp.ID).Bool("success", resp.Success).Str("error", resp.Error).Msg("response received")

		s.pendMu.Lock()
		ch, ok := s.pending[resp.ID]
		s.pendMu.Unlock()

		if ok {
			ch <- &resp
		} else {
			s.logger.Warn().Str("id", resp.ID).Msg("no pending request for response")
		}
	}
}
