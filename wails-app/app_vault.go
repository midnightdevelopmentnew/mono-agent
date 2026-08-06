package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"monoagent/internal/vault"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

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
		FROM vault_images WHERE profile_id = ? ORDER BY seq DESC LIMIT ?`, a.getActiveProfileID(), limit)
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
		FROM vault_images WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID()).
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

// ── Secrets Vault ────────────────────────────────────────────────────────────

// Every method below shells out to `monoagentcli secret ...` instead of
// calling internal/secrets directly — the CLI is the single implementation
// surface for vault operations. This file intentionally does not import
// monoagent/internal/secrets.

// VaultEntry mirrors `secret list --json`'s per-entry shape without
// importing internal/secrets — the CLI's --json output is the contract,
// not its Go types.
type VaultEntry struct {
	ID         string `json:"id"`
	ProfileID  string `json:"profile_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Username   string `json:"username,omitempty"`
	URL        string `json:"url,omitempty"`
	FieldCount int    `json:"field_count"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// VaultFieldsAndNotes mirrors the CLI reveal command's --json output shape.
type VaultFieldsAndNotes struct {
	Fields map[string]string `json:"fields"`
	Notes  string            `json:"notes"`
}

// VaultExportResult mirrors the CLI export command's --json output shape,
// plus a GUI-only Cancelled flag set when the user dismisses the save
// dialog. Skipped counts entries that failed to decrypt (e.g. an
// unmigrated legacy row) and were left out of the export — see
// secrets.Export.
type VaultExportResult struct {
	Path       string `json:"path"`
	Passphrase string `json:"passphrase"`
	Exported   int    `json:"exported"`
	Skipped    int    `json:"skipped"`
	Cancelled  bool   `json:"cancelled,omitempty"`
}

// VaultImportResult mirrors the CLI import command's --json output shape.
type VaultImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"`
}

// runVaultCLI runs `monoagentcli --profile <active> --json secret <args...>`,
// optionally piping stdin, and JSON-unmarshals stdout into result (skipped
// if result is nil). On a non-zero exit, the subprocess's stderr becomes
// the returned error — mirrors ExportData's existing exec.ExitError
// handling.
func (a *App) runVaultCLI(stdin string, result interface{}, args ...string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}
	fullArgs := append([]string{"--profile", a.getActiveProfileID(), "--json", "secret"}, args...)
	cmd := exec.CommandContext(a.ctx, cliBin, fullArgs...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("unexpected vault command output: %w", err)
	}
	return nil
}

func (a *App) ListSecrets() ([]VaultEntry, error) {
	var entries []VaultEntry
	if err := a.runVaultCLI("", &entries, "list"); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []VaultEntry{}
	}
	return entries, nil
}

func (a *App) AddSecret(kind, name, username, url, notes string, fields map[string]string) (string, error) {
	args := []string{"add", "--kind", kind, "--name", name}
	if username != "" {
		args = append(args, "--username", username)
	}
	if url != "" {
		args = append(args, "--url", url)
	}
	if notes != "" {
		args = append(args, "--notes", notes)
	}
	for k, v := range fields {
		args = append(args, "--field", k+"="+v)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := a.runVaultCLI("", &result, args...); err != nil {
		return "", err
	}
	return result.ID, nil
}

func (a *App) GetSecretFields(name string) (*VaultFieldsAndNotes, error) {
	var result VaultFieldsAndNotes
	if err := a.runVaultCLI("", &result, "reveal", name, "--reveal"); err != nil {
		return nil, err
	}
	return &result, nil
}

func (a *App) UpdateSecret(name, newName, username, url, notes string, fields map[string]string) error {
	args := []string{"update", name, "--name", newName, "--username", username, "--url", url, "--notes", notes}
	for k, v := range fields {
		args = append(args, "--field", k+"="+v)
	}
	return a.runVaultCLI("", nil, args...)
}

func (a *App) DeleteSecret(name string) error {
	return a.runVaultCLI("", nil, "rm", name)
}

// ExportVaultAll prompts for a save location, then exports the active
// profile's vault there. The returned passphrase is shown exactly once by
// the caller — it is never persisted.
func (a *App) ExportVaultAll() (*VaultExportResult, error) {
	dest, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Export Vault",
		DefaultFilename: "vault-export.json.enc",
		// Single extension only: Wails' macOS dialog resolves each ";"-separated
		// Pattern entry via UTType typeWithFilenameExtension:, which returns nil
		// for a compound extension like "json.enc" (embedded dot) — and Wails
		// inserts that nil into an NSMutableArray unguarded, crashing the app
		// (NSInvalidArgumentException: object cannot be nil). "*.enc" alone still
		// matches "vault-export.json.enc" since macOS resolves UTType from the
		// final extension.
		Filters: []runtime.FileFilter{
			{DisplayName: "Vault export", Pattern: "*.enc"},
		},
	})
	if err != nil {
		return nil, err
	}
	if dest == "" {
		return &VaultExportResult{Cancelled: true}, nil
	}
	var result VaultExportResult
	if err := a.runVaultCLI("", &result, "export", "--output", dest); err != nil {
		return nil, err
	}
	return &result, nil
}

// OpenVaultImportFilePicker opens a native file picker for a vault export
// file and returns the selected path (empty if cancelled).
func (a *App) OpenVaultImportFilePicker() string {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Import Vault",
		// Single extension only — see the matching comment on ExportVaultAll's
		// SaveFileDialog Filters above; OpenFileDialog has the identical
		// unguarded nil-UTType crash for compound extensions.
		Filters: []runtime.FileFilter{
			{DisplayName: "Vault export", Pattern: "*.enc"},
		},
	})
	if err != nil {
		return ""
	}
	return path
}

// ImportVaultAll decrypts path with passphrase (piped to the CLI's stdin,
// per the design spec — never a flag) and imports every entry into the
// active profile.
func (a *App) ImportVaultAll(path, passphrase string) (*VaultImportResult, error) {
	var result VaultImportResult
	if err := a.runVaultCLI(passphrase+"\n", &result, "import", path); err != nil {
		return nil, err
	}
	return &result, nil
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
	err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID()).Scan(&path)
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
	vaultCtx := vault.ContextWithProfileID(context.Background(), a.getActiveProfileID())
	id, err := vault.Register(vaultCtx, a.db, srcPath, "upload", "", "")
	if err != nil {
		return nil, fmt.Errorf("vault register: %w", err)
	}
	if label != "" {
		_, _ = a.db.Exec(`UPDATE vault_images SET label = ? WHERE id = ? AND profile_id = ?`, label, id, a.getActiveProfileID())
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
	err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID()).Scan(&srcPath)
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
	res, err := a.db.Exec(`UPDATE vault_images SET label = ? WHERE id = ? AND profile_id = ?`, nullLabel, id, a.getActiveProfileID())
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
	if err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID()).Scan(&path); err != nil {
		return fmt.Errorf("vault image %q not found: %w", id, err)
	}
	if _, err := a.db.Exec(`DELETE FROM vault_images WHERE id = ? AND profile_id = ?`, id, a.getActiveProfileID()); err != nil {
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
		WHERE profile_id = ? AND (label LIKE ? ESCAPE '\' OR filename LIKE ? ESCAPE '\' OR source LIKE ? ESCAPE '\' OR workflow_id LIKE ? ESCAPE '\')
		ORDER BY seq DESC LIMIT 100`, a.getActiveProfileID(), q, q, q, q)
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
	err := a.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size_bytes),0) FROM vault_images WHERE profile_id = ?`, a.getActiveProfileID()).
		Scan(&count, &totalBytes)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"count": count, "total_bytes": totalBytes}, nil
}
