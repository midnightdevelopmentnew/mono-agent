package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"monoagent/internal/action"
	"monoagent/internal/ai"
	aichat "monoagent/internal/ai/chat"
	"monoagent/internal/connections"
	"monoagent/internal/secrets"
	"monoagent/internal/storage"
	"monoagent/internal/workflow"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	_ "modernc.org/sqlite"
)

// App holds application state bound to the Wails runtime.
type App struct {
	ctx     context.Context
	db      *sql.DB
	dbPath  string
	logs    []LogEntry
	logsMu  sync.Mutex
	connMgr     *connections.Manager
	aiStore     *ai.AIStore
	chatService *aichat.ChatService
	wfStore     *workflow.HybridWorkflowStore

	runningMu   sync.Mutex
	runningCmds map[string]*exec.Cmd // workflowID → running subprocess

	chatCancels sync.Map // workflowID → *cancelHandle for in-flight AI chat streams

	activeProfileIDPtr atomic.Pointer[string] // currently selected profile; access via get/setActiveProfileID (read/written across Wails goroutines)
}

// cancelHandle wraps a stream's cancel func in a pointer so it has a comparable
// identity for sync.Map.CompareAndDelete.
type cancelHandle struct{ cancel context.CancelFunc }

// NewApp creates the App instance.
func NewApp() *App {
	home, _ := os.UserHomeDir()
	return &App{
		dbPath:      filepath.Join(home, ".monoagent", "monoagent.db"),
		logs:        make([]LogEntry, 0, 200),
		runningCmds: make(map[string]*exec.Cmd),
	}
}

// getActiveProfileID returns the currently selected profile id. Wails dispatches
// bound methods on independent goroutines, so this is read concurrently with
// SwitchProfile writes — the atomic makes that access race-free. Defaults to
// "default" before startup sets it.
func (a *App) getActiveProfileID() string {
	if p := a.activeProfileIDPtr.Load(); p != nil {
		return *p
	}
	return "default"
}

// setActiveProfileID atomically updates the selected profile id.
func (a *App) setActiveProfileID(id string) {
	a.activeProfileIDPtr.Store(&id)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	sdb, err := storage.NewDatabase(a.dbPath)
	if err != nil {
		runtime.LogErrorf(ctx, "DB open error: %v", err)
		return
	}
	if err := sdb.ApplyMigrations(); err != nil {
		runtime.LogErrorf(ctx, "DB migration error: %v", err)
	}
	db := sdb.DB
	a.db = db

	// Automatic, idempotent check-and-migrate: encrypt any connections rows
	// left over from before the secrets vault shipped. Cheap (a single COUNT
	// query) once everything is already encrypted, and self-healing if a
	// plaintext row is ever reintroduced.
	if _, _, err := connections.MigrateConnectionsToVault(ctx, db); err != nil {
		runtime.LogErrorf(ctx, "connections migration error: %v", err)
	}
	if _, _, err := secrets.MigrateFieldsToKV(ctx, db); err != nil {
		runtime.LogErrorf(ctx, "vault key-value migration error: %v", err)
	}
	if _, _, err := secrets.MigrateSessionsToVault(ctx, db); err != nil {
		runtime.LogErrorf(ctx, "sessions migration error: %v", err)
	}
	if _, _, err := ai.MigrateProvidersToVault(ctx, db); err != nil {
		runtime.LogErrorf(ctx, "ai providers migration error: %v", err)
	}

	// Ensure vault directory exists.
	vaultDir := filepath.Join(os.Getenv("HOME"), ".monoagent", "vault")
	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		runtime.LogErrorf(ctx, "vault dir error: %v", err)
	}

	// Initialize workflow hybrid store (file + SQLite) so workflows created
	// by both the GUI and the CLI are visible.
	wfDir := filepath.Join(os.Getenv("HOME"), ".monoagent", "workflows")
	fileStore, wfErr := workflow.NewWorkflowFileStore(wfDir)
	if wfErr != nil {
		fmt.Printf("workflow file store init error: %v\n", wfErr)
	} else {
		sqlStore := workflow.NewSQLiteWorkflowStore(db)
		a.wfStore = workflow.NewHybridWorkflowStore(fileStore, sqlStore)
	}

	// Initialize connections manager.
	mgr, err := connections.NewManager(a.db)
	if err != nil {
		fmt.Printf("connections manager init error: %v\n", err)
	} else {
		a.connMgr = mgr
	}

	// Initialize AI store.
	aiStore, aiErr := ai.NewAIStore(db)
	if aiErr != nil {
		fmt.Printf("ai store init error: %v\n", aiErr)
	} else {
		a.aiStore = aiStore
		cs := aichat.NewChatService(aiStore, db)
		// Feed the node type registry into canvas tools so AI knows what nodes are available.
		ntMap := a.GetWorkflowNodeTypes()
		var allTypes []aichat.NodeTypeInfo
		for _, v := range ntMap {
			// v is interface{} wrapping a typed slice; marshal+unmarshal to extract
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			var items []aichat.NodeTypeInfo
			if err := json.Unmarshal(b, &items); err != nil {
				continue
			}
			allTypes = append(allTypes, items...)
		}
		cs.SetCanvasNodeTypes(allTypes)
		a.chatService = cs
	}

	// Load the active profile from settings; default to 'default' if not set.
	var activeProfileID string
	_ = db.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&activeProfileID)
	if activeProfileID == "" {
		activeProfileID = "default"
	}
	a.setActiveProfileID(activeProfileID)

	a.emitLog("SYSTEM", "INFO", "Mono Agent UI connected to "+a.dbPath)

	go a.backgroundUpdateCheck()
}

