package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/spf13/cobra"
)

// userActionsInstallDir returns ~/.monoes/actions, creating it if needed.
func userActionsInstallDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".monoes", "actions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create actions dir: %w", err)
	}
	return dir, nil
}

// userCaptureDir returns ~/.monoes/captures, creating it if needed.
func userCaptureDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".monoes", "captures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create captures dir: %w", err)
	}
	return dir, nil
}

func newActionTemplateCmd(_ *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage action templates for new platforms",
		Long: `Capture HTML from any URL, generate an ActionDef template via Claude Code skill,
then install it so 'monoagent node run <platform>.<action_type>' works.

Workflow:
  1. monoagent action template capture <url>   # saves rendered HTML to a file
  2. /action-template-generator                # Claude Code skill: reads HTML, writes template JSON
  3. monoagent action template install <file>  # registers template with monoagent`,
	}

	cmd.AddCommand(
		newActionTemplateCaptureCmd(),
		newActionTemplateInstallCmd(),
		newActionTemplateListCmd(),
	)

	return cmd
}

func newActionTemplateCaptureCmd() *cobra.Command {
	var headless bool
	var waitSecs int
	var outFile string

	cmd := &cobra.Command{
		Use:   "capture <url>",
		Short: "Capture rendered HTML from a URL using a real browser",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagent action template capture https://www.instagram.com/someuser/
  monoagent action template capture https://example.com --headless --wait 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rawURL := args[0]

			parsed, err := url.Parse(rawURL)
			if err != nil {
				return fmt.Errorf("invalid URL %q: %w", rawURL, err)
			}

			domain := strings.ReplaceAll(parsed.Hostname(), ".", "_")
			timestamp := time.Now().Format("20060102_150405")

			captureDir, err := userCaptureDir()
			if err != nil {
				return err
			}

			dest := outFile
			if dest == "" {
				dest = filepath.Join(captureDir, fmt.Sprintf("%s_%s.html", domain, timestamp))
			}

			fmt.Fprintf(os.Stderr, "Launching browser...\n")
			l := launcher.New().
				Headless(headless).
				Set("disable-blink-features", "AutomationControlled")

			launchURL, err := l.Launch()
			if err != nil {
				return fmt.Errorf("launch browser: %w", err)
			}

			browser := rod.New().ControlURL(launchURL)
			if err := browser.Connect(); err != nil {
				return fmt.Errorf("connect to browser: %w", err)
			}
			defer browser.Close()

			page, err := browser.Page(proto.TargetCreateTarget{URL: rawURL})
			if err != nil {
				return fmt.Errorf("open page: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Waiting %ds for JS rendering...\n", waitSecs)
			time.Sleep(time.Duration(waitSecs) * time.Second)

			res, err := page.Eval("() => document.documentElement.outerHTML")
			if err != nil {
				return fmt.Errorf("capture HTML: %w", err)
			}

			html := res.Value.String()
			if err := os.WriteFile(dest, []byte(html), 0o644); err != nil {
				return fmt.Errorf("write HTML to %s: %w", dest, err)
			}

			fmt.Printf("Captured: %s\n", dest)
			fmt.Printf("\nNext: run the Claude Code skill to analyze and generate a template:\n")
			fmt.Printf("  /action-template-generator\n")
			fmt.Printf("Point the skill at: %s\n", dest)
			return nil
		},
	}

	cmd.Flags().BoolVar(&headless, "headless", false, "Run browser headlessly (no window)")
	cmd.Flags().IntVar(&waitSecs, "wait", 8, "Seconds to wait for JS rendering before capturing")
	cmd.Flags().StringVar(&outFile, "out", "", "Output file path (default: ~/.monoes/captures/<domain>_<ts>.html)")

	return cmd
}

func newActionTemplateInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <file>",
		Short: "Install an ActionDef JSON template so 'node run <platform>.<type>' works",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagent action template install ~/Downloads/example_scrape_profile.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			srcPath := args[0]

			fileData, err := os.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("read file %s: %w", srcPath, err)
			}

			var def action.ActionDef
			if err := json.Unmarshal(fileData, &def); err != nil {
				return fmt.Errorf("invalid ActionDef JSON: %w", err)
			}
			if def.Platform == "" {
				return fmt.Errorf("ActionDef missing required field: platform")
			}
			if def.ActionType == "" {
				return fmt.Errorf("ActionDef missing required field: actionType")
			}
			if len(def.Steps) == 0 {
				return fmt.Errorf("ActionDef has no steps")
			}

			platform := strings.ToLower(def.Platform)
			actionType := strings.ToLower(def.ActionType)

			// Prevent directory traversal in derived paths.
			if strings.ContainsAny(platform, "/..") || strings.ContainsAny(actionType, "/..") {
				return fmt.Errorf("platform or actionType contains invalid characters")
			}

			installDir, err := userActionsInstallDir()
			if err != nil {
				return err
			}

			platformDir := filepath.Join(installDir, platform)
			if err := os.MkdirAll(platformDir, 0o755); err != nil {
				return fmt.Errorf("create platform dir: %w", err)
			}

			dest := filepath.Join(platformDir, actionType+".json")
			if err := os.WriteFile(dest, fileData, 0o644); err != nil {
				return fmt.Errorf("write template: %w", err)
			}

			// Invalidate loader cache so the next node run picks up the new template.
			action.GetLoader().Invalidate(platform, actionType)

			fmt.Printf("Installed: %s.%s -> %s\n", platform, actionType, dest)
			fmt.Printf("Run it: monoagent node run %s.%s\n", platform, actionType)
			return nil
		},
	}
}

func newActionTemplateListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List user-installed action templates",
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home dir: %w", err)
			}
			actionsDir := filepath.Join(home, ".monoes", "actions")

			platforms, err := os.ReadDir(actionsDir)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("No user-installed templates. Install with: monoagent action template install <file>")
					return nil
				}
				return fmt.Errorf("read actions dir: %w", err)
			}

			type entry struct {
				nodeType string
				file     string
			}
			var entries []entry

			for _, pd := range platforms {
				if !pd.IsDir() {
					continue
				}
				files, _ := os.ReadDir(filepath.Join(actionsDir, pd.Name()))
				for _, f := range files {
					if !f.IsDir() && strings.HasSuffix(f.Name(), ".json") {
						name := strings.TrimSuffix(f.Name(), ".json")
						entries = append(entries, entry{
							nodeType: fmt.Sprintf("%s.%s", pd.Name(), name),
							file:     filepath.Join(actionsDir, pd.Name(), f.Name()),
						})
					}
				}
			}

			if len(entries) == 0 {
				fmt.Println("No user-installed templates found.")
				return nil
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NODE TYPE\tFILE")
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\n", e.nodeType, e.file)
			}
			tw.Flush()
			return nil
		},
	}
}
