package main

import (
	"bufio"
	"bytes"
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
	"syscall"
	"time"

	"github.com/google/uuid"
	"monoagent/internal/action"
	"monoagent/internal/ai"
	aichat "monoagent/internal/ai/chat"
	"monoagent/internal/connections"
	"monoagent/internal/storage"
	"monoagent/internal/noderegistry"
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
	if _, _, err := connections.EncryptPlaintextConnections(ctx, db); err != nil {
		runtime.LogErrorf(ctx, "connections migration error: %v", err)
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

// ─────────────────────────────────────────────────────────────────────────────
// Actions
// ─────────────────────────────────────────────────────────────────────────────

type ActionInfo struct {
	ID           string                 `json:"id"`
	Title        string                 `json:"title"`
	Type         string                 `json:"type"`
	State        string                 `json:"state"`
	Platform     string                 `json:"platform"`
	Keywords     string                 `json:"keywords"`
	ContentMsg   string                 `json:"content_message"`
	ReachedIndex int                    `json:"reached_index"`
	ExecCount    int                    `json:"exec_count"`
	TargetCount  int                    `json:"target_count"`
	CreatedAt    string                 `json:"created_at"`
	UpdatedAt    string                 `json:"updated_at"`
	Params       map[string]interface{} `json:"params,omitempty"`
}

func (a *App) GetActions(platform, state string, limit int) []ActionInfo {
	if a.db == nil {
		return nil
	}
	query := `SELECT id, title, type, state, target_platform,
	                 COALESCE(keywords,''), COALESCE(content_message,''),
	                 reached_index, action_execution_count,
	                 COALESCE(created_at_ts,''), COALESCE(updated_at_ts,'')
	          FROM actions WHERE profile_id = ?`
	var args []interface{}
	args = append(args, a.getActiveProfileID())

	if platform != "" && platform != "ALL" {
		query += " AND target_platform = ?"
		args = append(args, strings.ToUpper(platform))
	}
	if state != "" && state != "ALL" {
		query += " AND state = ?"
		args = append(args, strings.ToUpper(state))
	}
	query += " ORDER BY created_at_ts DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var actions []ActionInfo
	for rows.Next() {
		var act ActionInfo
		if rows.Scan(&act.ID, &act.Title, &act.Type, &act.State, &act.Platform,
			&act.Keywords, &act.ContentMsg, &act.ReachedIndex, &act.ExecCount,
			&act.CreatedAt, &act.UpdatedAt) == nil {
			_ = a.db.QueryRow("SELECT COUNT(*) FROM action_targets WHERE action_id = ?", act.ID).Scan(&act.TargetCount)
			actions = append(actions, act)
		}
	}
	return actions
}

func (a *App) GetAction(id string) *ActionInfo {
	if a.db == nil {
		return nil
	}
	row := a.db.QueryRow(`SELECT id, title, type, state, target_platform,
	                             COALESCE(keywords,''), COALESCE(content_message,''),
	                             reached_index, action_execution_count,
	                             COALESCE(created_at_ts,''), COALESCE(updated_at_ts,''),
	                             COALESCE(params,'{}')
	                      FROM actions WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID())
	var act ActionInfo
	var paramsJSON string
	if row.Scan(&act.ID, &act.Title, &act.Type, &act.State, &act.Platform,
		&act.Keywords, &act.ContentMsg, &act.ReachedIndex, &act.ExecCount,
		&act.CreatedAt, &act.UpdatedAt, &paramsJSON) != nil {
		return nil
	}
	if paramsJSON != "" && paramsJSON != "{}" {
		var p map[string]interface{}
		if json.Unmarshal([]byte(paramsJSON), &p) == nil {
			act.Params = p
		}
	}
	_ = a.db.QueryRow("SELECT COUNT(*) FROM action_targets WHERE action_id = ?", act.ID).Scan(&act.TargetCount)
	return &act
}

type CreateActionRequest struct {
	Title          string                 `json:"title"`
	Type           string                 `json:"type"`
	Platform       string                 `json:"platform"`
	Keywords       string                 `json:"keywords"`
	ContentMessage string                 `json:"content_message"`
	Params         map[string]interface{} `json:"params,omitempty"`
}

func (a *App) CreateAction(req CreateActionRequest) (*ActionInfo, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	id := newUUID()
	now := time.Now()
	paramsJSON := "{}"
	if len(req.Params) > 0 {
		if b, err := json.Marshal(req.Params); err == nil {
			paramsJSON = string(b)
		}
	}
	_, err := a.db.Exec(`INSERT INTO actions
	                      (id, created_at, title, type, state, target_platform, keywords, content_message, params, profile_id, created_at_ts, updated_at_ts)
	                      VALUES (?, ?, ?, ?, 'PENDING', ?, ?, ?, ?, ?, ?, ?)`,
		id, now.Unix(), req.Title, strings.ToUpper(req.Type), strings.ToUpper(req.Platform),
		req.Keywords, req.ContentMessage, paramsJSON, a.getActiveProfileID(), now.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	a.emitLog("ACTIONS", "INFO", fmt.Sprintf("Created action: %s [%s/%s]", req.Title, req.Platform, req.Type))
	return &ActionInfo{
		ID:       id,
		Title:    req.Title,
		Type:     strings.ToUpper(req.Type),
		State:    "PENDING",
		Platform: strings.ToUpper(req.Platform),
		Keywords: req.Keywords,
		Params:   req.Params,
	}, nil
}

func (a *App) UpdateActionState(id, state string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	res, err := a.db.Exec("UPDATE actions SET state = ?, updated_at_ts = ? WHERE id = ? AND profile_id = ?",
		strings.ToUpper(state), time.Now().Format(time.RFC3339), id, a.getActiveProfileID())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("action %s not found", id)
	}
	return nil
}

func (a *App) UpdateActionParams(id string, params map[string]interface{}) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	paramsJSON := "{}"
	if len(params) > 0 {
		if b, err := json.Marshal(params); err == nil {
			paramsJSON = string(b)
		}
	}
	res, err := a.db.Exec("UPDATE actions SET params = ?, updated_at_ts = ? WHERE id = ? AND profile_id = ?",
		paramsJSON, time.Now().Format(time.RFC3339), id, a.getActiveProfileID())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("action %s not found", id)
	}
	return nil
}

func (a *App) DeleteAction(id string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	res, err := a.db.Exec("DELETE FROM actions WHERE id = ? AND profile_id = ?", id, a.getActiveProfileID())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("action %s not found", id)
	}
	a.emitLog("ACTIONS", "WARN", "Deleted action: "+id)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Targets
// ─────────────────────────────────────────────────────────────────────────────

type TargetInfo struct {
	ID        string `json:"id"`
	ActionID  string `json:"action_id"`
	Platform  string `json:"platform"`
	Link      string `json:"link"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

func (a *App) GetActionTargets(actionID string) []TargetInfo {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.Query(`SELECT action_targets.id, action_id, platform, COALESCE(link,''), status, COALESCE(action_targets.created_at,'')
	                          FROM action_targets
	                          JOIN actions ON action_targets.action_id = actions.id
	                          WHERE action_id = ? AND actions.profile_id = ?
	                          ORDER BY action_targets.created_at DESC`, actionID, a.getActiveProfileID())
	if err != nil {
		return nil
	}
	defer rows.Close()
	var targets []TargetInfo
	for rows.Next() {
		var t TargetInfo
		if rows.Scan(&t.ID, &t.ActionID, &t.Platform, &t.Link, &t.Status, &t.CreatedAt) == nil {
			targets = append(targets, t)
		}
	}
	return targets
}

func (a *App) AddActionTarget(actionID, link, platform string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	var exists int
	if err := a.db.QueryRow(`SELECT 1 FROM actions WHERE id = ? AND profile_id = ?`, actionID, a.getActiveProfileID()).Scan(&exists); err != nil {
		return fmt.Errorf("action %s not found", actionID)
	}
	id := newUUID()
	_, err := a.db.Exec(`INSERT INTO action_targets (id, action_id, platform, link, status) VALUES (?, ?, ?, ?, 'PENDING')`,
		id, actionID, strings.ToUpper(platform), link)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
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
	var platform, cookiesJSON string
	var activeInt int
	err := a.db.QueryRow(
		`SELECT platform, cookies_json, (expiry > datetime('now')) as active
		 FROM crawler_sessions WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID(),
	).Scan(&platform, &cookiesJSON, &activeInt)
	if err != nil {
		return "error: session not found"
	}
	if activeInt != 1 {
		return "error: session expired"
	}
	if len(cookiesJSON) < 10 {
		return "error: no cookies stored"
	}
	return "ok"
}

func (a *App) DeleteSession(id int) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	res, err := a.db.Exec("DELETE FROM crawler_sessions WHERE id = ? AND profile_id = ?", id, a.getActiveProfileID())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("session %d not found", id)
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

// ─────────────────────────────────────────────────────────────────────────────
// Workflow types
// ─────────────────────────────────────────────────────────────────────────────

type WorkflowSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsActive    bool   `json:"is_active"`
	Version     int    `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type WorkflowNodeData struct {
	ID        string                 `json:"id"`
	NodeType  string                 `json:"node_type"`
	Name      string                 `json:"name"`
	Config    map[string]interface{} `json:"config"`
	PositionX float64                `json:"position_x"`
	PositionY float64                `json:"position_y"`
	Disabled  bool                   `json:"disabled"`
	Schema    *workflow.NodeSchema   `json:"schema,omitempty"`
}

type WorkflowConnectionData struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"source_node_id"`
	SourceHandle string `json:"source_handle"`
	TargetNodeID string `json:"target_node_id"`
	TargetHandle string `json:"target_handle"`
	Position     int    `json:"position"`
}

type WorkflowDetail struct {
	WorkflowSummary
	Nodes       []WorkflowNodeData       `json:"nodes"`
	Connections []WorkflowConnectionData `json:"connections"`
}

type SaveWorkflowRequest struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	IsActive    bool                     `json:"is_active"`
	Nodes       []WorkflowNodeData       `json:"nodes"`
	Connections []WorkflowConnectionData `json:"connections"`
}

type WorkflowExecutionSummary struct {
	ID           string `json:"id"`
	WorkflowID   string `json:"workflow_id"`
	WorkflowName string `json:"workflow_name"`
	Status       string `json:"status"`
	TriggerType  string `json:"trigger_type"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	Error        string `json:"error"`
	CreatedAt    string `json:"created_at"`
}

type CredentialSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ServiceType string `json:"service_type"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type SaveCredentialRequest struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	ServiceType string                 `json:"service_type"`
	Data        map[string]interface{} `json:"data"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Workflow CRUD
// ─────────────────────────────────────────────────────────────────────────────

// workflowToDetail converts a *workflow.Workflow into a *WorkflowDetail for the frontend.
func workflowToDetail(wf *workflow.Workflow) *WorkflowDetail {
	detail := &WorkflowDetail{
		WorkflowSummary: WorkflowSummary{
			ID:          wf.ID,
			Name:        wf.Name,
			Description: wf.Description,
			IsActive:    wf.IsActive,
			Version:     wf.Version,
			CreatedAt:   wf.CreatedAt.Format(time.RFC3339),
			UpdatedAt:   wf.UpdatedAt.Format(time.RFC3339),
		},
		Nodes:       []WorkflowNodeData{},
		Connections: []WorkflowConnectionData{},
	}
	for _, n := range wf.Nodes {
		detail.Nodes = append(detail.Nodes, WorkflowNodeData{
			ID:        n.ID,
			NodeType:  n.Type,
			Name:      n.Name,
			Config:    n.Config,
			PositionX: n.PositionX,
			PositionY: n.PositionY,
			Disabled:  n.Disabled,
			Schema:    n.Schema,
		})
	}
	for _, c := range wf.Connections {
		detail.Connections = append(detail.Connections, WorkflowConnectionData{
			ID:           c.ID,
			SourceNodeID: c.SourceNodeID,
			SourceHandle: c.SourceHandle,
			TargetNodeID: c.TargetNodeID,
			TargetHandle: c.TargetHandle,
			Position:     c.Position,
		})
	}
	return detail
}

// ListWorkflowTemplates returns metadata for all bundled, ready-to-use
// workflow templates (e.g. "Outlook Email Sync") shipped with the app.
func (a *App) ListWorkflowTemplates() []workflow.Template {
	return workflow.ListTemplates()
}

// CreateWorkflowFromTemplate instantiates a bundled template as a new,
// editable workflow for the active profile. Node IDs from the template are
// remapped to fresh UUIDs so multiple instantiations never collide, then
// saved via the same path as a normal SaveWorkflow call.
func (a *App) CreateWorkflowFromTemplate(templateID string) (*WorkflowSummary, error) {
	tmpl, ok := workflow.GetTemplate(templateID)
	if !ok {
		return nil, fmt.Errorf("unknown template %q", templateID)
	}

	idMap := make(map[string]string, len(tmpl.Nodes))
	for _, n := range tmpl.Nodes {
		idMap[n.ID] = uuid.New().String()
	}

	req := SaveWorkflowRequest{Name: tmpl.Name, Description: tmpl.Description, IsActive: false}
	for _, n := range tmpl.Nodes {
		config := n.Config
		if config == nil {
			config = map[string]interface{}{}
		}
		// people.sync_outlook_message scopes synced people/messages by its
		// own "profile_id" config field, independent of the workflow's
		// profile — default it to the active profile so the template works
		// correctly out of the box.
		if n.Type == "people.sync_outlook_message" {
			config["profile_id"] = a.getActiveProfileID()
		}
		req.Nodes = append(req.Nodes, WorkflowNodeData{
			ID:        idMap[n.ID],
			NodeType:  n.Type,
			Name:      n.Name,
			Config:    config,
			PositionX: n.Position.X,
			PositionY: n.Position.Y,
			Disabled:  n.Disabled,
		})
	}
	for _, c := range tmpl.Connections {
		req.Connections = append(req.Connections, WorkflowConnectionData{
			ID:           uuid.New().String(),
			SourceNodeID: idMap[c.Source],
			SourceHandle: c.SourceHandle,
			TargetNodeID: idMap[c.Target],
			TargetHandle: c.TargetHandle,
		})
	}

	return a.SaveWorkflow(req)
}

func (a *App) ListWorkflows() ([]WorkflowSummary, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	rows, err := a.db.Query(`SELECT id, name, COALESCE(description,''), is_active, version,
	                                 COALESCE(created_at,''), COALESCE(updated_at,'')
	                          FROM workflows
	                          WHERE profile_id = ?
	                          ORDER BY updated_at DESC`, a.getActiveProfileID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var summaries []WorkflowSummary
	for rows.Next() {
		var s WorkflowSummary
		var isActive int
		if rows.Scan(&s.ID, &s.Name, &s.Description, &isActive, &s.Version, &s.CreatedAt, &s.UpdatedAt) == nil {
			s.IsActive = isActive == 1
			summaries = append(summaries, s)
		}
	}
	if summaries == nil {
		summaries = []WorkflowSummary{}
	}
	return summaries, rows.Err()
}

func (a *App) GetWorkflow(id string) (*WorkflowDetail, error) {
	if a.wfStore == nil {
		return nil, fmt.Errorf("workflow store not available")
	}
	ctx := context.Background()
	wf, err := a.wfStore.GetWorkflow(ctx, id)
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, fmt.Errorf("workflow %s not found", id)
	}
	// Verify caller owns this workflow.
	if a.db != nil {
		var wfProfile string
		_ = a.db.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.getActiveProfileID() {
			return nil, fmt.Errorf("workflow %s not found", id)
		}
	}
	return workflowToDetail(wf), nil
}

func (a *App) SaveWorkflow(req SaveWorkflowRequest) (*WorkflowSummary, error) {
	if a.wfStore == nil {
		return nil, fmt.Errorf("workflow store not available")
	}
	if a.db != nil && req.ID != "" {
		var wfProfile string
		_ = a.db.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, req.ID).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.getActiveProfileID() {
			return nil, fmt.Errorf("workflow %s not found", req.ID)
		}
	}
	ctx := context.Background()
	wf := &workflow.Workflow{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		IsActive:    req.IsActive,
		ProfileID:   a.getActiveProfileID(),
	}
	for _, n := range req.Nodes {
		node := workflow.WorkflowNode{
			ID:        n.ID,
			Type:      n.NodeType,
			Name:      n.Name,
			PositionX: n.PositionX,
			PositionY: n.PositionY,
			Disabled:  n.Disabled,
			Config:    n.Config,
			Schema:    n.Schema,
		}
		if node.Schema == nil {
			schema, _ := workflow.LoadDefaultSchema(node.Type)
			node.Schema = schema
		}
		wf.Nodes = append(wf.Nodes, node)
	}
	for _, c := range req.Connections {
		wf.Connections = append(wf.Connections, workflow.WorkflowConnection{
			ID:           c.ID,
			SourceNodeID: c.SourceNodeID,
			SourceHandle: c.SourceHandle,
			TargetNodeID: c.TargetNodeID,
			TargetHandle: c.TargetHandle,
			Position:     c.Position,
		})
	}
	if err := a.wfStore.SaveWorkflow(ctx, wf); err != nil {
		return nil, err
	}
	// Tag the workflow with the active profile. The store doesn't know about profiles,
	// so we set it with a follow-up UPDATE.
	if a.db != nil {
		_, _ = a.db.Exec(`UPDATE workflows SET profile_id = ? WHERE id = ?`, a.getActiveProfileID(), wf.ID)
	}
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Saved workflow: %s [%s]", wf.Name, wf.ID))
	return &WorkflowSummary{
		ID:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		IsActive:    wf.IsActive,
		Version:     wf.Version,
		CreatedAt:   wf.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   wf.UpdatedAt.Format(time.RFC3339),
	}, nil
}

func (a *App) DeleteWorkflow(id string) error {
	if a.wfStore == nil {
		return fmt.Errorf("workflow store not available")
	}
	if a.db != nil {
		var wfProfile string
		_ = a.db.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.getActiveProfileID() {
			return fmt.Errorf("workflow %s not found", id)
		}
	}
	err := a.wfStore.DeleteWorkflow(context.Background(), id)
	if err == nil {
		a.emitLog("WORKFLOW", "WARN", "Deleted workflow: "+id)
	}
	return err
}

func (a *App) SetWorkflowActive(id string, active bool) error {
	if a.wfStore == nil {
		return fmt.Errorf("workflow store not available")
	}
	ctx := context.Background()
	if a.db != nil {
		var wfProfile string
		_ = a.db.QueryRow(`SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.getActiveProfileID() {
			return fmt.Errorf("workflow %s not found", id)
		}
	}
	wf, err := a.wfStore.GetWorkflow(ctx, id)
	if err != nil || wf == nil {
		return fmt.Errorf("workflow %s not found", id)
	}
	wf.IsActive = active
	return a.wfStore.SaveWorkflow(ctx, wf)
}

// ─────────────────────────────────────────────────────────────────────────────
// Workflow execution (subprocess)
// ─────────────────────────────────────────────────────────────────────────────

// RunWorkflow spawns `monoagentcli workflow run <id>` as a subprocess.
// Stdout/stderr stream to the UI. The subprocess can be killed via CancelWorkflow.
func (a *App) RunWorkflow(id string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}

	// Ensure workflow is active so the engine doesn't reject it.
	if a.db != nil {
		_, _ = a.db.Exec("UPDATE workflows SET is_active = 1 WHERE id = ? AND profile_id = ?", id, a.getActiveProfileID())
	}

	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Starting workflow %s", id))

	cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.getActiveProfileID(), "workflow", "run", id)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start workflow: %w", err)
	}
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Workflow %s started (pid %d)", id, cmd.Process.Pid))

	a.runningMu.Lock()
	a.runningCmds[id] = cmd
	a.runningMu.Unlock()

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			a.emitLog("WORKFLOW", "INFO", line)
			// Detect execution ID from CLI output and notify the frontend
			if strings.HasPrefix(line, "Execution started: ") {
				execID := strings.TrimPrefix(line, "Execution started: ")
				runtime.EventsEmit(a.ctx, "workflow:exec-started", map[string]interface{}{
					"workflow_id":  id,
					"execution_id": strings.TrimSpace(execID),
				})
			}
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			a.emitLog("WORKFLOW", "INFO", scanner.Text())
		}
	}()
	go func() {
		defer func() {
			a.runningMu.Lock()
			delete(a.runningCmds, id)
			a.runningMu.Unlock()
		}()
		waitErr := cmd.Wait()
		if waitErr != nil {
			a.emitLog("WORKFLOW", "ERROR", fmt.Sprintf("Workflow %s failed: %v", id, waitErr))
			runtime.EventsEmit(a.ctx, "workflow:complete", map[string]interface{}{"workflow_id": id, "success": false})
		} else {
			a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Workflow %s completed", id))
			runtime.EventsEmit(a.ctx, "workflow:complete", map[string]interface{}{"workflow_id": id, "success": true})
		}
	}()
	return nil
}

func (a *App) GetWorkflowExecutions(workflowID string, limit int) ([]WorkflowExecutionSummary, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := a.db.Query(`SELECT we.id, we.workflow_id, we.status, we.trigger_type,
	                                 COALESCE(we.started_at, '') as started_at,
	                                 COALESCE(we.finished_at, '') as finished_at,
	                                 COALESCE(we.error_message, '') as error,
	                                 we.created_at
	                          FROM workflow_executions we
	                          JOIN workflows w ON w.id = we.workflow_id
	                          WHERE we.workflow_id = ? AND w.profile_id = ?
	                          ORDER BY we.created_at DESC
	                          LIMIT ?`, workflowID, a.getActiveProfileID(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var execs []WorkflowExecutionSummary
	for rows.Next() {
		var e WorkflowExecutionSummary
		if rows.Scan(&e.ID, &e.WorkflowID, &e.Status, &e.TriggerType,
			&e.StartedAt, &e.FinishedAt, &e.Error, &e.CreatedAt) == nil {
			execs = append(execs, e)
		}
	}
	if execs == nil {
		execs = []WorkflowExecutionSummary{}
	}
	return execs, rows.Err()
}

func (a *App) GetRecentExecutions(limit int) ([]WorkflowExecutionSummary, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := a.db.Query(`SELECT e.id, e.workflow_id, COALESCE(w.name,'') as workflow_name,
	                                 e.status, COALESCE(e.trigger_type,''),
	                                 COALESCE(e.started_at,'') as started_at,
	                                 COALESCE(e.finished_at,'') as finished_at,
	                                 COALESCE(e.error_message,'') as error,
	                                 e.created_at
	                          FROM workflow_executions e
	                          LEFT JOIN workflows w ON e.workflow_id = w.id
	                          WHERE w.profile_id = ?
	                          ORDER BY e.created_at DESC
	                          LIMIT ?`, a.getActiveProfileID(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var execs []WorkflowExecutionSummary
	for rows.Next() {
		var e WorkflowExecutionSummary
		if rows.Scan(&e.ID, &e.WorkflowID, &e.WorkflowName, &e.Status, &e.TriggerType,
			&e.StartedAt, &e.FinishedAt, &e.Error, &e.CreatedAt) == nil {
			execs = append(execs, e)
		}
	}
	if execs == nil {
		execs = []WorkflowExecutionSummary{}
	}
	return execs, rows.Err()
}

// GetExecutionDetail returns a full execution record with per-node status.
func (a *App) GetExecutionDetail(executionID string) (map[string]interface{}, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}

	// Fetch the execution itself.
	var execID, wfID, status, triggerType, startedAt, finishedAt, errMsg, createdAt string
	err := a.db.QueryRow(`SELECT id, workflow_id, status,
	                              COALESCE(trigger_type,'') as trigger_type,
	                              COALESCE(started_at,'') as started_at,
	                              COALESCE(finished_at,'') as finished_at,
	                              COALESCE(error_message,'') as error_message,
	                              created_at
	                       FROM workflow_executions WHERE id = ? AND profile_id = ?`, executionID, a.getActiveProfileID()).
		Scan(&execID, &wfID, &status, &triggerType, &startedAt, &finishedAt, &errMsg, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("execution not found: %w", err)
	}

	// Fetch per-node execution rows.
	rows, err := a.db.Query(`SELECT id, node_id, node_name, status,
	                                COALESCE(error_message,'') as error_message,
	                                COALESCE(started_at,'') as started_at,
	                                COALESCE(finished_at,'') as finished_at,
	                                COALESCE(input_items,'[]') as input_items,
	                                COALESCE(output_items,'[]') as output_items,
	                                retry_count
	                         FROM workflow_execution_nodes
	                         WHERE execution_id = ?
	                         ORDER BY started_at`, executionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodesList []map[string]interface{}
	for rows.Next() {
		var nID, nodeID, nodeName, nStatus, nErr, nStarted, nFinished, inputItems, outputItems string
		var retryCount int
		if err := rows.Scan(&nID, &nodeID, &nodeName, &nStatus, &nErr, &nStarted, &nFinished, &inputItems, &outputItems, &retryCount); err != nil {
			continue
		}
		nodesList = append(nodesList, map[string]interface{}{
			"id":            nID,
			"node_id":       nodeID,
			"node_name":     nodeName,
			"status":        nStatus,
			"error_message": nErr,
			"started_at":    nStarted,
			"finished_at":   nFinished,
			"input_items":   inputItems,
			"output_items":  outputItems,
			"retry_count":   retryCount,
		})
	}
	if nodesList == nil {
		nodesList = []map[string]interface{}{}
	}

	return map[string]interface{}{
		"id":          execID,
		"workflow_id": wfID,
		"status":      status,
		"trigger_type": triggerType,
		"started_at":  startedAt,
		"finished_at": finishedAt,
		"error":       errMsg,
		"created_at":  createdAt,
		"nodes":       nodesList,
	}, nil
}

// CancelWorkflow cancels a running workflow execution.
func (a *App) CancelWorkflow(executionID string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}

	// Look up the workflow_id and pid for this execution, scoped to the active
	// profile so one profile cannot resolve (and kill) another's subprocess.
	var workflowID string
	var pid int
	_ = a.db.QueryRow(`SELECT workflow_id, COALESCE(pid,0) FROM workflow_executions WHERE id = ? AND profile_id = ?`, executionID, a.getActiveProfileID()).Scan(&workflowID, &pid)

	// Kill the subprocess if tracked by Wails (started via RunWorkflow).
	a.runningMu.Lock()
	killed := false
	if workflowID != "" {
		if cmd, ok := a.runningCmds[workflowID]; ok && cmd.Process != nil {
			_ = cmd.Process.Kill()
			delete(a.runningCmds, workflowID)
			killed = true
		}
	}
	if !killed {
		if cmd, ok := a.runningCmds[executionID]; ok && cmd.Process != nil {
			_ = cmd.Process.Kill()
			delete(a.runningCmds, executionID)
			killed = true
		}
	}
	a.runningMu.Unlock()

	// Kill external CLI process via PID stored in the DB.
	if !killed && pid > 0 {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}

	// Mark cancelled in DB — scoped to the active profile for safety.
	_, _ = a.db.Exec(`UPDATE workflow_executions SET status = 'CANCELLED', finished_at = CURRENT_TIMESTAMP WHERE id = ? AND profile_id = ?`, executionID, a.getActiveProfileID())
	// Reject any pending HIL items for this execution so they don't stay blocked forever.
	_, _ = a.db.Exec(`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='pending' AND profile_id = ?`, executionID, a.getActiveProfileID())
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Execution %s cancelled", executionID))
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
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

// ─────────────────────────────────────────────────────────────────────────────
// Node types registry
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) GetWorkflowNodeTypes() map[string]interface{} {
	type nodeDesc struct {
		Type        string               `json:"type"`
		Label       string               `json:"label"`
		Category    string               `json:"category"`
		Description string               `json:"description"`
		Schema      *workflow.NodeSchema `json:"schema,omitempty"`
	}
	mkNode := func(t, label, cat, desc string) nodeDesc {
		schema, _ := workflow.LoadDefaultSchema(t)
		return nodeDesc{Type: t, Label: label, Category: cat, Description: desc, Schema: schema}
	}

	catalog := map[string][]nodeDesc{
		"triggers": []nodeDesc{
			mkNode("trigger.manual", "Manual Trigger", "triggers", "Start a workflow run manually"),
			mkNode("trigger.schedule", "Schedule", "triggers", "Run workflow on a cron or interval schedule"),
			mkNode("trigger.webhook", "Webhook", "triggers", "Start workflow on incoming HTTP request"),
		},
		"control": []nodeDesc{
			mkNode("core.if", "If", "control", "Branch execution based on a condition"),
			mkNode("core.switch", "Switch", "control", "Route execution to one of multiple branches"),
			mkNode("core.merge", "Merge", "control", "Merge multiple input branches into one"),
			mkNode("core.split_in_batches", "Split In Batches", "control", "Process items in fixed-size batches"),
			mkNode("core.wait", "Wait", "control", "Pause execution for a specified duration"),
			mkNode("core.stop_error", "Stop And Error", "control", "Halt workflow with an error message"),
			mkNode("core.set", "Set", "control", "Set or transform data fields"),
			mkNode("core.code", "Code", "control", "Execute arbitrary JavaScript code"),
			mkNode("core.filter", "Filter", "control", "Keep only items matching a condition"),
			mkNode("core.sort", "Sort", "control", "Sort items by one or more fields"),
			mkNode("core.limit", "Limit", "control", "Limit the number of output items"),
			mkNode("core.remove_duplicates", "Remove Duplicates", "control", "Deduplicate items by key"),
			mkNode("core.compare_datasets", "Compare Datasets", "control", "Compare two datasets and output differences"),
			mkNode("core.aggregate", "Aggregate", "control", "Aggregate multiple items into one"),
			mkNode("core.human_in_loop", "Human in Loop", "control", "Pause and wait for a human to review, edit, and approve the data"),
		},
		"data": []nodeDesc{
			mkNode("data.datetime", "Date & Time", "data", "Parse, format, and manipulate date/time values"),
			mkNode("data.crypto", "Crypto", "data", "Hash, encrypt, or sign data"),
			mkNode("data.html", "HTML", "data", "Parse or generate HTML"),
			mkNode("data.xml", "XML", "data", "Parse or generate XML"),
			mkNode("data.markdown", "Markdown", "data", "Convert Markdown to HTML and vice-versa"),
			mkNode("data.spreadsheet", "Spreadsheet", "data", "Read or write spreadsheet data"),
			mkNode("data.compression", "Compression", "data", "Compress or decompress files"),
			mkNode("data.write_binary_file", "Write Binary File", "data", "Write binary data to disk"),
		},
		"http": []nodeDesc{
			mkNode("http.request", "HTTP Request", "http", "Make an HTTP/S request"),
			mkNode("http.ftp", "FTP", "http", "Transfer files via FTP/SFTP"),
			mkNode("http.ssh", "SSH", "http", "Execute commands over SSH"),
		},
		"system": []nodeDesc{
			mkNode("system.execute_command", "Execute Command", "system", "Run a shell command on the host"),
			mkNode("system.rss_read", "RSS Read", "system", "Fetch and parse an RSS/Atom feed"),
		},
		"db": []nodeDesc{
			mkNode("db.mysql", "MySQL", "db", "Query a MySQL/MariaDB database"),
			mkNode("db.postgres", "Postgres", "db", "Query a PostgreSQL database"),
			mkNode("db.mongodb", "MongoDB", "db", "Interact with a MongoDB collection"),
			mkNode("db.redis", "Redis", "db", "Read/write keys in a Redis store"),
		},
		"comm": []nodeDesc{
			mkNode("comm.email_send", "Send Email", "comm", "Send an email via SMTP"),
			mkNode("comm.email_read", "Read Email", "comm", "Read emails via IMAP"),
			mkNode("comm.slack", "Slack", "comm", "Send or read Slack messages"),
			mkNode("comm.telegram", "Telegram", "comm", "Send or receive Telegram messages"),
			mkNode("comm.discord", "Discord", "comm", "Send messages to a Discord channel"),
			mkNode("comm.twilio", "Twilio", "comm", "Send SMS or make calls via Twilio"),
			mkNode("comm.whatsapp", "WhatsApp", "comm", "Send WhatsApp messages"),
		},
		"service": []nodeDesc{
			mkNode("service.github", "GitHub", "service", "Interact with GitHub repositories and issues"),
			mkNode("service.airtable", "Airtable", "service", "Read/write Airtable bases"),
			mkNode("service.notion", "Notion", "service", "Read/write Notion pages and databases"),
			mkNode("service.jira", "Jira", "service", "Manage Jira issues and projects"),
			mkNode("service.linear", "Linear", "service", "Manage Linear issues and cycles"),
			mkNode("service.asana", "Asana", "service", "Manage Asana tasks and projects"),
			mkNode("service.stripe", "Stripe", "service", "Interact with Stripe payments"),
			mkNode("service.shopify", "Shopify", "service", "Manage Shopify orders and products"),
			mkNode("service.salesforce", "Salesforce", "service", "Read/write Salesforce records"),
			mkNode("service.hubspot", "HubSpot", "service", "Manage HubSpot CRM contacts and deals"),
			mkNode("service.google_sheets", "Google Sheets", "service", "Read/write Google Sheets"),
			mkNode("service.gmail", "Gmail", "service", "Send and read Gmail messages"),
			mkNode("service.outlook_mail", "Outlook", "service", "Send and read Outlook/Hotmail messages via Microsoft Graph"),
			mkNode("service.google_drive", "Google Drive", "service", "Manage Google Drive files"),
			mkNode("service.huggingface", "HuggingFace", "service", "Generate images or text via HuggingFace Inference API"),
			mkNode("service.openrouter", "OpenRouter", "service", "Generate text or images via OpenRouter AI API"),
		},
		"ai": []nodeDesc{
			mkNode("ai.chat", "AI Chat", "ai", "Send a prompt to an AI model and get a response"),
			mkNode("ai.extract", "AI Extract", "ai", "Extract structured data from text using AI"),
			mkNode("ai.classify", "AI Classify", "ai", "Classify items into categories using AI"),
			mkNode("ai.transform", "AI Transform", "ai", "Transform text content using AI"),
			mkNode("ai.embed", "AI Embed", "ai", "Generate embeddings for text content"),
			mkNode("ai.agent", "AI Agent", "ai", "Autonomous AI agent that works toward a goal"),
			mkNode("ai.read_page", "Read Page", "ai", "Crawl a webpage and return clean readable content (markdown, links, images)"),
			mkNode("ai.extract_page", "Extract Page", "ai", "Extract specific fields from a webpage using AI or CSS selectors"),
		},
		"browser": []nodeDesc{
			// Instagram
			mkNode("instagram.find_by_keyword", "Instagram: Find By Keyword", "browser", "Search Instagram users or posts by keyword"),
			mkNode("instagram.export_followers", "Instagram: Export Followers", "browser", "Export a profile's follower list"),
			mkNode("instagram.scrape_profile_info", "Instagram: Scrape Profile Info", "browser", "Collect profile metadata"),
			mkNode("instagram.engage_with_posts", "Instagram: Engage With Posts", "browser", "Like/comment on matched posts"),
			mkNode("instagram.send_dms", "Instagram: Send DMs", "browser", "Send direct messages"),
			mkNode("instagram.auto_reply_dms", "Instagram: Auto Reply DMs", "browser", "Automatically reply to incoming DMs"),
			mkNode("instagram.publish_post", "Instagram: Publish Post", "browser", "Publish a photo or reel"),
			mkNode("instagram.like_posts", "Instagram: Like Posts", "browser", "Like a list of posts"),
			mkNode("instagram.comment_on_posts", "Instagram: Comment On Posts", "browser", "Comment on a list of posts"),
			mkNode("instagram.like_comments_on_posts", "Instagram: Like Comments On Posts", "browser", "Like comments on posts"),
			mkNode("instagram.extract_post_data", "Instagram: Extract Post Data", "browser", "Extract structured data from posts"),
			mkNode("instagram.follow_users", "Instagram: Follow Users", "browser", "Follow a list of users"),
			mkNode("instagram.unfollow_users", "Instagram: Unfollow Users", "browser", "Unfollow a list of users"),
			mkNode("instagram.watch_stories", "Instagram: Watch Stories", "browser", "View stories for a list of users"),
			mkNode("instagram.engage_user_posts", "Instagram: Engage User Posts", "browser", "Engage with a specific user's posts"),
			mkNode("instagram.list_post_comments", "Instagram: List Post Comments", "browser", "List comments on a post"),
			mkNode("instagram.list_user_posts", "Instagram: List User Posts", "browser", "List posts from a user profile"),
			mkNode("instagram.reply_to_comments", "Instagram: Reply To Comments", "browser", "Reply to comments on posts"),
			// LinkedIn
			mkNode("linkedin.find_by_keyword", "LinkedIn: Find By Keyword", "browser", "Search LinkedIn profiles by keyword"),
			mkNode("linkedin.export_followers", "LinkedIn: Export Followers", "browser", "Export a profile's connections/followers"),
			mkNode("linkedin.scrape_profile_info", "LinkedIn: Scrape Profile Info", "browser", "Collect LinkedIn profile metadata"),
			mkNode("linkedin.engage_with_posts", "LinkedIn: Engage With Posts", "browser", "Like/comment on LinkedIn posts"),
			mkNode("linkedin.send_dms", "LinkedIn: Send DMs", "browser", "Send LinkedIn direct messages"),
			mkNode("linkedin.auto_reply_dms", "LinkedIn: Auto Reply DMs", "browser", "Automatically reply to LinkedIn messages"),
			mkNode("linkedin.publish_post", "LinkedIn: Publish Post", "browser", "Publish a LinkedIn post"),
			mkNode("linkedin.comment_on_posts", "LinkedIn: Comment On Posts", "browser", "Comment on LinkedIn posts"),
			mkNode("linkedin.like_comments", "LinkedIn: Like Comments", "browser", "Like comments on LinkedIn posts"),
			mkNode("linkedin.like_posts", "LinkedIn: Like Posts", "browser", "Like LinkedIn posts"),
			mkNode("linkedin.list_post_comments", "LinkedIn: List Post Comments", "browser", "List comments on a LinkedIn post"),
			mkNode("linkedin.list_user_posts", "LinkedIn: List User Posts", "browser", "List posts from a LinkedIn profile"),
			// X (Twitter)
			mkNode("x.find_by_keyword", "X: Find By Keyword", "browser", "Search X/Twitter by keyword"),
			mkNode("x.export_followers", "X: Export Followers", "browser", "Export a profile's followers on X"),
			mkNode("x.scrape_profile_info", "X: Scrape Profile Info", "browser", "Collect X profile metadata"),
			mkNode("x.engage_with_posts", "X: Engage With Posts", "browser", "Like/reply to X posts"),
			mkNode("x.send_dms", "X: Send DMs", "browser", "Send X direct messages"),
			mkNode("x.auto_reply_dms", "X: Auto Reply DMs", "browser", "Automatically reply to X DMs"),
			mkNode("x.publish_post", "X: Publish Post", "browser", "Publish a post on X"),
			// TikTok
			mkNode("tiktok.find_by_keyword", "TikTok: Find By Keyword", "browser", "Search TikTok by keyword"),
			mkNode("tiktok.export_followers", "TikTok: Export Followers", "browser", "Export a TikTok profile's followers"),
			mkNode("tiktok.scrape_profile_info", "TikTok: Scrape Profile Info", "browser", "Collect TikTok profile metadata"),
			mkNode("tiktok.engage_with_posts", "TikTok: Engage With Posts", "browser", "Like/comment on TikTok posts"),
			mkNode("tiktok.send_dms", "TikTok: Send DMs", "browser", "Send TikTok direct messages"),
			mkNode("tiktok.auto_reply_dms", "TikTok: Auto Reply DMs", "browser", "Automatically reply to TikTok DMs"),
			mkNode("tiktok.publish_post", "TikTok: Publish Post", "browser", "Publish a TikTok video"),
			mkNode("tiktok.comment_on_video", "TikTok: Comment On Video", "browser", "Comment on a TikTok video"),
			mkNode("tiktok.duet_video", "TikTok: Duet Video", "browser", "Create a duet with a TikTok video"),
			mkNode("tiktok.follow_user", "TikTok: Follow User", "browser", "Follow a TikTok user"),
			mkNode("tiktok.like_comment", "TikTok: Like Comment", "browser", "Like a comment on a TikTok video"),
			mkNode("tiktok.like_video", "TikTok: Like Video", "browser", "Like a TikTok video"),
			mkNode("tiktok.list_user_videos", "TikTok: List User Videos", "browser", "List videos from a TikTok profile"),
			mkNode("tiktok.list_video_comments", "TikTok: List Video Comments", "browser", "List comments on a TikTok video"),
			mkNode("tiktok.share_video", "TikTok: Share Video", "browser", "Share a TikTok video"),
			mkNode("tiktok.stitch_video", "TikTok: Stitch Video", "browser", "Create a stitch with a TikTok video"),

			mkNode("gemini.generate_text", "Gemini Text", "browser", "Send a prompt to Gemini and get a text response"),
			mkNode("gemini.generate_image", "Gemini Image", "browser", "Send a prompt to Gemini and download generated images"),
		},
		"people": []nodeDesc{
			mkNode("people.save", "Save to People", "people", "Upsert items into the People tab"),
			mkNode("people.sync_outlook_message", "Sync Email to People", "people", "Upsert the sender as a person and save the message to their history"),
		},
	}

	// Reconcile the hand-written catalog against the live node registry (the
	// same one the CLI runs) so the GUI always matches what can actually run:
	// stale entries drop out, newly registered types appear with a derived label.
	reg := noderegistry.Build(a.db)
	groupOf := func(t string) string {
		prefix, _, _ := strings.Cut(t, ".")
		switch prefix {
		case "core":
			return "control"
		case "data", "http", "system", "db", "comm", "service", "ai", "people", "image":
			return prefix
		default: // platform bots: instagram, linkedin, x, tiktok, gemini, hackernews, ...
			return "browser"
		}
	}
	title := func(s string) string {
		words := strings.Split(strings.ReplaceAll(s, "_", " "), " ")
		for i, w := range words {
			if w != "" {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		return strings.Join(words, " ")
	}
	deriveLabel := func(t string) string {
		prefix, rest, _ := strings.Cut(t, ".")
		if groupOf(t) == "browser" {
			return title(prefix) + ": " + title(rest)
		}
		return title(rest)
	}

	seen := map[string]bool{}
	for group, list := range catalog {
		if group == "triggers" {
			continue // triggers run via the trigger manager, not the node registry
		}
		kept := list[:0]
		for _, n := range list {
			if reg.Has(n.Type) {
				kept = append(kept, n)
				seen[n.Type] = true
			}
		}
		catalog[group] = kept
	}
	for _, t := range reg.Types() { // Types() is sorted, so extras append deterministically
		if !seen[t] {
			g := groupOf(t)
			catalog[g] = append(catalog[g], mkNode(t, deriveLabel(t), g, ""))
		}
	}

	out := make(map[string]interface{}, len(catalog))
	for group, list := range catalog {
		out[group] = list
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Node runner — execute any node type directly
// ─────────────────────────────────────────────────────────────────────────────

// NodeRunRequest is the payload sent by the frontend to run a node.
type NodeRunRequest struct {
	NodeType string                   `json:"node_type"`
	Config   map[string]interface{}   `json:"config"`
	Items    []map[string]interface{} `json:"items"` // each element is a JSON object (the item's .json field)
}

// NodeRunResult is returned after running a node.
type NodeRunResult struct {
	Outputs []NodeRunOutput `json:"outputs"`
	Error   string          `json:"error,omitempty"`
	DurationMs int64        `json:"duration_ms"`
}

// NodeRunOutput is one output handle's items.
type NodeRunOutput struct {
	Handle string                   `json:"handle"`
	Items  []map[string]interface{} `json:"items"`
}

// RunNode executes any registered node type directly via the CLI subprocess.
// Config and input items are passed as JSON; results are returned as structured data.
// legacyNodeTypes maps old short type names to their new prefixed equivalents.
var legacyNodeTypes = map[string]string{
	"if": "core.if", "switch": "core.switch", "merge": "core.merge",
	"split_in_batches": "core.split_in_batches", "wait": "core.wait",
	"stop_error": "core.stop_error", "set": "core.set", "code": "core.code",
	"filter": "core.filter", "sort": "core.sort", "limit": "core.limit",
	"remove_duplicates": "core.remove_duplicates", "compare_datasets": "core.compare_datasets",
	"aggregate": "core.aggregate",
	"datetime": "data.datetime", "crypto": "data.crypto", "html": "data.html",
	"xml": "data.xml", "markdown": "data.markdown", "spreadsheet": "data.spreadsheet",
	"compression": "data.compression", "write_binary_file": "data.write_binary_file",
	"mysql": "db.mysql", "postgres": "db.postgres", "mongodb": "db.mongodb", "redis": "db.redis",
	"email_send": "comm.email_send", "email_read": "comm.email_read",
	"slack": "comm.slack", "telegram": "comm.telegram", "discord": "comm.discord",
	"twilio": "comm.twilio", "whatsapp": "comm.whatsapp",
	"github": "service.github", "airtable": "service.airtable", "notion": "service.notion",
	"jira": "service.jira", "linear": "service.linear", "asana": "service.asana",
	"stripe": "service.stripe", "shopify": "service.shopify", "salesforce": "service.salesforce",
	"hubspot": "service.hubspot", "google_sheets": "service.google_sheets",
	"gmail": "service.gmail", "google_drive": "service.google_drive",
}

func (a *App) RunNode(req NodeRunRequest) NodeRunResult {
	if mapped, ok := legacyNodeTypes[req.NodeType]; ok {
		req.NodeType = mapped
	}

	// Extract credential_id and pass it to the CLI via --credential so the
	// subprocess resolves the stored tokens internally against the same DB.
	// Never merge plaintext credential data into --config: process arguments
	// are world-readable to other local processes (ps / /proc/<pid>/cmdline).
	credID, _ := req.Config["credential_id"].(string)
	delete(req.Config, "credential_id")

	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return NodeRunResult{Error: err.Error()}
	}

	configBytes, err := json.Marshal(req.Config)
	if err != nil {
		return NodeRunResult{Error: "invalid config: " + err.Error()}
	}

	items := req.Items
	if len(items) == 0 {
		items = []map[string]interface{}{}
	}
	inputItems := make([]map[string]interface{}, len(items))
	for i, it := range items {
		inputItems[i] = map[string]interface{}{"json": it}
	}
	inputBytes, err := json.Marshal(inputItems)
	if err != nil {
		return NodeRunResult{Error: "invalid input: " + err.Error()}
	}

	// Pass config + input via stdin, not argv: any secrets typed directly into
	// node fields (DB passwords, API keys) would otherwise be world-readable via
	// ps / /proc/<pid>/cmdline for the subprocess lifetime.
	payload, err := json.Marshal(map[string]json.RawMessage{
		"config": json.RawMessage(configBytes),
		"input":  json.RawMessage(inputBytes),
	})
	if err != nil {
		return NodeRunResult{Error: "encoding node payload: " + err.Error()}
	}

	start := time.Now()
	args := []string{
		"--profile", a.getActiveProfileID(),
		"node", "run", req.NodeType,
		"--stdin",
		"--output", "json",
	}
	if credID != "" {
		args = append(args, "--credential", credID)
	}
	cmd := exec.Command(cliBin, args...)
	cmd.Stdin = bytes.NewReader(payload)
	out, runErr := cmd.Output()
	elapsed := time.Since(start).Milliseconds()

	if runErr != nil {
		msg := runErr.Error()
		if exitErr, ok := runErr.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			msg = strings.TrimSpace(string(exitErr.Stderr))
		}
		return NodeRunResult{Error: msg, DurationMs: elapsed}
	}

	var raw map[string][]struct {
		JSON map[string]interface{} `json:"json"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return NodeRunResult{Error: "failed to parse output: " + err.Error(), DurationMs: elapsed}
	}

	var outputs []NodeRunOutput
	for handle, rawItems := range raw {
		flat := make([]map[string]interface{}, len(rawItems))
		for i, ri := range rawItems {
			flat[i] = ri.JSON
		}
		outputs = append(outputs, NodeRunOutput{Handle: handle, Items: flat})
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Handle < outputs[j].Handle })
	return NodeRunResult{Outputs: outputs, DurationMs: elapsed}
}
