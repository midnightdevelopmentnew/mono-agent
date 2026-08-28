package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// newBridgeCmd holds the extension bridge open for the active profile and does
// nothing else.
//
// `daemon` also holds the bridge, but it restores every active workflow's
// triggers across ALL profiles (see engine.RestoreActiveWorkflows), so running
// a second daemon to serve a second profile's browser would re-run the first
// profile's workflows in the wrong browser — duplicating scheduled work and
// executing it against a session that isn't logged in. This command exists so
// a profile can own a persistent bridge without that side effect: it is the
// right way to keep a secondary automation browser attached.
func newBridgeCmd(cfg *globalConfig) *cobra.Command {
	var wait time.Duration
	c := &cobra.Command{
		Use:   "bridge",
		Short: "Hold the Chrome extension bridge open for this profile (no workflow engine)",
		Long: "Starts the extension bridge server on this profile's port and blocks until interrupted " +
			"(Ctrl+C), without starting the workflow engine or restoring any triggers. Use this to keep " +
			"a secondary profile's automation browser attached — `daemon` would additionally restore " +
			"every active workflow across all profiles, which would run them in this browser too.",
		Example: "  monoagentcli --profile linkedin-management bridge",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolves cfg.ProfileID, which is what selects the bridge port.
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			db.Close()

			logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).
				With().Timestamp().Str("component", "bridge").Logger()
			if !cfg.Verbose {
				logger = logger.Level(zerolog.WarnLevel)
			}

			fmt.Fprintf(os.Stdout, "Profile:  %s\n", cfg.ProfileID)
			fmt.Fprintf(os.Stdout, "Bridge:   %s\n", extensionServerBind)
			fmt.Fprintf(os.Stdout, "Extension WebSocket URL for this profile's Chrome:\n  %s\n\n",
				ExtensionWSURLForProfile(cfg.ProfileID))

			bridge := setupExtensionBridge(logger, wait)
			if bridge.IsConnected() {
				fmt.Fprintln(os.Stdout, "Extension connected. Holding the bridge open. Press Ctrl+C to stop.")
			} else {
				fmt.Fprintln(os.Stdout, "No extension connected yet — holding the port open so it can attach.")
				fmt.Fprintln(os.Stdout, "In that profile's Chrome, open the MonoAgent Bridge popup, set the URL")
				fmt.Fprintln(os.Stdout, "above and press Save to reconnect immediately. Press Ctrl+C to stop.")
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			<-ctx.Done()
			fmt.Fprintln(os.Stdout, "\nShutting down bridge...")
			return nil
		},
	}
	c.Flags().DurationVar(&wait, "wait", 30*time.Second, "how long to wait for the extension to connect before holding regardless")
	return c
}
