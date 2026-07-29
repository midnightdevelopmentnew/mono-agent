package main

import (
	"fmt"
	"os"
	"path/filepath"

	"monoagent/data"
	"github.com/spf13/cobra"
)

// claudeInitMarker is written after a successful first-run Claude check so we
// don't repeat the check on every subsequent invocation.
const claudeInitMarker = ".claude_init"

// claudeSkillNames are the skill files distributed with monoagent. They are
// installed into ~/.claude/skills/ so that a Claude Code session in ANY project
// — not just this repo — knows monoagent exists and how to drive it.
var claudeSkillNames = []string{
	"action-template-generator.md",
	"monoagent-workflows.md",
}

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

	for _, name := range claudeSkillNames {
		content, err := data.SkillsFS.ReadFile("skills/" + name)
		if err != nil {
			return fmt.Errorf("read embedded skill %s: %w", name, err)
		}

		dest := filepath.Join(skillsDir, name)
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			return fmt.Errorf("write skill to %s: %w", dest, err)
		}
		if verbose {
			fmt.Printf("Claude skill installed: %s\n", dest)
		}
	}

	// Write the marker so first-run check doesn't repeat.
	monoagentDir := filepath.Join(home, ".monoagent")
	_ = os.MkdirAll(monoagentDir, 0o755)
	_ = os.WriteFile(filepath.Join(monoagentDir, claudeInitMarker), []byte("1"), 0o644)

	if verbose {
		fmt.Printf("\nIn Claude Code, run /action-template-generator to generate crawling templates.\n")
		fmt.Printf("Claude now also knows how to run monoagent workflows and templates from any project:\n")
		fmt.Printf("  monoagentcli workflow templates list\n")
	}
	return nil
}

// runClaudeFirstRunCheck installs monoagent's Claude Code skills if Claude Code
// is detected (~/.claude/ exists) and any of them is missing. It checks the
// skills themselves rather than trusting the first-run marker, so a monoagent
// version that ships a NEW skill delivers it to existing installs too. Errors
// are silently ignored — this is best-effort.
func runClaudeFirstRunCheck() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	// Detect Claude Code: ~/.claude/ directory must exist.
	claudeDir := filepath.Join(home, ".claude")
	if _, err := os.Stat(claudeDir); os.IsNotExist(err) {
		// Claude Code not installed — write marker so we don't re-check.
		marker := filepath.Join(home, ".monoagent", claudeInitMarker)
		if _, err := os.Stat(marker); err != nil {
			monoagentDir := filepath.Join(home, ".monoagent")
			_ = os.MkdirAll(monoagentDir, 0o755)
			_ = os.WriteFile(marker, []byte("0"), 0o644)
		}
		return
	}

	allInstalled := true
	for _, name := range claudeSkillNames {
		if _, err := os.Stat(filepath.Join(claudeDir, "skills", name)); err != nil {
			allInstalled = false
			break
		}
	}
	if allInstalled {
		return
	}

	if err := installClaudeSkill(false); err != nil {
		return
	}

	fmt.Fprintf(os.Stderr, "[monoagent] Claude Code detected — monoagent skills installed.\n")
	fmt.Fprintf(os.Stderr, "[monoagent] Claude can now run: monoagentcli workflow templates list\n")
}
