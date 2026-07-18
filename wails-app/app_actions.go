package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

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
