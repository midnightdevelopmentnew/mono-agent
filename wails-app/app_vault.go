package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"monoagent/internal/secrets"
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
		FROM vault_images WHERE COALESCE(profile_id,'default') = ? ORDER BY seq DESC LIMIT ?`, a.getActiveProfileID(), limit)
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
		FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.getActiveProfileID()).
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

func (a *App) ListSecrets() ([]secrets.Entry, error) {
	entries, err := secrets.List(context.Background(), a.db, a.getActiveProfileID())
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []secrets.Entry{}
	}
	return entries, nil
}

func (a *App) AddSecret(kind, name, value, username, url, notes string) (string, error) {
	return secrets.Add(context.Background(), a.db, a.getActiveProfileID(), kind, name, value, username, url, notes)
}

// RevealSecret returns the plaintext value for id. This is the GUI's only
// decrypt entrypoint, calling the identical secrets.DecryptEntry function
// the CLI's `secret reveal --reveal` command calls.
func (a *App) RevealSecret(id string) (string, error) {
	return secrets.DecryptEntry(context.Background(), a.db, a.getActiveProfileID(), id)
}

func (a *App) DeleteSecret(id string) error {
	return secrets.Delete(context.Background(), a.db, a.getActiveProfileID(), id)
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
	err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.getActiveProfileID()).Scan(&path)
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
		_, _ = a.db.Exec(`UPDATE vault_images SET label = ? WHERE id = ? AND COALESCE(profile_id,'default') = ?`, label, id, a.getActiveProfileID())
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
	err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.getActiveProfileID()).Scan(&srcPath)
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
	res, err := a.db.Exec(`UPDATE vault_images SET label = ? WHERE id = ? AND COALESCE(profile_id,'default') = ?`, nullLabel, id, a.getActiveProfileID())
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
	if err := a.db.QueryRow(`SELECT path FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.getActiveProfileID()).Scan(&path); err != nil {
		return fmt.Errorf("vault image %q not found: %w", id, err)
	}
	if _, err := a.db.Exec(`DELETE FROM vault_images WHERE id = ? AND COALESCE(profile_id,'default') = ?`, id, a.getActiveProfileID()); err != nil {
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
	err := a.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size_bytes),0) FROM vault_images WHERE COALESCE(profile_id,'default') = ?`, a.getActiveProfileID()).
		Scan(&count, &totalBytes)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"count": count, "total_bytes": totalBytes}, nil
}
