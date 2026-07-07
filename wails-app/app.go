package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"monoagent/internal/ai"
	aichat "monoagent/internal/ai/chat"
	"monoagent/internal/connections"
	"monoagent/internal/storage"
	"monoagent/internal/vault"
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
	connMgr     *connections.Manager
	aiStore     *ai.AIStore
	chatService *aichat.ChatService
	wfStore     *workflow.HybridWorkflowStore

	runningMu   sync.Mutex
	runningCmds map[string]*exec.Cmd // workflowID → running subprocess

	activeProfileID string // currently selected profile; all read/write queries are scoped to this
}

// NewApp creates the App instance.
func NewApp() *App {
	home, _ := os.UserHomeDir()
	return &App{
		dbPath:      filepath.Join(home, ".monoagent", "monoagent.db"),
		logs:        make([]LogEntry, 0, 200),
		runningCmds: make(map[string]*exec.Cmd),
	}
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
	a.activeProfileID = activeProfileID

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
	a.logs = append(a.logs, entry)
	if len(a.logs) > 500 {
		a.logs = a.logs[len(a.logs)-500:]
	}
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

	_ = a.db.QueryRow("SELECT COUNT(*) FROM crawler_sessions WHERE expiry > datetime('now') AND COALESCE(profile_id,'default') = ?", a.activeProfileID).Scan(&stats.ActiveSessions)
	_ = a.db.QueryRow("SELECT COUNT(*) FROM people WHERE COALESCE(profile_id,'default') = ?", a.activeProfileID).Scan(&stats.TotalPeople)
	_ = a.db.QueryRow("SELECT COUNT(*) FROM social_lists WHERE COALESCE(profile_id,'default') = ?", a.activeProfileID).Scan(&stats.TotalLists)

	rows, _ := a.db.Query("SELECT state, COUNT(*) FROM actions WHERE COALESCE(profile_id,'default') = ? GROUP BY state", a.activeProfileID)
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
	                               FROM crawler_sessions WHERE COALESCE(profile_id,'default') = ? ORDER BY platform`, a.activeProfileID)
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
	          FROM actions WHERE COALESCE(profile_id,'default') = ?`
	var args []interface{}
	args = append(args, a.activeProfileID)

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
	                      FROM actions WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID)
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
		req.Keywords, req.ContentMessage, paramsJSON, a.activeProfileID, now.Format(time.RFC3339), now.Format(time.RFC3339))
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
	res, err := a.db.Exec("UPDATE actions SET state = ?, updated_at_ts = ? WHERE id = ? AND COALESCE(profile_id,'default') = ?",
		strings.ToUpper(state), time.Now().Format(time.RFC3339), id, a.activeProfileID)
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
	res, err := a.db.Exec("UPDATE actions SET params = ?, updated_at_ts = ? WHERE id = ? AND COALESCE(profile_id,'default') = ?",
		paramsJSON, time.Now().Format(time.RFC3339), id, a.activeProfileID)
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
	res, err := a.db.Exec("DELETE FROM actions WHERE id = ? AND COALESCE(profile_id,'default') = ?", id, a.activeProfileID)
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
	                          WHERE action_id = ? AND COALESCE(actions.profile_id,'default') = ?
	                          ORDER BY action_targets.created_at DESC`, actionID, a.activeProfileID)
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
	if err := a.db.QueryRow(`SELECT 1 FROM actions WHERE id = ? AND COALESCE(profile_id,'default') = ?`, actionID, a.activeProfileID).Scan(&exists); err != nil {
		return fmt.Errorf("action %s not found", actionID)
	}
	id := newUUID()
	_, err := a.db.Exec(`INSERT INTO action_targets (id, action_id, platform, link, status) VALUES (?, ?, ?, ?, 'PENDING')`,
		id, actionID, strings.ToUpper(platform), link)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// People
// ─────────────────────────────────────────────────────────────────────────────

type PersonInfo struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Platform       string `json:"platform"`
	FullName       string `json:"full_name"`
	ImageURL       string `json:"image_url"`
	ProfileURL     string `json:"profile_url"`
	FollowerCount  string `json:"follower_count"`
	FollowingCount int    `json:"following_count"`
	IsVerified     bool   `json:"is_verified"`
	JobTitle       string `json:"job_title"`
	Category       string `json:"category"`
	CreatedAt      string `json:"created_at"`
}

type PersonDetailInfo struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Platform       string `json:"platform"`
	FullName       string `json:"full_name"`
	ImageURL       string `json:"image_url"`
	ProfileURL     string `json:"profile_url"`
	FollowerCount  string `json:"follower_count"`
	FollowingCount int    `json:"following_count"`
	ContentCount   int    `json:"content_count"`
	IsVerified     bool   `json:"is_verified"`
	JobTitle       string `json:"job_title"`
	Category       string `json:"category"`
	Introduction   string `json:"introduction"`
	Website        string `json:"website"`
	ContactDetails string `json:"contact_details"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type PersonInteraction struct {
	ActionID         string `json:"action_id"`
	ActionTitle      string `json:"action_title"`
	ActionType       string `json:"action_type"`
	Platform         string `json:"platform"`
	Link             string `json:"link"`
	Status           string `json:"status"`
	CommentText      string `json:"comment_text"`
	SourceType       string `json:"source_type"`
	LastInteractedAt string `json:"last_interacted_at"`
	CreatedAt        string `json:"created_at"`
}

// PostSummary is returned by GetPersonPosts.
type PostSummary struct {
	ID           string `json:"id"`
	Shortcode    string `json:"shortcode"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
	LikeCount    int    `json:"like_count"`
	CommentCount int    `json:"comment_count"`
	Caption      string `json:"caption"`
	PostedAt     string `json:"posted_at"`
	ScrapedAt    string `json:"scraped_at"`
	WeLiked      bool   `json:"we_liked"`
	WeCommented  bool   `json:"we_commented"`
}

// PostDetail is returned by GetPostDetail.
type PostDetail struct {
	ID           string `json:"id"`
	Shortcode    string `json:"shortcode"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
	LikeCount    int    `json:"like_count"`
	CommentCount int    `json:"comment_count"`
	Caption      string `json:"caption"`
	PostedAt     string `json:"posted_at"`
	ScrapedAt    string `json:"scraped_at"`
}

// PostComment is returned by GetPostComments.
type PostComment struct {
	ID         string `json:"id"`
	Author     string `json:"author"`
	Text       string `json:"text"`
	Timestamp  string `json:"timestamp"`
	LikesCount int    `json:"likes_count"`
	ReplyCount int    `json:"reply_count"`
}

