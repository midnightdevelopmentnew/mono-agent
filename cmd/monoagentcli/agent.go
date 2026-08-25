package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"monoagent/internal/monomind"
)

// newAgentCmd exposes the local agent-runner surface: thin proxies over
// monomind's Agent Exec Protocol commands. mono-agent never learns
// agent-CLI wire formats — discovery, probing, and smoke tests all live in
// monomind (`agent scan` / `agent test`, protocol §6).
func newAgentCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Local AI agent runtimes (via the monomind engine)",
		Long: "Detect and smoke-test the AI agent CLIs installed on this machine — claude, codex, " +
			"kimi, qwen, grok, crush, copilot, pi, and more — through the monomind engine. " +
			"monomind is the AI engine mono-agent delegates every chat and AI node to; if it is " +
			"missing, these commands print the install hint.",
	}
	cmd.AddCommand(
		newAgentScanCmd(cfg),
		newAgentTestCmd(cfg),
	)
	return cmd
}

func newAgentScanCmd(cfg *globalConfig) *cobra.Command {
	var installedOnly bool
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Detect installed AI agent runtimes",
		Example: `  monoagentcli agent scan
  monoagentcli agent scan --installed --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, _, err := monomind.Ensure(cmd.Context()); err != nil {
				return err
			}
			res, err := monomind.Scan(cmd.Context())
			if err != nil {
				return err
			}
			agents := res.Agents
			if installedOnly {
				agents = res.Installed()
			}
			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]interface{}{"v": res.V, "agents": agents})
			}
			if len(agents) == 0 {
				fmt.Println("No agent runtimes detected. Install monomind-visible agents (see `agent scan` without --installed).")
				return nil
			}
			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"Runtime", "Installed", "Version", "Binary"})
			table.SetBorder(false)
			table.SetAutoWrapText(false)
			for _, a := range agents {
				binary, version := "—", "—"
				if a.Binary != nil && *a.Binary != "" {
					binary = *a.Binary
				}
				if a.Version != nil && *a.Version != "" {
					version = *a.Version
				}
				installed := "no"
				if a.Installed {
					installed = "yes"
				}
				table.Append([]string{a.ID, installed, version, binary})
			}
			table.Render()
			if !installedOnly {
				fmt.Fprintln(os.Stderr, "\nNot installed:")
				for _, a := range res.Agents {
					if !a.Installed {
						fmt.Fprintf(os.Stderr, "  %s: %s\n", a.ID, a.InstallHint)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&installedOnly, "installed", false, "Only list installed runtimes")
	return cmd
}

func newAgentTestCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "test <runtime>",
		Short:   "Smoke-test one agent runtime (also verifies auth)",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagentcli agent test codex`,
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime := args[0]
			ctx := cmd.Context()
			bin, _, err := monomind.Ensure(ctx)
			if err != nil {
				return err
			}
			timeoutRaw, _ := cmd.Flags().GetString("timeout")
			timeout, err := parseDurationFlag(timeoutRaw)
			if err != nil {
				return err
			}
			res, err := monomind.Exec(ctx, monomind.ExecOptions{
				Bin:     bin,
				Runtime: runtime,
				Prompt:  "Reply with the single word: ok",
				Timeout: timeout,
			}, func(ev monomind.Event) {
				switch ev.Type {
				case monomind.EventAssistant:
					fmt.Fprintln(os.Stderr, ev.Text)
				case monomind.EventError:
					fmt.Fprintf(os.Stderr, "error: %s: %s\n", ev.Code, ev.ErrMessage)
				case monomind.EventDone:
					fmt.Fprintf(os.Stderr, "exit: %d\n", ev.ExitCode)
				}
			})
			if err != nil {
				return err
			}
			if res.Err != nil {
				return res.Err
			}
			fmt.Fprintf(os.Stderr, "\n%s: OK (session %s)\n", runtime, res.SessionID)
			return nil
		},
	}
	cmd.Flags().String("timeout", "90s", "Overall timeout")
	return cmd
}
