package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"monoagent/internal/ai"
)

func newAICmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "Configure AI providers used by ai.* workflow nodes",
		Long: "List, add, delete, and test AI providers — the same registry the desktop app's AI Providers " +
			"page manages. ai.* workflow nodes resolve their provider by ID from this store, so a provider must " +
			"exist here before any AI node can run.",
	}
	cmd.AddCommand(
		newAIProviderCmd(cfg),
	)
	return cmd
}

func newAIProviderCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage AI providers",
	}
	cmd.AddCommand(
		newAIProviderListCmd(cfg),
		newAIProviderAddCmd(cfg),
		newAIProviderDeleteCmd(cfg),
		newAIProviderTestCmd(cfg),
	)
	return cmd
}

// openAIStore opens the DB and builds an AIStore scoped to the active profile.
func openAIStore(cfg *globalConfig) (*ai.AIStore, func(), error) {
	db, err := initDB(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("initializing database: %w", err)
	}
	store, err := ai.NewAIStore(db.DB)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("initializing AI store: %w", err)
	}
	return store, func() { db.Close() }, nil
}

func newAIProviderListCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured AI providers",
		Example: `  monoagentcli ai provider list
  monoagentcli --json ai provider list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, closeDB, err := openAIStore(cfg)
			if err != nil {
				return err
			}
			defer closeDB()

			providers, err := store.ListProviders(cfg.ProfileID)
			if err != nil {
				return fmt.Errorf("listing providers: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(providers) // api_key omitted via AIProvider.MarshalJSON
			}

			if len(providers) == 0 {
				fmt.Println("No AI providers configured. Add one with `ai provider add`.")
				return nil
			}
			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"ID", "Name", "Provider", "Model", "Status"})
			table.SetBorder(false)
			table.SetAutoWrapText(false)
			for _, p := range providers {
				shortID := p.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				table.Append([]string{shortID, truncateStr(p.Name, 20), p.ProviderID, p.DefaultModel, p.Status})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d provider(s)\n", len(providers))
			return nil
		},
	}
}

func newAIProviderAddCmd(cfg *globalConfig) *cobra.Command {
	var name, providerID, apiKey, baseURL, model, tier, extraHeaders string
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new AI provider",
		Example: `  monoagentcli ai provider add --name "OpenAI" --provider openai --api-key sk-... --model gpt-4o-mini
  monoagentcli ai provider add --name "Local" --provider openai --tier gateway --base-url http://localhost:11434/v1 --model llama3`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || providerID == "" {
				return fmt.Errorf("--name and --provider are required")
			}
			store, closeDB, err := openAIStore(cfg)
			if err != nil {
				return err
			}
			defer closeDB()

			if tier == "" {
				tier = "known"
			}
			p := ai.AIProvider{
				ID:           uuid.NewString(),
				Name:         name,
				ProviderID:   providerID,
				Tier:         tier,
				APIKey:       apiKey,
				BaseURL:      baseURL,
				DefaultModel: model,
				ExtraHeaders: extraHeaders,
				Status:       "untested",
				ProfileID:    cfg.ProfileID,
			}
			if err := store.SaveProvider(p); err != nil {
				return fmt.Errorf("saving provider: %w", err)
			}
			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(p)
			}
			fmt.Printf("Added AI provider %s (%s)\n", p.Name, p.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Display name for the provider (required)")
	cmd.Flags().StringVar(&providerID, "provider", "", "Registry provider id, e.g. openai, anthropic (required)")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Base URL override (for gateways / self-hosted)")
	cmd.Flags().StringVar(&model, "model", "", "Default model id")
	cmd.Flags().StringVar(&tier, "tier", "", "Provider tier: known | gateway (default known)")
	cmd.Flags().StringVar(&extraHeaders, "extra-headers", "", "Extra HTTP headers as a JSON object")
	return cmd
}

func newAIProviderDeleteCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Short:   "Delete an AI provider",
		Example: `  monoagentcli ai provider delete 1a2b3c4d`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, closeDB, err := openAIStore(cfg)
			if err != nil {
				return err
			}
			defer closeDB()

			if err := store.DeleteProvider(args[0], cfg.ProfileID); err != nil {
				return fmt.Errorf("deleting provider: %w", err)
			}
			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"id": args[0], "status": "deleted"})
			}
			fmt.Printf("Deleted AI provider %s\n", args[0])
			return nil
		},
	}
}

func newAIProviderTestCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "test <id>",
		Short:   "Test an AI provider by sending a minimal completion request",
		Example: `  monoagentcli ai provider test 1a2b3c4d`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, closeDB, err := openAIStore(cfg)
			if err != nil {
				return err
			}
			defer closeDB()

			p, err := store.GetProvider(args[0], cfg.ProfileID)
			if err != nil {
				return fmt.Errorf("provider %q not found", args[0])
			}
			client, err := ai.NewClient(p)
			if err != nil {
				return fmt.Errorf("building client: %w", err)
			}
			model := p.DefaultModel
			if model == "" {
				if def, ok := ai.GetProviderDef(p.ProviderID); ok && len(def.Models) > 0 {
					model = def.Models[0].ID
				} else {
					model = "gpt-4o-mini"
				}
			}
			_, testErr := client.Complete(context.Background(), ai.CompletionRequest{
				Model:     model,
				Messages:  []ai.Message{{Role: ai.RoleUser, Content: "Say ok"}},
				MaxTokens: 5,
			})
			status := "active"
			if testErr != nil {
				status = "error"
			}
			_ = store.UpdateProviderStatus(p.ID, status, time.Now().UTC().Format(time.RFC3339), cfg.ProfileID)

			if cfg.JSONOutput {
				out := map[string]string{"id": p.ID, "status": status}
				if testErr != nil {
					out["error"] = testErr.Error()
				}
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			if testErr != nil {
				return fmt.Errorf("provider test failed: %w", testErr)
			}
			fmt.Printf("Provider %s is working (model %s)\n", p.Name, model)
			return nil
		},
	}
}