func (a *App) shutdown(_ context.Context) {
	a.runningMu.Lock()
	for _, cmd := range a.runningCmds {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	a.runningMu.Unlock()
	if a.db != nil {
		_ = a.db.Close()
	}
}

// newUUID generates a random UUID v4 without external dependencies.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (a *App) emitLog(source, level, message string) {
	entry := LogEntry{
		Time:    time.Now().Format("15:04:05"),
		Source:  source,
		Level:   level,
		Message: message,
	}
	a.logsMu.Lock()
	a.logs = append(a.logs, entry)
	if len(a.logs) > 500 {
		a.logs = a.logs[len(a.logs)-500:]
	}
	a.logsMu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "log:entry", entry)
	}
}

// OpenURL opens a URL in the system default browser.
func (a *App) OpenURL(url string) {
	runtime.BrowserOpenURL(a.ctx, url)
}

// ─────────────────────────────────────────────────────────────────────────────
// Dashboard
// ─────────────────────────────────────────────────────────────────────────────

type DashboardStats struct {
	ActiveSessions int                    `json:"active_sessions"`
	TotalActions   int                    `json:"total_actions"`
	ActionsByState map[string]int         `json:"actions_by_state"`
	TotalPeople    int                    `json:"total_people"`
	TotalLists     int                    `json:"total_lists"`
	Sessions       []SessionSummary       `json:"sessions"`
	RecentActions  []ActionInfo           `json:"recent_actions"`
	DBPath         string                 `json:"db_path"`
}

type SessionSummary struct {
	Platform string `json:"platform"`
	Username string `json:"username"`
	Expiry   string `json:"expiry"`
	Active   bool   `json:"active"`
}