func (a *App) GetPeople(platform, search string, limit, offset int) []PersonInfo {
	if a.db == nil {
		return nil
	}
	query := `SELECT id, platform_username, platform, COALESCE(full_name,''), COALESCE(image_url,''),
	                 COALESCE(profile_url,''), COALESCE(follower_count,''), COALESCE(following_count,0), COALESCE(is_verified,0),
	                 COALESCE(job_title,''), COALESCE(category,''), COALESCE(created_at,'')
	          FROM people WHERE COALESCE(profile_id,'default') = ?`
	var args []interface{}
	args = append(args, a.activeProfileID)
	if platform != "" && platform != "ALL" {
		query += " AND UPPER(platform) = ?"
		args = append(args, strings.ToUpper(platform))
	}
	if search != "" {
		query += " AND (platform_username LIKE ? OR full_name LIKE ?)"
		s := "%" + search + "%"
		args = append(args, s, s)
	}
	query += " ORDER BY created_at DESC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var people []PersonInfo
	for rows.Next() {
		var p PersonInfo
		var isVerified int
		if rows.Scan(&p.ID, &p.Username, &p.Platform, &p.FullName, &p.ImageURL,
			&p.ProfileURL, &p.FollowerCount, &p.FollowingCount, &isVerified, &p.JobTitle, &p.Category, &p.CreatedAt) == nil {
			p.IsVerified = isVerified == 1
			people = append(people, p)
		}
	}
	return people
}

func (a *App) GetPeopleCount(platform, search string) int {
	if a.db == nil {
		return 0
	}
	query := "SELECT COUNT(*) FROM people WHERE COALESCE(profile_id,'default') = ?"
	var args []interface{}
	args = append(args, a.activeProfileID)
	if platform != "" && platform != "ALL" {
		query += " AND UPPER(platform) = ?"
		args = append(args, strings.ToUpper(platform))
	}
	if search != "" {
		query += " AND (platform_username LIKE ? OR full_name LIKE ?)"
		s := "%" + search + "%"
		args = append(args, s, s)
	}
	var count int
	_ = a.db.QueryRow(query, args...).Scan(&count)
	return count
}

