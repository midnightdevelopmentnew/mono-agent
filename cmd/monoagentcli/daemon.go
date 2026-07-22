package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// newDaemonCmd runs the workflow engine as a long-running foreground process,
// keeping every active workflow's triggers (schedule, webhook) alive across
// all profiles until interrupted. Without this, `workflow activate` only
// registers a trigger for the lifetime of that one CLI invocation — the
// engine deactivates all triggers on Stop(), which happens as soon as the
// activating command exits.
func newDaemonCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the workflow engine in the foreground, keeping scheduled/webhook triggers alive",
		Long: "Starts the workflow engine and restores every active workflow's triggers (across all " +
			"profiles), then blocks until interrupted (Ctrl+C). This is what actually makes " +
			"`workflow activate` schedules fire over time — without a daemon running, an activated " +
			"workflow's cron/webhook triggers only live for the instant the activating command runs.",
		Example: "  monoagentcli daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			engine, closeBrowsers, err := buildEngine(cfg, true)
			if err != nil {
				return fmt.Errorf("build engine: %w", err)
			}
			defer closeBrowsers()

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if err := engine.Start(ctx); err != nil {
				return fmt.Errorf("start engine: %w", err)
			}
			defer engine.Stop() //nolint:errcheck

			if err := engine.RestoreActiveWorkflows(ctx); err != nil {
				return fmt.Errorf("restore active workflows: %w", err)
			}

			fmt.Fprintln(os.Stdout, "Daemon running. Active workflows' triggers are live. Press Ctrl+C to stop.")
			<-ctx.Done()
			fmt.Fprintln(os.Stdout, "Shutting down...")
			return nil
		},
	}
}