func (a *App) GetDashboardStats() DashboardStats {
	stats := DashboardStats{
		ActionsByState: make(map[string]int),
		DBPath:         a.dbPath,
	}
	if a.db == nil {
		return stats
	}

	_ = a.db.QueryRow("SELECT COUNT(*) FROM crawler_sessions WHERE expiry > datetime('now') AND profile_id = ?", a.getActiveProfileID()).Scan(&stats.ActiveSessions)
	_ = a.db.QueryRow("SELECT COUNT(*) FROM people WHERE profile_id = ?", a.getActiveProfileID()).Scan(&stats.TotalPeople)
	_ = a.db.QueryRow("SELECT COUNT(*) FROM social_lists WHERE profile_id = ?", a.getActiveProfileID()).Scan(&stats.TotalLists)

	rows, _ := a.db.Query("SELECT state, COUNT(*) FROM actions WHERE profile_id = ? GROUP BY state", a.getActiveProfileID())
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var state string
			var count int
			if rows.Scan(&state, &count) == nil {
				stats.ActionsByState[state] = count
				stats.TotalActions += count
			}
		}
	}

	sessionRows, _ := a.db.Query(`SELECT platform, username, expiry, (expiry > datetime('now')) as active
	                               FROM crawler_sessions WHERE profile_id = ? ORDER BY platform`, a.getActiveProfileID())
	if sessionRows != nil {
		defer sessionRows.Close()
		for sessionRows.Next() {
			var s SessionSummary
			var activeInt int
			if sessionRows.Scan(&s.Platform, &s.Username, &s.Expiry, &activeInt) == nil {
				s.Active = activeInt == 1
				stats.Sessions = append(stats.Sessions, s)
			}
		}
	}

	stats.RecentActions = a.GetActions("", "", 6)
	return stats
}

// Tags
// ─────────────────────────────────────────────────────────────────────────────

type TagInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// GetAllTags returns every tag in the active profile, ordered by name.
func (a *App) GetAllTags() []TagInfo {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.Query(`SELECT id, name, color FROM tags WHERE profile_id = ? ORDER BY name COLLATE NOCASE`, a.getActiveProfileID())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var tags []TagInfo
	for rows.Next() {
		var t TagInfo
		if rows.Scan(&t.ID, &t.Name, &t.Color) == nil {
			tags = append(tags, t)
		}
	}
	return tags
}

// GetPersonTags returns all tags attached to the given person.
func (a *App) GetPersonTags(personId string) []TagInfo {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.Query(`
		SELECT t.id, t.name, t.color
		FROM tags t
		JOIN people_tags pt ON pt.tag_id = t.id
		JOIN people p ON pt.person_id = p.id
		WHERE pt.person_id = ? AND t.profile_id = ? AND p.profile_id = ?
		ORDER BY t.name COLLATE NOCASE`, personId, a.getActiveProfileID(), a.getActiveProfileID())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var tags []TagInfo
	for rows.Next() {
		var t TagInfo
		if rows.Scan(&t.ID, &t.Name, &t.Color) == nil {
			tags = append(tags, t)
		}
	}
	return tags
}