func (a *App) GetPersonDetail(id string) *PersonDetailInfo {
	if a.db == nil {
		return nil
	}
	row := a.db.QueryRow(`
		SELECT id, platform_username, platform,
		       COALESCE(full_name,''), COALESCE(image_url,''), COALESCE(profile_url,''),
		       COALESCE(follower_count,''), COALESCE(following_count,0), COALESCE(content_count,0), COALESCE(is_verified,0),
		       COALESCE(job_title,''), COALESCE(category,''),
		       COALESCE(introduction,''), COALESCE(website,''), COALESCE(contact_details,''),
		       COALESCE(created_at,''), COALESCE(updated_at,'')
		FROM people WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID)
	var p PersonDetailInfo
	var isVerified int
	if err := row.Scan(&p.ID, &p.Username, &p.Platform,
		&p.FullName, &p.ImageURL, &p.ProfileURL,
		&p.FollowerCount, &p.FollowingCount, &p.ContentCount, &isVerified,
		&p.JobTitle, &p.Category,
		&p.Introduction, &p.Website, &p.ContactDetails,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil
	}
	p.IsVerified = isVerified == 1
	return &p
}

func (a *App) GetPersonInteractions(id string) []PersonInteraction {
	if a.db == nil {
		return nil
	}
	rows, err := a.db.Query(`
		SELECT at.action_id, COALESCE(a.title,''), COALESCE(a.type,''),
		       at.platform, COALESCE(at.link,''), at.status,
		       COALESCE(at.comment_text,''), COALESCE(at.source_type,''),
		       COALESCE(at.last_interacted_at,''), COALESCE(at.created_at,'')
		FROM action_targets at
		LEFT JOIN actions a ON at.action_id = a.id
		JOIN people p ON at.person_id = p.id
		WHERE at.person_id = ? AND COALESCE(p.profile_id,'default') = ?
		ORDER BY COALESCE(at.last_interacted_at, at.created_at) DESC
		LIMIT 200`, id, a.activeProfileID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var interactions []PersonInteraction
	for rows.Next() {
		var i PersonInteraction
		if rows.Scan(&i.ActionID, &i.ActionTitle, &i.ActionType,
			&i.Platform, &i.Link, &i.Status,
			&i.CommentText, &i.SourceType,
			&i.LastInteractedAt, &i.CreatedAt) == nil {
			interactions = append(interactions, i)
		}
	}
	return interactions
}

// GetPersonMessages returns a person's message/interaction history (from
// Outlook, social platforms, manual notes, ...), delegating to the same
// storage.PersonMessage repo used by `monoagentcli people messages`.
func (a *App) GetPersonMessages(personID string) []*storage.PersonMessage {
	if a.db == nil {
		return nil
	}
	messages, err := (&storage.Database{DB: a.db}).ListPersonMessages(personID, "", a.activeProfileID, 0, 0)
	if err != nil {
		return nil
	}
	return messages
}

// AddPersonMessage records a message/interaction for a person, delegating to
// the same storage.PersonMessage repo used by `monoagentcli people messages add`.
func (a *App) AddPersonMessage(personID, source, externalID, direction, sender, subject, body string) error {
	if a.db == nil {
		return fmt.Errorf("database not initialized")
	}
	msg := &storage.PersonMessage{
		PersonID:   personID,
		Source:     source,
		ExternalID: externalID,
		Direction:  direction,
		Sender:     sender,
		Subject:    subject,
		Body:       body,
	}
	return (&storage.Database{DB: a.db}).UpsertPersonMessage(msg, a.activeProfileID)
}

// GetPersonPosts returns all scraped posts for a person, with we_liked/we_commented flags.
func (a *App) GetPersonPosts(personID string) []PostSummary {
	if a.db == nil {
		return []PostSummary{}
	}
	rows, err := a.db.Query(`
		SELECT
			p.id,
			p.shortcode,
			p.url,
			COALESCE(p.thumbnail_url, ''),
			COALESCE(p.like_count, 0),
			COALESCE(p.comment_count, 0),
			COALESCE(p.caption, ''),
			COALESCE(p.posted_at, ''),
			p.scraped_at,
			EXISTS(
				SELECT 1 FROM action_targets at2
				JOIN actions a2 ON at2.action_id = a2.id
				WHERE rtrim(at2.link, '/') = rtrim(p.url, '/')
				  AND a2.type = 'like_posts'
				  AND at2.status = 'COMPLETED'
			) AS we_liked,
			EXISTS(
				SELECT 1 FROM action_targets at3
				JOIN actions a3 ON at3.action_id = a3.id
				WHERE rtrim(at3.link, '/') = rtrim(p.url, '/')
				  AND a3.type = 'comment_on_posts'
				  AND at3.status = 'COMPLETED'
			) AS we_commented
		FROM posts p
		JOIN people pe ON p.person_id = pe.id
		WHERE p.person_id = ? AND COALESCE(pe.profile_id,'default') = ?
		ORDER BY p.scraped_at DESC`,
		personID, a.activeProfileID,
	)
	if err != nil {
		return []PostSummary{}
	}
	defer rows.Close()

	var posts []PostSummary
	for rows.Next() {
		var p PostSummary
		var weLiked, weCommented int
		if err := rows.Scan(
			&p.ID, &p.Shortcode, &p.URL, &p.ThumbnailURL,
			&p.LikeCount, &p.CommentCount, &p.Caption,
			&p.PostedAt, &p.ScrapedAt,
			&weLiked, &weCommented,
		); err != nil {
			continue
		}
		p.WeLiked = weLiked != 0
		p.WeCommented = weCommented != 0
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return []PostSummary{}
	}
	if posts == nil {
		return []PostSummary{}
	}
	return posts
}

// GetPostDetail returns full metadata for a single post by ID.
func (a *App) GetPostDetail(postID string) *PostDetail {
	if a.db == nil {
		return nil
	}
	var p PostDetail
	err := a.db.QueryRow(`
		SELECT posts.id, shortcode, url,
		       COALESCE(thumbnail_url, ''),
		       COALESCE(like_count, 0),
		       COALESCE(comment_count, 0),
		       COALESCE(caption, ''),
		       COALESCE(posted_at, ''),
		       scraped_at
		FROM posts
		JOIN people ON posts.person_id = people.id
		WHERE posts.id = ? AND COALESCE(people.profile_id,'default') = ?`,
		postID, a.activeProfileID,
	).Scan(
		&p.ID, &p.Shortcode, &p.URL, &p.ThumbnailURL,
		&p.LikeCount, &p.CommentCount, &p.Caption,
		&p.PostedAt, &p.ScrapedAt,
	)
	if err != nil {
		return nil
	}
	return &p
}

// GetPostComments returns all scraped comments for a post, ordered by timestamp.
func (a *App) GetPostComments(postID string) []PostComment {
	if a.db == nil {
		return []PostComment{}
	}
	rows, err := a.db.Query(`
		SELECT post_comments.id, COALESCE(author, ''), COALESCE(text, ''),
		       COALESCE(timestamp, ''),
		       COALESCE(likes_count, 0),
		       COALESCE(reply_count, 0)
		FROM post_comments
		JOIN posts ON post_comments.post_id = posts.id
		JOIN people ON posts.person_id = people.id
		WHERE post_id = ? AND COALESCE(people.profile_id,'default') = ?
		ORDER BY timestamp ASC`,
		postID, a.activeProfileID,
	)
	if err != nil {
		return []PostComment{}
	}
	defer rows.Close()

	var comments []PostComment
	for rows.Next() {
		var c PostComment
		if err := rows.Scan(
			&c.ID, &c.Author, &c.Text,
			&c.Timestamp, &c.LikesCount, &c.ReplyCount,
		); err != nil {
			continue
		}
		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return []PostComment{}
	}
	if comments == nil {
		return []PostComment{}
	}
	return comments
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
	rows, err := a.db.Query(`SELECT id, name, color FROM tags WHERE COALESCE(profile_id,'default') = ? ORDER BY name COLLATE NOCASE`, a.activeProfileID)
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
		WHERE pt.person_id = ? AND COALESCE(t.profile_id,'default') = ? AND COALESCE(p.profile_id,'default') = ?
		ORDER BY t.name COLLATE NOCASE`, personId, a.activeProfileID, a.activeProfileID)
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
	if err := a.db.QueryRow(`SELECT 1 FROM people WHERE id = ? AND COALESCE(profile_id,'default') = ?`, personId, a.activeProfileID).Scan(&personExists); err != nil {
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
	err = tx.QueryRow(`SELECT id, color FROM tags WHERE LOWER(name) = LOWER(?) AND COALESCE(profile_id,'default') = ?`, tagName, a.activeProfileID).Scan(&tagId, &tagColor)
	if err != nil {
		// Create new tag scoped to the active profile.
		tagId = newUUID()
		if color == "" {
			color = "#00b4d8"
		}
		if _, err = tx.Exec(`INSERT INTO tags(id, name, color, profile_id) VALUES(?,?,?,?)`, tagId, tagName, color, a.activeProfileID); err != nil {
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
	if err := a.db.QueryRow(`SELECT 1 FROM people WHERE id = ? AND COALESCE(profile_id,'default') = ?`, personId, a.activeProfileID).Scan(&exists); err != nil {
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
	args = append(args, a.activeProfileID, a.activeProfileID)
	query := fmt.Sprintf(`
		SELECT pt.person_id, t.id, t.name, t.color
		FROM people_tags pt
		JOIN tags t ON t.id = pt.tag_id
		JOIN people p ON pt.person_id = p.id
		WHERE pt.person_id IN (%s) AND COALESCE(t.profile_id,'default') = ? AND COALESCE(p.profile_id,'default') = ?
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
	                          FROM crawler_sessions WHERE COALESCE(profile_id,'default') = ? ORDER BY platform, username`, a.activeProfileID)
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
		 FROM crawler_sessions WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID,
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
	res, err := a.db.Exec("DELETE FROM crawler_sessions WHERE id = ? AND COALESCE(profile_id,'default') = ?", id, a.activeProfileID)
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
	                          FROM social_lists WHERE COALESCE(profile_id,'default') = ? ORDER BY created_at DESC`, a.activeProfileID)
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
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}

	_, _ = a.db.Exec("UPDATE actions SET state = 'RUNNING', updated_at_ts = ? WHERE id = ? AND COALESCE(profile_id,'default') = ?",
		time.Now().Format(time.RFC3339), id, a.activeProfileID)

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
// Logs
// ─────────────────────────────────────────────────────────────────────────────

type LogEntry struct {
	Time    string `json:"time"`
	Source  string `json:"source"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (a *App) GetLogs() []LogEntry {
	return a.logs
}

func (a *App) ClearLogs() {
	a.logs = make([]LogEntry, 0, 200)
}

// ─────────────────────────────────────────────────────────────────────────────
// Metadata
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) GetAvailableActionTypes() map[string][]string {
	return map[string][]string{
		"INSTAGRAM": {
			"find_by_keyword", "export_followers", "scrape_profile_info", "engage_with_posts",
			"send_dms", "auto_reply_dms", "publish_post",
			"like_posts", "comment_on_posts", "like_comments_on_posts", "extract_post_data",
			"follow_users", "unfollow_users", "watch_stories", "engage_user_posts",
		},
		"LINKEDIN": {
			"find_by_keyword", "export_followers", "scrape_profile_info", "engage_with_posts",
			"send_dms", "auto_reply_dms", "publish_post",
		},
		"X": {
			"find_by_keyword", "export_followers", "scrape_profile_info", "engage_with_posts",
			"send_dms", "auto_reply_dms", "publish_post",
		},
		"TIKTOK": {
			"find_by_keyword", "export_followers", "scrape_profile_info", "engage_with_posts",
			"send_dms", "auto_reply_dms", "publish_post",
		},
	}
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
			p.IsActive = p.ID == a.activeProfileID
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
	a.activeProfileID = id
	a.emitLog("SYSTEM", "INFO", "Switched to profile: "+id)
	return nil
}

func (a *App) GetActiveProfile() (*ProfileInfo, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	var p ProfileInfo
	err := a.db.QueryRow(`SELECT id, name, created_at FROM profiles WHERE id = ?`, a.activeProfileID).
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

func (a *App) ListWorkflows() ([]WorkflowSummary, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	rows, err := a.db.Query(`SELECT id, name, COALESCE(description,''), is_active, version,
	                                 COALESCE(created_at,''), COALESCE(updated_at,'')
	                          FROM workflows
	                          WHERE COALESCE(profile_id,'default') = ?
	                          ORDER BY updated_at DESC`, a.activeProfileID)
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
		_ = a.db.QueryRow(`SELECT COALESCE(profile_id,'default') FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.activeProfileID {
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
		_ = a.db.QueryRow(`SELECT COALESCE(profile_id,'default') FROM workflows WHERE id = ?`, req.ID).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.activeProfileID {
			return nil, fmt.Errorf("workflow %s not found", req.ID)
		}
	}
	ctx := context.Background()
	wf := &workflow.Workflow{
		ID:          req.ID,
		Name:        req.Name,
		Description: req.Description,
		IsActive:    req.IsActive,
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
		_, _ = a.db.Exec(`UPDATE workflows SET profile_id = ? WHERE id = ?`, a.activeProfileID, wf.ID)
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
		_ = a.db.QueryRow(`SELECT COALESCE(profile_id,'default') FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.activeProfileID {
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
		_ = a.db.QueryRow(`SELECT COALESCE(profile_id,'default') FROM workflows WHERE id = ?`, id).Scan(&wfProfile)
		if wfProfile != "" && wfProfile != a.activeProfileID {
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
		_, _ = a.db.Exec("UPDATE workflows SET is_active = 1 WHERE id = ? AND COALESCE(profile_id,'default') = ?", id, a.activeProfileID)
	}

	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Starting workflow %s", id))

	cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.activeProfileID, "workflow", "run", id)
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
	                          WHERE we.workflow_id = ? AND COALESCE(w.profile_id,'default') = ?
	                          ORDER BY we.created_at DESC
	                          LIMIT ?`, workflowID, a.activeProfileID, limit)
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
	                          WHERE COALESCE(w.profile_id, 'default') = ?
	                          ORDER BY e.created_at DESC
	                          LIMIT ?`, a.activeProfileID, limit)
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
	                       FROM workflow_executions WHERE id = ? AND COALESCE(profile_id,'default') = ?`, executionID, a.activeProfileID).
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
	_ = a.db.QueryRow(`SELECT workflow_id, COALESCE(pid,0) FROM workflow_executions WHERE id = ? AND COALESCE(profile_id,'default') = ?`, executionID, a.activeProfileID).Scan(&workflowID, &pid)

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
	_, _ = a.db.Exec(`UPDATE workflow_executions SET status = 'CANCELLED', finished_at = CURRENT_TIMESTAMP WHERE id = ? AND COALESCE(profile_id,'default') = ?`, executionID, a.activeProfileID)
	// Reject any pending HIL items for this execution so they don't stay blocked forever.
	_, _ = a.db.Exec(`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE execution_id=? AND status='pending' AND COALESCE(profile_id,'default') = ?`, executionID, a.activeProfileID)
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
		 WHERE h.status = 'pending' AND COALESCE(h.profile_id, 'default') = ?
		 ORDER BY h.created_at ASC`,
		a.activeProfileID,
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
		`UPDATE hil_pending SET status='approved', edited_data=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending' AND COALESCE(profile_id,'default') = ?`,
		editedDataJSON, id, a.activeProfileID,
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
		`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending' AND COALESCE(profile_id,'default') = ?`,
		id, a.activeProfileID,
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
	                          FROM credentials WHERE profile_id = ? ORDER BY name`, a.activeProfileID)
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
			credID, req.Name, req.ServiceType, dataJSON, a.activeProfileID, now, now)
		if err != nil {
			return nil, fmt.Errorf("insert credential: %w", err)
		}
	} else {
		credID = req.ID
		res, err := a.db.Exec(`UPDATE credentials SET name = ?, service_type = ?, encrypted_data = ?, updated_at = ?
		                      WHERE id = ? AND COALESCE(profile_id,'default') = ?`,
			req.Name, req.ServiceType, dataJSON, now, credID, a.activeProfileID)
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
	res, err := a.db.Exec(`DELETE FROM credentials WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID)
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

	return map[string]interface{}{
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
		},
	}
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

	// Resolve credential_id → merge connection data into config.
	if credID, ok := req.Config["credential_id"].(string); ok && credID != "" {
		if a.connMgr != nil {
			credData, err := a.getResourceCredentialData(context.Background(), credID)
			if err != nil {
				platform := nodeTypeToPlatform(req.NodeType)
				if platform != "" {
					credData, err = a.getResourceCredentialData(context.Background(), platform)
				}
				if err != nil {
					return NodeRunResult{Error: fmt.Sprintf("resolve credential %s: %v", credID, err)}
				}
			}
			for k, v := range credData {
				req.Config[k] = v
			}
			delete(req.Config, "credential_id")
		}
	}

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

	start := time.Now()
	cmd := exec.Command(cliBin,
		"node", "run", req.NodeType,
		"--config", string(configBytes),
		"--input", string(inputBytes),
		"--output", "json",
	)
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

// ─────────────────────────────────────────────────────────────────────────────
// Connections
// ─────────────────────────────────────────────────────────────────────────────

// CredentialOption is a lightweight connection summary used to populate
// credential dropdowns in the workflow node inspector.
type CredentialOption struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Platform string `json:"platform"`
	Method   string `json:"method"`
}

// ListCredentialsForNode returns credential options relevant to a given node type.
// Social platform nodes (action.instagram.*, action.linkedin.*, etc.) get browser
// method credentials; API service nodes get their matching platform's connections.
func (a *App) ListCredentialsForNode(nodeType string) []CredentialOption {
	if a.db == nil {
		return []CredentialOption{}
	}
	store := connections.NewStore(a.db)
	var platform string

	// Detect social platform from node type prefix (e.g. "action.instagram.publish_post")
	socialPlatforms := []string{"instagram", "linkedin", "tiktok", "x", "twitter"}
	lnodeType := strings.ToLower(nodeType)
	for _, sp := range socialPlatforms {
		if strings.Contains(lnodeType, sp) {
			platform = sp
			break
		}
	}

	// Service nodes: map by known service identifiers
	if platform == "" {
		serviceMap := map[string]string{
			"openrouter":    "openrouter",
			"huggingface":   "huggingface",
			"google_sheets": "google",
			"gmail":         "google",
			"slack":         "slack",
		}
		for key, pid := range serviceMap {
			if strings.Contains(lnodeType, key) {
				platform = pid
				break
			}
		}
	}

	var conns []connections.Connection
	var err error
	if platform != "" {
		conns, err = store.ListByPlatform(a.ctx, platform, a.activeProfileID)
	} else {
		conns, err = store.ListAll(a.ctx, a.activeProfileID)
	}
	if err != nil || conns == nil {
		return []CredentialOption{}
	}
	opts := make([]CredentialOption, 0, len(conns))
	for _, c := range conns {
		opts = append(opts, CredentialOption{
			ID:       c.ID,
			Label:    c.Label,
			Platform: c.Platform,
			Method:   string(c.Method),
		})
	}
	return opts
}

// ListConnections returns all saved connections for the active profile, filtered by platform if non-empty.
func (a *App) ListConnections(platform string) []connections.Connection {
	if a.connMgr == nil {
		return []connections.Connection{}
	}
	result, err := a.connMgr.List(a.ctx, platform, a.activeProfileID)
	if err != nil {
		return []connections.Connection{}
	}
	if result == nil {
		result = []connections.Connection{}
	}
	return result
}

// PlatformInfo is a frontend-safe representation of a platform (no OAuth secrets).
type PlatformInfo struct {
	ID         string                                       `json:"id"`
	Name       string                                       `json:"name"`
	Category   string                                       `json:"category"`
	ConnectVia string                                       `json:"connectVia"`
	Methods    []string                                     `json:"methods"`
	Fields     map[string][]connections.CredentialField     `json:"fields"`
	IconEmoji  string                                       `json:"iconEmoji"`
}

func toPlatformInfo(p connections.PlatformDef) PlatformInfo {
	methods := make([]string, len(p.Methods))
	for i, m := range p.Methods {
		methods[i] = string(m)
	}
	fields := make(map[string][]connections.CredentialField)
	for method, cfields := range p.Fields {
		fields[string(method)] = cfields
	}
	return PlatformInfo{
		ID:         p.ID,
		Name:       p.Name,
		Category:   p.Category,
		ConnectVia: p.ConnectVia,
		Methods:    methods,
		Fields:     fields,
		IconEmoji:  p.IconEmoji,
	}
}

// ListPlatformsJSON returns all platforms as a JSON string (bypasses Wails type serialization).
func (a *App) ListPlatformsJSON(connectVia string) string {
	var platforms []connections.PlatformDef
	if connectVia == "" {
		platforms = connections.All()
	} else {
		platforms = connections.ByConnectVia(connectVia)
	}
	result := make([]PlatformInfo, len(platforms))
	for i, p := range platforms {
		result[i] = toPlatformInfo(p)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// TestConnection re-validates a connection by ID.
// For OAuth connections it attempts a silent token refresh first.
// For browser sessions (social platforms), it checks session expiry and cookie presence.
func (a *App) TestConnection(id string) string {
	if a.connMgr == nil && a.db == nil {
		return "error: manager not initialized"
	}

	// First try the connections table (OAuth/API key connections).
	if a.connMgr != nil {
		if conn, err := a.connMgr.Get(a.ctx, id); err == nil && conn != nil {
			// Enforce profile scope.
			if conn.ProfileID != "" && conn.ProfileID != a.activeProfileID {
				return "error: connection not found"
			}
			// OAuth: attempt silent token refresh before testing.
			if conn.Method == "oauth" {
				if _, refreshErr := a.getResourceCredentialData(a.ctx, id); refreshErr != nil {
					fmt.Printf("token refresh attempted: %v\n", refreshErr)
				}
			}
			if err := a.connMgr.Test(a.ctx, id); err != nil {
				return fmt.Sprintf("error: %v", err)
			}
			return "ok"
		}
	}

	// Fallback: check crawler_sessions (browser sessions for social platforms).
	// The UI passes integer session IDs for social platforms.
	if a.db != nil {
		var platform, cookiesJSON, expiry string
		err := a.db.QueryRow(
			`SELECT platform, cookies_json, expiry FROM crawler_sessions WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID,
		).Scan(&platform, &cookiesJSON, &expiry)
		if err == nil {
			// Check expiry
			if exp, pErr := time.Parse("2006-01-02 15:04:05", expiry); pErr == nil {
				if time.Now().After(exp) {
					return "error: session expired — please log in again via the browser"
				}
			} else if exp2, pErr2 := time.Parse(time.RFC3339, expiry); pErr2 == nil {
				if time.Now().After(exp2) {
					return "error: session expired — please log in again via the browser"
				}
			}
			// Check cookies present
			if cookiesJSON == "" || cookiesJSON == "[]" || cookiesJSON == "null" {
				return "error: no session cookies stored — please log in again"
			}
			return "ok"
		}
	}

	// Also try looking up by platform name (fallback for credential_id = platform string).
	if a.connMgr != nil {
		platform := nodeTypeToPlatform(id)
		if conns, err := a.connMgr.List(a.ctx, platform, a.activeProfileID); err == nil && len(conns) > 0 {
			for _, c := range conns {
				if c.Status == "active" {
					return "ok"
				}
			}
		}
	}

	return "error: connection not found"
}

// RemoveConnection deletes a connection by ID, scoped to the active profile.
func (a *App) RemoveConnection(id string) string {
	if a.connMgr == nil {
		return "error: manager not initialized"
	}
	// Verify ownership before deletion.
	conn, err := a.connMgr.Get(a.ctx, id)
	if err != nil || conn == nil {
		return "error: connection not found"
	}
	if conn.ProfileID != "" && conn.ProfileID != a.activeProfileID {
		return "error: connection not found"
	}
	if err := a.connMgr.Remove(a.ctx, id, a.activeProfileID); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}

// GetConnectionsForPlatform returns connections filtered by platform ID.
func (a *App) GetConnectionsForPlatform(platformID string) []connections.Connection {
	return a.ListConnections(platformID)
}

// ensureOAuthCredsTable creates the platform_oauth_credentials table if it doesn't exist.
func (a *App) ensureOAuthCredsTable() error {
	_, err := a.db.Exec(`CREATE TABLE IF NOT EXISTS platform_oauth_credentials (
		platform      TEXT PRIMARY KEY,
		client_id     TEXT NOT NULL,
		client_secret TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	)`)
	return err
}

// GetOAuthCredentials returns the stored OAuth client_id and client_secret for a platform as JSON.
// Returns JSON {"clientID":"...","clientSecret":"..."} or "" if not set.
func (a *App) GetOAuthCredentials(platformID string) string {
	if a.db == nil {
		return ""
	}
	_ = a.ensureOAuthCredsTable()
	var clientID, clientSecret string
	err := a.db.QueryRow(
		`SELECT client_id, client_secret FROM platform_oauth_credentials WHERE platform = ?`, platformID,
	).Scan(&clientID, &clientSecret)
	if err != nil {
		return ""
	}
	b, _ := json.Marshal(map[string]string{"clientID": clientID, "clientSecret": clientSecret})
	return string(b)
}

// SetOAuthCredentials saves OAuth client_id and client_secret for a platform.
func (a *App) SetOAuthCredentials(platformID, clientID, clientSecret string) string {
	if a.db == nil {
		return "error: db not available"
	}
	if clientID == "" || clientSecret == "" {
		return "error: clientID and clientSecret are required"
	}
	_ = a.ensureOAuthCredsTable()
	_, err := a.db.Exec(
		`INSERT OR REPLACE INTO platform_oauth_credentials (platform, client_id, client_secret, updated_at)
		 VALUES (?, ?, ?, ?)`,
		platformID, clientID, clientSecret, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return "ok"
}

// ConnectPlatformOAuth starts an OAuth flow in a background goroutine.
// Emits "conn:progress" events with {platform, message, kind} and a final
// "conn:done" event with {platform, success, accountID?, error?}.
// Returns "started" immediately, or "error: ..." if preconditions fail.
func (a *App) ConnectPlatformOAuth(platformID string) string {
	if a.connMgr == nil {
		return "error: manager not initialized"
	}
	p, ok := connections.Get(platformID)
	if !ok {
		return fmt.Sprintf("error: unknown platform %q", platformID)
	}
	if p.OAuth == nil {
		return "error: platform does not support OAuth"
	}

	go func() {
		emit := func(msg, kind string) {
			runtime.EventsEmit(a.ctx, "conn:progress", map[string]interface{}{
				"platform": platformID,
				"message":  msg,
				"kind":     kind,
			})
		}

		// Resolve DB-stored OAuth credentials and pass them directly.
		var oauthClientID, oauthClientSecret string
		if credsJSON := a.GetOAuthCredentials(platformID); credsJSON != "" {
			var creds map[string]string
			if json.Unmarshal([]byte(credsJSON), &creds) == nil {
				oauthClientID = creds["clientID"]
				oauthClientSecret = creds["clientSecret"]
			}
		}

		conn, err := a.connMgr.ConnectOAuthWithProgress(a.ctx, platformID, emit, oauthClientID, oauthClientSecret, a.activeProfileID)
		if err != nil {
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{
				"platform": platformID,
				"success":  false,
				"error":    err.Error(),
			})
			return
		}

		runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{
			"platform":  platformID,
			"success":   true,
			"accountID": conn.AccountID,
		})
	}()

	return "started"
}

// LoginSocial spawns `monoagentcli login <platform>` as a subprocess.
// The CLI handles the browser, cookie capture, and session storage.
// Progress events are streamed to the UI via stdout scanning.
// Returns "started" immediately or "error: ..." if the binary is not found.
func (a *App) LoginSocial(platform string) string {
	pid := strings.ToLower(platform)
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	emit := func(msg, kind string) {
		runtime.EventsEmit(a.ctx, "conn:progress", map[string]interface{}{
			"platform": pid,
			"message":  msg,
			"kind":     kind,
		})
	}

	go func() {
		cmd := exec.CommandContext(a.ctx, cliBin, "--profile", a.activeProfileID, "login", pid)
		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()

		if startErr := cmd.Start(); startErr != nil {
			emit(fmt.Sprintf("Failed to start login: %v", startErr), "error")
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": startErr.Error()})
			return
		}
		emit("Browser opened — please log in in the window that appeared", "info")

		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				emit(scanner.Text(), "info")
			}
		}()

		scanner := bufio.NewScanner(stdout)
		var username string
		for scanner.Scan() {
			line := scanner.Text()
			emit(line, "info")
			// CLI prints "username: <name>" on success.
			if strings.HasPrefix(line, "username: ") {
				username = strings.TrimPrefix(line, "username: ")
			}
		}

		waitErr := cmd.Wait()
		if waitErr != nil {
			runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": false, "error": waitErr.Error()})
			return
		}
		if username == "" {
			username = "unknown"
		}
		runtime.EventsEmit(a.ctx, "conn:done", map[string]interface{}{"platform": pid, "success": true, "accountID": username})
	}()

	return "started"
}

// SaveConnectionDirect saves a connection directly from the UI with provided field values.
// fieldValuesJSON is a JSON object string (avoids Wails map serialization issues).
// Returns "ok:<id>" on success or "error: ..." on failure.
func (a *App) SaveConnectionDirect(platformID string, method string, fieldValuesJSON string) string {
	if a.connMgr == nil {
		return "error: manager not initialized"
	}
	p, ok := connections.Get(platformID)
	if !ok {
		return fmt.Sprintf("error: unknown platform %q", platformID)
	}
	var fieldValues map[string]interface{}
	if err := json.Unmarshal([]byte(fieldValuesJSON), &fieldValues); err != nil {
		return fmt.Sprintf("error: invalid field values JSON: %v", err)
	}
	now := time.Now().Format(time.RFC3339)
	conn := &connections.Connection{
		ID:        uuid.New().String(),
		Platform:  platformID,
		Method:    connections.AuthMethod(method),
		Label:     p.Name,
		Data:      fieldValues,
		Status:    "active",
		ProfileID: a.activeProfileID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Validate the connection
	accountID, err := connections.ValidateConnection(a.ctx, conn)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if accountID != "" {
		conn.AccountID = accountID
		conn.Label = fmt.Sprintf("%s – %s", p.Name, accountID)
	}
	// Save to DB
	store := connections.NewStore(a.db)
	if err := store.EnsureTable(a.ctx); err != nil {
		return fmt.Sprintf("error: table init: %v", err)
	}
	if err := store.Save(a.ctx, conn); err != nil {
		return fmt.Sprintf("error: save: %v", err)
	}
	return "ok:" + conn.ID
}

// ─────────────────────────────────────────────────────────────────────────────
// AI Providers
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) ListAIProviders() string {
	if a.aiStore == nil {
		return "[]"
	}
	providers, err := a.aiStore.ListProviders(a.activeProfileID)
	if err != nil {
		return aiError(err)
	}
	b, _ := json.Marshal(providers)
	return string(b)
}

func (a *App) SaveAIProvider(providerJSON string) string {
	if a.aiStore == nil {
		return aiError(fmt.Errorf("ai store not initialized"))
	}
	var p ai.AIProvider
	if err := json.Unmarshal([]byte(providerJSON), &p); err != nil {
		return aiError(err)
	}
	if p.ID == "" {
		p.ID = newUUID()
	} else if _, err := a.aiStore.GetProvider(p.ID, a.activeProfileID); err != nil {
		return aiError(fmt.Errorf("provider %s not found", p.ID))
	}
	p.ProfileID = a.activeProfileID
	if err := a.aiStore.SaveProvider(p); err != nil {
		return aiError(err)
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func (a *App) DeleteAIProvider(id string) string {
	if a.aiStore == nil {
		return aiError(fmt.Errorf("ai store not initialized"))
	}
	if err := a.aiStore.DeleteProvider(id, a.activeProfileID); err != nil {
		return aiError(err)
	}
	return `{"ok":true}`
}

func (a *App) TestAIProvider(id string) string {
	if a.aiStore == nil {
		return aiError(fmt.Errorf("ai store not initialized"))
	}
	p, err := a.aiStore.GetProvider(id, a.activeProfileID)
	if err != nil {
		return aiError(err)
	}
	client, err := ai.NewClient(p)
	if err != nil {
		return aiError(err)
	}
	model := p.DefaultModel
	if model == "" {
		def, ok := ai.GetProviderDef(p.ProviderID)
		if ok && len(def.Models) > 0 {
			model = def.Models[0].ID
		} else {
			model = "gpt-4o-mini"
		}
	}
	_, err = client.Complete(context.Background(), ai.CompletionRequest{
		Model:     model,
		Messages:  []ai.Message{{Role: ai.RoleUser, Content: "Say ok"}},
		MaxTokens: 5,
	})
	status := "active"
	if err != nil {
		status = "error"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_ = a.aiStore.UpdateProviderStatus(id, status, now, a.activeProfileID)
	if err != nil {
		return fmt.Sprintf(`{"status":"error","error":%q}`, err.Error())
	}
	return `{"status":"active"}`
}

func (a *App) GetAIModels(providerID string) string {
	def, ok := ai.GetProviderDef(providerID)
	if !ok {
		return "[]"
	}
	b, _ := json.Marshal(def.Models)
	return string(b)
}

func (a *App) GetAIRegistry() string {
	b, _ := json.Marshal(ai.ProviderRegistry)
	return string(b)
}

func aiError(err error) string {
	return fmt.Sprintf(`{"error":%q}`, err.Error())
}

// ─────────────────────────────────────────────────────────────────────────────
// AI Chat
// ─────────────────────────────────────────────────────────────────────────────

func (a *App) StreamAIChat(workflowID, message, providerID, model string) string {
	if a.chatService == nil {
		return aiError(fmt.Errorf("chat service not initialized"))
	}
	a.chatService.SetProfileID(a.activeProfileID)
	go func() {
		err := a.chatService.StreamChat(
			context.Background(),
			workflowID, message, providerID, model,
			func(chunk ai.StreamChunk) {
				runtime.EventsEmit(a.ctx, "ai:chunk", map[string]interface{}{
					"workflowID": workflowID,
					"content":    chunk.Content,
					"done":       chunk.Done,
				})
			},
			func(name, args, result string) {
				runtime.EventsEmit(a.ctx, "ai:tool", map[string]interface{}{
					"workflowID": workflowID,
					"tool":       name,
					"args":       args,
					"result":     result,
				})
			},
		)
		if err != nil {
			runtime.EventsEmit(a.ctx, "ai:error", map[string]interface{}{
				"workflowID": workflowID,
				"error":      err.Error(),
			})
		} else {
			// Signal streaming is complete.
			runtime.EventsEmit(a.ctx, "ai:chunk", map[string]interface{}{
				"workflowID": workflowID,
				"content":    "",
				"done":       true,
			})
		}
	}()
	return `{"ok":true}`
}

func (a *App) GetAIChatHistory(workflowID string) string {
	if a.chatService == nil {
		return "[]"
	}
	a.chatService.SetProfileID(a.activeProfileID)
	msgs, err := a.chatService.GetHistory(workflowID)
	if err != nil {
		return aiError(err)
	}
	b, _ := json.Marshal(msgs)
	return string(b)
}

func (a *App) ClearAIChatHistory(workflowID string) string {
	if a.chatService == nil {
		return aiError(fmt.Errorf("chat service not initialized"))
	}
	a.chatService.SetProfileID(a.activeProfileID)
	if err := a.chatService.ClearHistory(workflowID); err != nil {
		return aiError(err)
	}
	return `{"ok":true}`
}

// GetRunLogs returns the most recent run log entries written by CLI processes.
func (a *App) GetRunLogs(limit int) []LogEntry {
	if a.db == nil || limit <= 0 {
		return nil
	}
	rows, err := a.db.Query(
		`SELECT source, level, message, created_at FROM run_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		if rows.Scan(&e.Source, &e.Level, &e.Message, &e.Time) == nil {
			out = append(out, e)
		}
	}
	// Reverse so oldest is first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ── Image Vault ──────────────────────────────────────────────────────────────

func (a *App) GetVaultImages(limit int) ([]map[string]interface{}, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	if limit <= 0 {
		limit = 200
	}
	rows, err := a.db.Query(`
		SELECT id, seq, path, filename, size_bytes, source,
		       COALESCE(workflow_id,'') as workflow_id,
		       COALESCE(execution_id,'') as execution_id,
		       COALESCE(label,'') as label, created_at
		FROM vault_images WHERE COALESCE(profile_id,'default') = ? ORDER BY seq DESC LIMIT ?`, a.activeProfileID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]interface{}
	for rows.Next() {
		var id, path, filename, source, workflowID, executionID, label, createdAt string
		var seq, sizeBytes int
		if err := rows.Scan(&id, &seq, &path, &filename, &sizeBytes, &source, &workflowID, &executionID, &label, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"id": id, "seq": seq, "path": path, "filename": filename,
			"size_bytes": sizeBytes, "source": source,
			"workflow_id": workflowID, "execution_id": executionID,
			"label": label, "created_at": createdAt,
			"url": "/vault-image/" + filename,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []map[string]interface{}{}
	}
	return out, nil
}

func (a *App) GetVaultImage(id string) (map[string]interface{}, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	var imgID, path, filename, source, workflowID, executionID, label, createdAt string
	var seq, sizeBytes int
	err := a.db.QueryRow(`
		SELECT id, seq, path, filename, size_bytes, source,
		       COALESCE(workflow_id,'') as workflow_id,
		       COALESCE(execution_id,'') as execution_id,
		       COALESCE(label,'') as label, created_at
		FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID).
		Scan(&imgID, &seq, &path, &filename, &sizeBytes, &source, &workflowID, &executionID, &label, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("vault image %q not found: %w", id, err)
	}
	return map[string]interface{}{
		"id": imgID, "seq": seq, "path": path, "filename": filename,
		"size_bytes": sizeBytes, "source": source,
		"workflow_id": workflowID, "execution_id": executionID,
		"label": label, "created_at": createdAt,
		"url": "/vault-image/" + filename,
	}, nil
}

// GetVaultImageData reads a vault image from disk and returns it as a base64
// data URL (e.g. "data:image/png;base64,..."). This is the reliable way to
// display vault images inside the Wails WebView without relying on the HTTP
// asset handler.
func (a *App) GetVaultImageData(id string) (string, error) {
	if a.db == nil {
		return "", fmt.Errorf("database not available")
	}
	var path string
	err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID).Scan(&path)
	if err != nil {
		return "", fmt.Errorf("vault image %q not found: %w", id, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("vault image file open: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("vault image file read: %w", err)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if mimeType == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (a *App) AddVaultImage(srcPath, label string) (map[string]interface{}, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	vaultCtx := vault.ContextWithProfileID(context.Background(), a.activeProfileID)
	id, err := vault.Register(vaultCtx, a.db, srcPath, "upload", "", "")
	if err != nil {
		return nil, fmt.Errorf("vault register: %w", err)
	}
	if label != "" {
		_, _ = a.db.Exec(`UPDATE vault_images SET label = ? WHERE id = ? AND COALESCE(profile_id,'default') = ?`, label, id, a.activeProfileID)
	}
	return a.GetVaultImage(id)
}

// OpenVaultFilePicker opens a native file picker and returns the selected file path (empty if cancelled).
func (a *App) OpenVaultFilePicker() string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Select Image",
		Filters: []runtime.FileFilter{
			{DisplayName: "Images", Pattern: "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.bmp"},
		},
	})
	if err != nil {
		return ""
	}
	return path
}

// SaveVaultImageToFile opens a native Save File dialog pre-filled with suggestedName,
// then copies the vault image file to the chosen path. Returns "" if the user cancels.
func (a *App) SaveVaultImageToFile(id, suggestedName string) string {
	if a.db == nil {
		return "error: database not available"
	}
	var srcPath string
	err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID).Scan(&srcPath)
	if err != nil {
		return "error: image not found"
	}
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Save Image",
		DefaultFilename: suggestedName,
		Filters: []runtime.FileFilter{
			{DisplayName: "Images", Pattern: "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.bmp;*.tif"},
		},
	})
	if err != nil || dest == "" {
		return ""
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return "error: " + err.Error()
	}
	defer src.Close()
	out, err := os.Create(dest)
	if err != nil {
		return "error: " + err.Error()
	}
	defer out.Close()
	if _, err := io.Copy(out, src); err != nil {
		return "error: " + err.Error()
	}
	return dest
}

func (a *App) UpdateVaultImageLabel(id, label string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	var nullLabel interface{} = nil
	if label != "" {
		nullLabel = label
	}
	res, err := a.db.Exec(`UPDATE vault_images SET label = ? WHERE id = ? AND COALESCE(profile_id,'default') = ?`, nullLabel, id, a.activeProfileID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("vault image %s not found", id)
	}
	return nil
}

func (a *App) DeleteVaultImage(id string) error {
	if a.db == nil {
		return fmt.Errorf("database not available")
	}
	var path string
	if err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID).Scan(&path); err != nil {
		return fmt.Errorf("vault image %q not found: %w", id, err)
	}
	if _, err := a.db.Exec(`DELETE FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.activeProfileID); err != nil {
		return fmt.Errorf("delete record: %w", err)
	}
	_ = os.Remove(path) // best-effort
	return nil
}

func (a *App) SearchVaultImages(query string) ([]map[string]interface{}, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	q := "%" + escaped + "%"
	rows, err := a.db.Query(`
		SELECT id, seq, path, filename, size_bytes, source,
		       COALESCE(workflow_id,'') as workflow_id,
		       COALESCE(execution_id,'') as execution_id,
		       COALESCE(label,'') as label, created_at
		FROM vault_images
		WHERE COALESCE(profile_id,'default') = ? AND (label LIKE ? ESCAPE '\' OR filename LIKE ? ESCAPE '\' OR source LIKE ? ESCAPE '\' OR workflow_id LIKE ? ESCAPE '\')
		ORDER BY seq DESC LIMIT 100`, a.activeProfileID, q, q, q, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]interface{}
	for rows.Next() {
		var id, path, filename, source, workflowID, executionID, label, createdAt string
		var seq, sizeBytes int
		if err := rows.Scan(&id, &seq, &path, &filename, &sizeBytes, &source, &workflowID, &executionID, &label, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"id": id, "seq": seq, "path": path, "filename": filename,
			"size_bytes": sizeBytes, "source": source,
			"workflow_id": workflowID, "execution_id": executionID,
			"label": label, "created_at": createdAt,
			"url": "/vault-image/" + filename,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []map[string]interface{}{}
	}
	return out, nil
}

func (a *App) GetVaultStats() (map[string]interface{}, error) {
	if a.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	var count int
	var totalBytes int64
	err := a.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size_bytes),0) FROM vault_images WHERE COALESCE(profile_id,'default') = ?`, a.activeProfileID).
		Scan(&count, &totalBytes)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"count": count, "total_bytes": totalBytes}, nil
}
