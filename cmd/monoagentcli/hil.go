package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

func newHILCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hil",
		Short: "List, approve, or reject Human-in-Loop items",
		Long: "Human-in-Loop (HIL) nodes pause a running workflow until a person reviews and " +
			"approves or rejects the data. This command exposes that queue to headless/agent use — " +
			"the same operations as the desktop app's Human in Loop page.",
	}
	cmd.AddCommand(
		newHILListCmd(cfg),
		newHILApproveCmd(cfg),
		newHILRejectCmd(cfg),
	)
	return cmd
}

type hilItem struct {
	ID           string                 `json:"id"`
	ExecutionID  string                 `json:"execution_id"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name"`
	NodeID       string                 `json:"node_id"`
	NodeName     string                 `json:"node_name"`
	Status       string                 `json:"status"`
	ReadonlyData map[string]interface{} `json:"readonly_data"`
	EditableData map[string]interface{} `json:"editable_data"`
	CreatedAt    string                 `json:"created_at"`
}

func newHILListCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List pending Human-in-Loop items",
		Example: `  monoagentcli hil list
  monoagentcli --json hil list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			rows, err := db.DB.Query(
				`SELECT h.id, h.execution_id, h.workflow_id, h.node_id, h.node_name, h.status,
				        h.readonly_data, h.editable_data, h.created_at, COALESCE(w.name,'')
				 FROM hil_pending h
				 LEFT JOIN workflows w ON w.id = h.workflow_id
				 WHERE h.status = 'pending' AND COALESCE(h.profile_id,'default') = ?
				 ORDER BY h.created_at ASC`,
				cfg.ProfileID,
			)
			if err != nil {
				return fmt.Errorf("querying HIL items: %w", err)
			}
			defer rows.Close()

			var items []hilItem
			for rows.Next() {
				var it hilItem
				var roRaw, edRaw string
				if err := rows.Scan(&it.ID, &it.ExecutionID, &it.WorkflowID, &it.NodeID, &it.NodeName,
					&it.Status, &roRaw, &edRaw, &it.CreatedAt, &it.WorkflowName); err != nil {
					return fmt.Errorf("scanning HIL item: %w", err)
				}
				_ = json.Unmarshal([]byte(roRaw), &it.ReadonlyData)
				_ = json.Unmarshal([]byte(edRaw), &it.EditableData)
				items = append(items, it)
			}
			if err := rows.Err(); err != nil {
				return fmt.Errorf("iterating HIL items: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if items == nil {
					items = []hilItem{}
				}
				return enc.Encode(items)
			}

			if len(items) == 0 {
				fmt.Println("No pending Human-in-Loop items.")
				return nil
			}

			table := tablewriter.NewWriter(os.Stdout)
			table.SetHeader([]string{"ID", "Workflow", "Node", "Created"})
			table.SetBorder(false)
			table.SetAutoWrapText(false)
			for _, it := range items {
				shortID := it.ID
				if len(shortID) > 8 {
					shortID = shortID[:8]
				}
				name := it.WorkflowName
				if name == "" {
					name = it.WorkflowID
				}
				table.Append([]string{shortID, truncateStr(name, 24), truncateStr(it.NodeName, 20), it.CreatedAt})
			}
			table.Render()
			fmt.Fprintf(os.Stderr, "\nTotal: %d pending item(s). Approve with `hil approve <id>` or reject with `hil reject <id>`.\n", len(items))
			return nil
		},
	}
}

func newHILApproveCmd(cfg *globalConfig) *cobra.Command {
	var editedData string
	cmd := &cobra.Command{
		Use:   "approve <id>",
		Short: "Approve a pending Human-in-Loop item",
		Long:  "Approve a pending item, optionally overriding its editable data with a JSON object via --data.",
		Example: `  monoagentcli hil approve 1a2b3c4d
  monoagentcli hil approve 1a2b3c4d --data '{"caption":"edited text"}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if editedData == "" {
				editedData = "{}"
			}
			var check map[string]interface{}
			if err := json.Unmarshal([]byte(editedData), &check); err != nil {
				return fmt.Errorf("--data is not valid JSON: %w", err)
			}
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			res, err := db.DB.Exec(
				`UPDATE hil_pending SET status='approved', edited_data=?, updated_at=CURRENT_TIMESTAMP
				 WHERE id=? AND status='pending' AND COALESCE(profile_id,'default') = ?`,
				editedData, args[0], cfg.ProfileID,
			)
			if err != nil {
				return fmt.Errorf("approving HIL item: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("HIL item %q not found or already resolved", args[0])
			}
			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"id": args[0], "status": "approved"})
			}
			fmt.Printf("Approved HIL item %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&editedData, "data", "", "JSON object overriding the item's editable data")
	return cmd
}

func newHILRejectCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "reject <id>",
		Short: "Reject a pending Human-in-Loop item (the workflow errors out)",
		Example: `  monoagentcli hil reject 1a2b3c4d`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := initDB(cfg)
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			res, err := db.DB.Exec(
				`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
				 WHERE id=? AND status='pending' AND COALESCE(profile_id,'default') = ?`,
				args[0], cfg.ProfileID,
			)
			if err != nil {
				return fmt.Errorf("rejecting HIL item: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("HIL item %q not found or already resolved", args[0])
			}
			if cfg.JSONOutput {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"id": args[0], "status": "rejected"})
			}
			fmt.Printf("Rejected HIL item %s\n", args[0])
			return nil
		},
	}
}