// AddPersonTag creates a tag (if new) and links it to the person.
// Returns the tag that was added, or nil on error / if the person already has 10 tags.
func (a *App) AddPersonTag(personId, tagName, color string) *TagInfo {
	if a.db == nil {
		return nil
	}
	tagName = strings.TrimSpace(tagName)
	if tagName == "" {
		return nil
	}

	var personExists int
	if err := a.db.QueryRow(`SELECT 1 FROM people WHERE id = ? AND profile_id = ?`, personId, a.getActiveProfileID()).Scan(&personExists); err != nil {
		return nil
	}

	// Enforce max-10 limit.
	var count int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM people_tags WHERE person_id = ?`, personId).Scan(&count)
	if count >= 10 {
		return nil
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil
	}
	defer tx.Rollback()

	// Find or create the tag within the active profile.
	var tagId, tagColor string
	err = tx.QueryRow(`SELECT id, color FROM tags WHERE LOWER(name) = LOWER(?) AND profile_id = ?`, tagName, a.getActiveProfileID()).Scan(&tagId, &tagColor)
	if err != nil {
		// Create new tag scoped to the active profile.
		tagId = newUUID()
		if color == "" {
			color = "#00b4d8"
		}
		if _, err = tx.Exec(`INSERT INTO tags(id, name, color, profile_id) VALUES(?,?,?,?)`, tagId, tagName, color, a.getActiveProfileID()); err != nil {
			return nil
		}
		tagColor = color
	}

	// Link person ↔ tag (ignore if already linked).
	if _, err = tx.Exec(`INSERT OR IGNORE INTO people_tags(person_id, tag_id) VALUES(?,?)`, personId, tagId); err != nil {
		return nil
	}

	if err = tx.Commit(); err != nil {
		return nil
	}
	return &TagInfo{ID: tagId, Name: tagName, Color: tagColor}
}

// RemovePersonTag unlinks a tag from a person (does not delete the tag globally).
func (a *App) RemovePersonTag(personId, tagId string) {
	if a.db == nil {
		return
	}
	var exists int
	if err := a.db.QueryRow(`SELECT 1 FROM people WHERE id = ? AND profile_id = ?`, personId, a.getActiveProfileID()).Scan(&exists); err != nil {
		return
	}
	_, _ = a.db.Exec(`DELETE FROM people_tags WHERE person_id = ? AND tag_id = ?`, personId, tagId)
}

// GetPeopleTagsMap returns a map of personId → []TagInfo for a slice of person IDs.
// Used to bulk-load tags for the People list without N queries.
func (a *App) GetPeopleTagsMap(personIds []string) map[string][]TagInfo {
	if a.db == nil || len(personIds) == 0 {
		return nil
	}

	// Build IN clause.
	placeholders := make([]string, len(personIds))
	args := make([]interface{}, len(personIds))
	for i, id := range personIds {
		placeholders[i] = "?"
		args[i] = id
	}
	args = append(args, a.getActiveProfileID(), a.getActiveProfileID())
	query := fmt.Sprintf(`
		SELECT pt.person_id, t.id, t.name, t.color
		FROM people_tags pt
		JOIN tags t ON t.id = pt.tag_id
		JOIN people p ON pt.person_id = p.id
		WHERE pt.person_id IN (%s) AND t.profile_id = ? AND p.profile_id = ?
		ORDER BY t.name COLLATE NOCASE`, strings.Join(placeholders, ","))

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	result := make(map[string][]TagInfo)
	for rows.Next() {
		var pid string
		var t TagInfo
		if rows.Scan(&pid, &t.ID, &t.Name, &t.Color) == nil {
			result[pid] = append(result[pid], t)
		}
	}
	return result
}

// ─────────────────────────────────────────────────────────────────────────────
// Sessions
// ─────────────────────────────────────────────────────────────────────────────

type SessionInfo struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Platform string `json:"platform"`
	Expiry   string `json:"expiry"`
	AddedAt  string `json:"added_at"`
	Active   bool   `json:"active"`
}

func (a *App) GetSessions() []SessionInfo {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.Query(`SELECT id, username, platform, expiry, when_added,
	                                (expiry > datetime('now')) as active
	                          FROM crawler_sessions WHERE profile_id = ? ORDER BY platform, username`, a.getActiveProfileID())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var sessions []SessionInfo
	for rows.Next() {
		var s SessionInfo
		var activeInt int
		if rows.Scan(&s.ID, &s.Username, &s.Platform, &s.Expiry, &s.AddedAt, &activeInt) == nil {
			s.Active = activeInt == 1
			sessions = append(sessions, s)
		}
	}
	return sessions
}

// TestSession validates a browser session by checking if it exists and hasn't expired.
func (a *App) TestSession(id int) string {
	if a.db == nil {
		return "error: database not available"
	}
	var platform, vaultRef string
	var activeInt int
	err := a.db.QueryRow(
		`SELECT platform, COALESCE(vault_ref,''), (expiry > datetime('now')) as active
		 FROM crawler_sessions WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID(),
	).Scan(&platform, &vaultRef, &activeInt)
	if err != nil {
		return "error: session not found"
	}
	if activeInt != 1 {
		return "error: session expired"
	}
	if vaultRef == "" {
		return "error: no cookies stored"
	}
	return "ok"
}

