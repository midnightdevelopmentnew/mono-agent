package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/spf13/cobra"
)

func newCrawlCmd(_ *globalConfig) *cobra.Command {
	var headless bool
	var waitSecs int

	cmd := &cobra.Command{
		Use:   "crawl <url>",
		Short: "Capture a website for AI-assisted crawling automation",
		Long: `Render a URL in a real browser and capture its HTML for Claude Code analysis.

Claude will then generate a monoagent ActionDef template that you can
install and run as a reusable automation node — no API key required.

Steps (automated by Claude when you use /action-template-generator):
  1. monoagent crawl <url>           ← you are here
  2. /action-template-generator      ← Claude Code skill
  3. monoagent action template install <generated-file>
  4. monoagent node run <platform>.<action_type>

For the full guide:  monoagent ref crawling`,
		Args: cobra.ExactArgs(1),
		Example: `  monoagent crawl https://www.producthunt.com/posts/new-product
  monoagent crawl https://github.com/someuser --headless
  monoagent crawl https://somesite.com/profile --wait 12`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rawURL := args[0]

			parsed, err := url.Parse(rawURL)
			if err != nil {
				return fmt.Errorf("invalid URL %q: %w", rawURL, err)
			}

			domain := strings.ReplaceAll(parsed.Hostname(), ".", "_")
			timestamp := time.Now().Format("20060102_150405")

			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home dir: %w", err)
			}
			captureDir := filepath.Join(home, ".monoagent", "captures")
			if err := os.MkdirAll(captureDir, 0o755); err != nil {
				return fmt.Errorf("create capture dir: %w", err)
			}
			htmlFile := filepath.Join(captureDir, fmt.Sprintf("%s_%s.html", domain, timestamp))

			fmt.Fprintf(os.Stderr, "Launching browser for %s...\n", parsed.Hostname())

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

			fmt.Fprintf(os.Stderr, "Waiting %ds for JS to render...\n", waitSecs)
			time.Sleep(time.Duration(waitSecs) * time.Second)

			res, err := page.Eval("() => document.documentElement.outerHTML")
			if err != nil {
				return fmt.Errorf("capture HTML: %w", err)
			}

			html := res.Value.String()
			if err := os.WriteFile(htmlFile, []byte(html), 0o644); err != nil {
				return fmt.Errorf("save HTML: %w", err)
			}

			// Check whether the Claude skill is installed.
			claudeDir := filepath.Join(home, ".claude")
			skillInstalled := false
			if _, err := os.Stat(filepath.Join(claudeDir, "skills", claudeSkillName)); err == nil {
				skillInstalled = true
			}

			// Print structured output Claude can read and act on.
			fmt.Printf("\n[MONOAGENT_CRAWL]\n")
			fmt.Printf("url:       %s\n", rawURL)
			fmt.Printf("html_file: %s\n", htmlFile)
			fmt.Printf("html_size: %d bytes\n", len(html))
			fmt.Printf("skill_ready: %v\n", skillInstalled)
			fmt.Printf("[/MONOAGENT_CRAWL]\n\n")

			if skillInstalled {
				fmt.Printf("Next — run the Claude Code skill:\n")
				fmt.Printf("  /action-template-generator\n\n")
				fmt.Printf("When prompted, provide the HTML file path:\n")
				fmt.Printf("  %s\n\n", htmlFile)
				fmt.Printf("After the skill generates the template, install it:\n")
				fmt.Printf("  monoagent action template install <generated-file>\n")
			} else {
				fmt.Printf("Claude skill not installed. Run once:\n")
				fmt.Printf("  monoagent init --claude\n\n")
				fmt.Printf("Then run:\n")
				fmt.Printf("  /action-template-generator\n")
				fmt.Printf("  (provide html_file: %s)\n", htmlFile)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&headless, "headless", false, "Run browser headlessly (no window)")
	cmd.Flags().IntVar(&waitSecs, "wait", 8, "Seconds to wait for JS rendering")

	return cmd
}
