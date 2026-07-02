package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/data"
	"github.com/spf13/cobra"
)

// claudeInitMarker is written after a successful first-run Claude check so we
// don't repeat the check on every subsequent invocation.
const claudeInitMarker = ".claude_init"

// claudeSkillName is the skill file distributed with monoagent.
const claudeSkillName = "action-template-generator.md"

// newInitCmd returns the `monoagent init` command.
func newInitCmd() *cobra.Command {
	var claudeFlag bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize monoagent integrations",
		Long: `Set up monoagent integrations.

  --claude    Install the action-template-generator Claude Code skill so
              Claude automatically knows how to crawl new websites using
              monoagent's browser automation.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !claudeFlag {
				return cmd.Help()
			}
			return installClaudeSkill(true)
		},
	}

	cmd.Flags().BoolVar(&claudeFlag, "claude", false, "Install Claude Code skill for crawling automation")
	return cmd
}

// installClaudeSkill copies the embedded skill file to ~/.claude/skills/.
// If verbose is true, it prints status messages.
func installClaudeSkill(verbose bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}

	content, err := data.SkillsFS.ReadFile("skills/" + claudeSkillName)
	if err != nil {
		return fmt.Errorf("read embedded skill: %w", err)
	}

	dest := filepath.Join(skillsDir, claudeSkillName)
	if err := os.WriteFile(dest, content, 0o644); err != nil {
		return fmt.Errorf("write skill to %s: %w", dest, err)
	}

	// Write the marker so first-run check doesn't repeat.
	monoesDir := filepath.Join(home, ".monoes")
	_ = os.MkdirAll(monoesDir, 0o755)
	_ = os.WriteFile(filepath.Join(monoesDir, claudeInitMarker), []byte("1"), 0o644)

	if verbose {
		fmt.Printf("Claude skill installed: %s\n", dest)
		fmt.Printf("\nIn Claude Code, run /action-template-generator to generate crawling templates.\n")
		fmt.Printf("Or use: monoagent crawl <url>\n")
	}
	return nil
}

// runClaudeFirstRunCheck installs the Claude skill on the first monoagent
// invocation if Claude Code is detected (~/.claude/ exists) and the skill
// hasn't been installed yet. Errors are silently ignored — this is best-effort.
func runClaudeFirstRunCheck() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	marker := filepath.Join(home, ".monoes", claudeInitMarker)
	if _, err := os.Stat(marker); err == nil {
		return // already checked
	}

	// Detect Claude Code: ~/.claude/ directory must exist.
	claudeDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		// Claude Code not installed — write marker so we don't re-check.
		monoesDir := filepath.Join(home, ".monoes")
		_ = os.MkdirAll(monoesDir, 0o755)
		_ = os.WriteFile(marker, []byte("0"), 0o644)
		return
	}

	// Skill already installed?
	skillDest := filepath.Join(claudeDir, "skills", claudeSkillName)
	if _, err := os.Stat(skillDest); err == nil {
		_ = os.WriteFile(marker, []byte("1"), 0o644)
		return
	}

	// Claude Code present, skill missing — auto-install.
	if err := installClaudeSkill(false); err != nil {
		return
	}

	fmt.Fprintf(os.Stderr, "[monoagent] Claude Code detected — crawling skill installed.\n")
	fmt.Fprintf(os.Stderr, "[monoagent] Use /action-template-generator or: monoagent crawl <url>\n")
}
