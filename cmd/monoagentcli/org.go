package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"monoagent/internal/monomind"
)

// defaultOrgProjectRoot is the project root org state resolves under
// (`<root>/.monomind/orgs/`, protocol §7.1) when --project is not given.
// mono-agent has no per-workflow project directory of its own, so orgs live
// under the app's home data dir by default, same as configs/output.
const defaultOrgProjectRoot = "~/.monoagent"

// newOrgCmd exposes monomind's org observe/action surface: thin proxies over
// `monomind org <sub> [<name>] --format json` (protocol §7). Output is
// always JSON — these commands exist for monoagentcli's own Wails bindings
// and for scripting, not interactive table browsing (mirrors the doctrine
// established by `chat`: the CLI's machine output IS the UI contract).
func newOrgCmd(cfg *globalConfig) *cobra.Command {
	var projectRoot string
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Local agent organizations (via the monomind engine)",
		Long: "Observe and act on monomind-managed agent organizations — status, logs, costs, " +
			"pending questions/gates, and a live event tail. Every subcommand prints JSON to " +
			"stdout; there is no human table mode.",
	}
	cmd.PersistentFlags().StringVar(&projectRoot, "project", defaultOrgProjectRoot,
		"Project root orgs resolve under (<root>/.monomind/orgs/)")
	root := func() string { return expandPath(projectRoot) }

	cmd.AddCommand(
		newOrgListCmd(root),
		newOrgStatusCmd(root),
		newOrgLogsCmd(root),
		newOrgReportCmd(root),
		newOrgCostsCmd(root),
		newOrgFlowCmd(root),
		newOrgQuestionsCmd(root),
		newOrgGatesCmd(root),
		newOrgDecisionsCmd(root),
		newOrgMemoryCmd(root),
		newOrgAnswerCmd(root),
		newOrgApproveCmd(root),
		newOrgDenyCmd(root),
		newOrgGateApproveCmd(root),
		newOrgGateRejectCmd(root),
		newOrgEventsCmd(root),
	)
	return cmd
}

// printOrgJSON writes a raw JSON payload to stdout with a trailing newline.
func printOrgJSON(payload []byte) error {
	_, err := fmt.Println(string(payload))
	return err
}

func newOrgListCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all orgs in the project",
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgList(cmd.Context(), root())
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgStatusCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "status [name]",
		Short: "Show one org's status, or every org's when name is omitted",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			out, err := monomind.OrgStatus(cmd.Context(), root(), name)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgLogsCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "logs <name>",
		Short: "Show the org's bus event log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgLogs(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgReportCmd(root func() string) *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:   "report <name>",
		Short: "Show the org's run report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgReport(cmd.Context(), root(), args[0], all)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().BoolVar(&all, "all", false, "Report on every run instead of just the latest")
	return c
}

func newOrgCostsCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "costs <name>",
		Short: "Show per-role token/cost totals",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgCosts(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgFlowCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "flow <name>",
		Short: "Show the org's role communication graph",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgFlow(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgQuestionsCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "questions <name>",
		Short: "List pending human-input questions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgQuestions(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgGatesCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "gates <name>",
		Short: "List pending decision gates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgGates(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgDecisionsCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "decisions <name>",
		Short: "Show the org's decision trace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgDecisions(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgMemoryCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "memory <name>",
		Short: "Show org memory statistics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgMemoryStats(cmd.Context(), root(), args[0])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgAnswerCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "answer <name> <questionId> <answer...>",
		Short: "Answer a pending human-input question",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			answer := joinRemainder(args[2:])
			out, err := monomind.OrgAnswer(cmd.Context(), root(), args[0], args[1], answer)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgApproveCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "approve <name> <role> <action>",
		Short: "Approve a pending tool-approval request",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgApprove(cmd.Context(), root(), args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgDenyCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "deny <name> <role> <action>",
		Short: "Deny a pending tool-approval request",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := monomind.OrgDeny(cmd.Context(), root(), args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgGateApproveCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "gate-approve <name> <gateId> [resolution...]",
		Short: "Approve a decision gate",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolution := joinRemainder(args[2:])
			out, err := monomind.OrgGateApprove(cmd.Context(), root(), args[0], args[1], resolution)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgGateRejectCmd(root func() string) *cobra.Command {
	return &cobra.Command{
		Use:   "gate-reject <name> <gateId> [resolution...]",
		Short: "Reject a decision gate",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolution := joinRemainder(args[2:])
			out, err := monomind.OrgGateReject(cmd.Context(), root(), args[0], args[1], resolution)
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
}

func newOrgEventsCmd(root func() string) *cobra.Command {
	var run, since string
	var follow bool
	c := &cobra.Command{
		Use:   "events <name>",
		Short: "Stream the org's bus event log as NDJSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return monomind.OrgEvents(cmd.Context(), root(), args[0], monomind.OrgEventsOptions{
				Run:    run,
				Follow: follow,
				Since:  since,
			}, func(line []byte) {
				os.Stdout.Write(line)
				os.Stdout.Write([]byte("\n"))
			})
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: current run)")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "Keep streaming as new events arrive")
	c.Flags().StringVar(&since, "since", "", "Replay from a cursor (event id or ISO timestamp)")
	return c
}

// joinRemainder joins trailing positional args with a space, mirroring
// monomind's own answer/gate-approve/gate-reject argument handling.
func joinRemainder(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