func (a *App) DeleteSession(id int) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	var vaultRef string
	_ = a.db.QueryRow(
		`SELECT COALESCE(vault_ref,'') FROM crawler_sessions WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID(),
	).Scan(&vaultRef)
	res, err := a.db.Exec("DELETE FROM crawler_sessions WHERE id = ? AND profile_id = ?", id, a.getActiveProfileID())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("session %d not found", id)
	}
	if vaultRef != "" {
		if err := secrets.Delete(context.Background(), a.db, a.getActiveProfileID(), vaultRef); err != nil {
			runtime.LogErrorf(a.ctx, "deleting vault entry %s for session %d: %v", vaultRef, id, err)
		}
	}
	a.emitLog("SESSIONS", "WARN", fmt.Sprintf("Deleted session ID %d", id))
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Social Lists
// ─────────────────────────────────────────────────────────────────────────────

type SocialListInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ListType  string `json:"list_type"`
	ItemCount int    `json:"item_count"`
	CreatedAt string `json:"created_at"`
}

func (a *App) GetSocialLists() []SocialListInfo {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.Query(`SELECT id, name, COALESCE(list_type,''), item_count, COALESCE(created_at,'')
	                          FROM social_lists WHERE profile_id = ? ORDER BY created_at DESC`, a.getActiveProfileID())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var lists []SocialListInfo
	for rows.Next() {
		var l SocialListInfo
		if rows.Scan(&l.ID, &l.Name, &l.ListType, &l.ItemCount, &l.CreatedAt) == nil {
			lists = append(lists, l)
		}
	}
	return lists
}

// ─────────────────────────────────────────────────────────────────────────────
// Templates
// ─────────────────────────────────────────────────────────────────────────────

type TemplateInfo struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (a *App) GetTemplates() []TemplateInfo {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.Query("SELECT id, name, COALESCE(subject,''), body FROM templates ORDER BY name")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var templates []TemplateInfo
	for rows.Next() {
		var t TemplateInfo
		if rows.Scan(&t.ID, &t.Name, &t.Subject, &t.Body) == nil {
			templates = append(templates, t)
		}
	}
	return templates
}

// ─────────────────────────────────────────────────────────────────────────────
// Action Execution
// ─────────────────────────────────────────────────────────────────────────────

// findMonoAgentCLI locates the monoagentcli binary.
func findMonoAgentCLI() (string, error) {
	if p, err := exec.LookPath("monoagentcli"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, "go", "bin", "monoagentcli"),
		filepath.Join(home, ".local", "bin", "monoagentcli"),
		"/usr/local/bin/monoagentcli",
		"/opt/homebrew/bin/monoagentcli",
	}
	// Also check relative to executable (bundled app).
	if execDir, err := filepath.Abs(filepath.Dir(os.Args[0])); err == nil {
		candidates = append(candidates,
			filepath.Join(execDir, "monoagentcli"),
			filepath.Join(execDir, "..", "..", "..", "cmd", "monoagentcli", "monoagentcli"),
		)
	}
	for _, p := range candidates {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("monoagentcli binary not found — run `go install` or place the binary in PATH")
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// nodeTypeToPlatform derives the connection platform ID from a node type string.
// e.g. "service.google_sheets" → "google_sheets", "db.postgres" → "postgresql"
func nodeTypeToPlatform(nodeType string) string {
	overrides := map[string]string{
		"db.postgres": "postgresql",
		"db.mysql":    "mysql",
		"db.mongodb":  "mongodb",
		"db.redis":    "redis",
		"comm.email_send": "smtp",
		"comm.email_read": "imap",
	}
	if p, ok := overrides[nodeType]; ok {
		return p
	}
	parts := strings.SplitN(nodeType, ".", 2)
	if len(parts) == 2 {
		return parts[1] // "service.google_sheets" → "google_sheets"
	}
	return nodeType
}

// ExecuteAction runs a legacy action by spawning the CLI subprocess.
// stdout/stderr are streamed to the UI log panel in real time.
func (a *App) ExecuteAction(id string) error {
	if a.db == nil {
		return fmt.Errorf("database not initialized")
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}

	if _, err := a.db.Exec("UPDATE actions SET state = 'RUNNING', updated_at_ts = ? WHERE id = ? AND profile_id = ?",
		time.Now().Format(time.RFC3339), id, a.getActiveProfileID()); err != nil {
		a.emitLog("RUNNER", "WARN", fmt.Sprintf("failed to mark action %s RUNNING: %v", id, err))
	}

	cmd := exec.CommandContext(a.ctx, cliBin, "run", id, "--verbose")
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start action: %w", err)
	}
	a.emitLog("RUNNER", "INFO", fmt.Sprintf("Started action %s (pid %d)", id, cmd.Process.Pid))

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			a.emitLog("STDOUT", "INFO", scanner.Text())
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			a.emitLog("STDERR", "WARN", scanner.Text())
		}
	}()
	go func() {
		waitErr := cmd.Wait()
		if waitErr != nil {
			a.emitLog("RUNNER", "ERROR", fmt.Sprintf("Action %s failed: %v", id, waitErr))
			runtime.EventsEmit(a.ctx, "action:complete", map[string]interface{}{"action_id": id, "success": false})
		} else {
			a.emitLog("RUNNER", "INFO", fmt.Sprintf("Action %s completed", id))
			runtime.EventsEmit(a.ctx, "action:complete", map[string]interface{}{"action_id": id, "success": true})
		}
	}()
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Data export
// ─────────────────────────────────────────────────────────────────────────────

// ExportResult summarizes a completed export.
type ExportResult struct {
	OutputDir    string `json:"output_dir"`
	PeopleCount  int    `json:"people_count"`
	ActionsCount int    `json:"actions_count"`
	Cancelled    bool   `json:"cancelled,omitempty"`
}

// ExportData asks the user for a destination folder and exports all people and
// actions to JSON files there by invoking the CLI (`monoagentcli export`).
func (a *App) ExportData() (*ExportResult, error) {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return nil, err
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "Choose export folder"})
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return &ExportResult{Cancelled: true}, nil
	}
	cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "--json", "export", "--output-dir", dir)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("export failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("export failed: %w", err)
	}
	var res ExportResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("unexpected export output: %w", err)
	}
	a.emitLog("EXPORT", "INFO", fmt.Sprintf("Exported %d people and %d actions to %s", res.PeopleCount, res.ActionsCount, res.OutputDir))
	return &res, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Logs
// ─────────────────────────────────────────────────────────────────────────────

type LogEntry struct {
	Time    string `json:"time"`
	Source  string `json:"source"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (a *App) GetLogs() []LogEntry {
	a.logsMu.Lock()
	defer a.logsMu.Unlock()
	out := make([]LogEntry, len(a.logs))
	copy(out, a.logs)
	return out
}

func (a *App) ClearLogs() {
	a.logsMu.Lock()
	a.logs = make([]LogEntry, 0, 200)
	a.logsMu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Metadata
// ─────────────────────────────────────────────────────────────────────────────

// GetAvailableActionTypes returns action types grouped by platform, derived from
// the on-disk action definitions (data/actions/<platform>/<TYPE>.json plus any
// user-installed templates) — the same source `monoagentcli node list` uses — so
// the GUI can't drift from the platforms and actions that actually exist.
func (a *App) GetAvailableActionTypes() map[string][]string {
	out := map[string][]string{}
	entries, err := action.GetLoader().ListAvailable() // "platform/action_type"
	if err != nil {
		return out
	}
	for _, e := range entries {
		platform, actionType, ok := strings.Cut(e, "/")
		if !ok {
			continue
		}
		key := strings.ToUpper(platform)
		out[key] = append(out[key], actionType)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

func (a *App) GetDBPath() string {
	return a.dbPath
}

func (a *App) IsDBConnected() bool {
	if a.db == nil {
		return false
	}
	return a.db.Ping() == nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Profiles
// ─────────────────────────────────────────────────────────────────────────────

type ProfileInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsActive  bool   `json:"is_active"`
	CreatedAt string `json:"created_at"`
}

func (a *App) GetProfiles() ([]ProfileInfo, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	rows, err := a.db.Query(`SELECT id, name, created_at FROM profiles ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []ProfileInfo
	for rows.Next() {
		var p ProfileInfo
		if rows.Scan(&p.ID, &p.Name, &p.CreatedAt) == nil {
			p.IsActive = p.ID == a.getActiveProfileID()
			profiles = append(profiles, p)
		}
	}
	if profiles == nil {
		profiles = []ProfileInfo{}
	}
	return profiles, rows.Err()
}

func (a *App) CreateProfile(name string) (*ProfileInfo, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("profile name cannot be empty")
	}
	id := newUUID()
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_, err := a.db.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, ?)`, id, name, now)
	if err != nil {
		return nil, fmt.Errorf("create profile: %w", err)
	}
	return &ProfileInfo{ID: id, Name: name, IsActive: false, CreatedAt: now}, nil
}

func (a *App) SwitchProfile(id string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	// Verify the profile exists.
	var count int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE id = ?`, id).Scan(&count); err != nil || count == 0 {
		return fmt.Errorf("profile %q not found", id)
	}
	// Persist selection — does NOT kill any running workflow subprocesses.
	_, err := a.db.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('active_profile_id', ?)`, id)
	if err != nil {
		return fmt.Errorf("persist active profile: %w", err)
	}
	a.setActiveProfileID(id)
	a.emitLog("SYSTEM", "INFO", "Switched to profile: "+id)
	return nil
}

func (a *App) GetActiveProfile() (*ProfileInfo, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	var p ProfileInfo
	err := a.db.QueryRow(`SELECT id, name, created_at FROM profiles WHERE id = ?`, a.getActiveProfileID()).
		Scan(&p.ID, &p.Name, &p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("active profile not found: %w", err)
	}
	p.IsActive = true
	return &p, nil
}

// Human in Loop (HIL)
// ─────────────────────────────────────────────────────────────────────────────

// HILItem is the data structure returned to the frontend for each pending HIL item.
type HILItem struct {
	ID           string                 `json:"id"`
	ExecutionID  string                 `json:"execution_id"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name"`
	NodeID       string                 `json:"node_id"`
	NodeName     string                 `json:"node_name"`
	Status       string                 `json:"status"`
	ReadonlyData map[string]interface{} `json:"readonly_data"`
	EditableData map[string]interface{} `json:"editable_data"`
	NodeConfig   map[string]interface{} `json:"node_config"`
	CreatedAt    string                 `json:"created_at"`
}

// GetHILItems returns all pending Human-in-Loop items, including the workflow name.
func (a *App) GetHILItems() ([]HILItem, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	rows, err := a.db.Query(
		`SELECT h.id, h.execution_id, h.workflow_id, h.node_id, h.node_name, h.status,
		        h.readonly_data, h.editable_data, h.node_config, h.created_at,
		        COALESCE(w.name, '') AS workflow_name
		 FROM hil_pending h
		 LEFT JOIN workflows w ON w.id = h.workflow_id
		 WHERE h.status = 'pending' AND h.profile_id = ?
		 ORDER BY h.created_at ASC`,
		a.getActiveProfileID(),
	)
	if err != nil {
		return nil, fmt.Errorf("GetHILItems: %w", err)
	}
	defer rows.Close()

	var items []HILItem
	for rows.Next() {
		var it HILItem
		var roRaw, edRaw, cfgRaw string
		if err := rows.Scan(&it.ID, &it.ExecutionID, &it.WorkflowID, &it.NodeID, &it.NodeName,
			&it.Status, &roRaw, &edRaw, &cfgRaw, &it.CreatedAt, &it.WorkflowName); err != nil {
			continue
		}
		json.Unmarshal([]byte(roRaw), &it.ReadonlyData) //nolint:errcheck
		json.Unmarshal([]byte(edRaw), &it.EditableData) //nolint:errcheck
		json.Unmarshal([]byte(cfgRaw), &it.NodeConfig)  //nolint:errcheck
		items = append(items, it)
	}
	if items == nil {
		items = []HILItem{}
	}
	return items, nil
}

// ApproveHIL approves a pending HIL item with optional edited data (JSON string).
func (a *App) ApproveHIL(id string, editedDataJSON string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	if editedDataJSON == "" {
		editedDataJSON = "{}"
	}
	// Validate JSON.
	var check map[string]interface{}
	if err := json.Unmarshal([]byte(editedDataJSON), &check); err != nil {
		return fmt.Errorf("ApproveHIL: editedDataJSON is not valid JSON: %w", err)
	}
	res, err := a.db.Exec(
		`UPDATE hil_pending SET status='approved', edited_data=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending' AND profile_id = ?`,
		editedDataJSON, id, a.getActiveProfileID(),
	)
	if err != nil {
		return fmt.Errorf("ApproveHIL: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("ApproveHIL: item not found or already resolved")
	}
	a.emitLog("HIL", "INFO", fmt.Sprintf("HIL item %s approved", id))
	return nil
}

// RejectHIL rejects a pending HIL item, causing the workflow to error out.
func (a *App) RejectHIL(id string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	res, err := a.db.Exec(
		`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending' AND profile_id = ?`,
		id, a.getActiveProfileID(),
	)
	if err != nil {
		return fmt.Errorf("RejectHIL: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("RejectHIL: item not found or already resolved")
	}
	a.emitLog("HIL", "INFO", fmt.Sprintf("HIL item %s rejected", id))
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Credentials
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) ListCredentials() ([]CredentialSummary, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	rows, err := a.db.Query(`SELECT id, name, service_type, created_at, updated_at
	                          FROM credentials WHERE profile_id = ? ORDER BY name`, a.getActiveProfileID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var creds []CredentialSummary
	for rows.Next() {
		var c CredentialSummary
		if rows.Scan(&c.ID, &c.Name, &c.ServiceType, &c.CreatedAt, &c.UpdatedAt) == nil {
			creds = append(creds, c)
		}
	}
	if creds == nil {
		creds = []CredentialSummary{}
	}
	return creds, rows.Err()
}

func (a *App) SaveCredential(req SaveCredentialRequest) (*CredentialSummary, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	dataJSON := "{}"
	if len(req.Data) > 0 {
		if b, err := json.Marshal(req.Data); err == nil {
			dataJSON = string(b)
		}
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	var credID string

	if req.ID == "" {
		credID = uuid.New().String()
		_, err := a.db.Exec(`INSERT INTO credentials (id, name, service_type, encrypted_data, profile_id, created_at, updated_at)
		                      VALUES (?, ?, ?, ?, ?, ?, ?)`,
			credID, req.Name, req.ServiceType, dataJSON, a.getActiveProfileID(), now, now)
		if err != nil {
			return nil, fmt.Errorf("insert credential: %w", err)
		}
	} else {
		credID = req.ID
		res, err := a.db.Exec(`UPDATE credentials SET name = ?, service_type = ?, encrypted_data = ?, updated_at = ?
		                      WHERE id = ? AND profile_id = ?`,
			req.Name, req.ServiceType, dataJSON, now, credID, a.getActiveProfileID())
		if err != nil {
			return nil, fmt.Errorf("update credential: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, fmt.Errorf("credential %s not found", credID)
		}
	}

	row := a.db.QueryRow(`SELECT id, name, service_type, created_at, updated_at FROM credentials WHERE id = ?`, credID)
	var c CredentialSummary
	if err := row.Scan(&c.ID, &c.Name, &c.ServiceType, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func (a *App) DeleteCredential(id string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	res, err := a.db.Exec(`DELETE FROM credentials WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("credential %s not found", id)
	}
	return nil
}

