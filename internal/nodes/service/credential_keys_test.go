package service

import (
	"context"
	"strings"
	"testing"

	"monoagent/internal/workflow"
)

// TestNodesAcceptRegistryCredentialKeys is a regression test: each node must
// read the exact config key name that internal/connections/registry.go injects
// for its platform, not a differently-named key that credential data never
// populates. We supply a config using the registry's real key names (plus
// enough operation-specific fields to get past validation) and assert the
// node proceeds to attempt a network call (any error NOT mentioning "required")
// instead of failing immediately on a missing required field.
func TestNodesAcceptRegistryCredentialKeys(t *testing.T) {
	cases := []struct {
		name   string
		node   workflow.NodeExecutor
		config map[string]interface{}
	}{
		{"service.jira (domain)", &JiraNode{}, map[string]interface{}{
			"domain": "example.atlassian.net", "email": "a@b.com", "api_token": "tok", "operation": "list_projects",
		}},
		{"service.linear (api_key)", &LinearNode{}, map[string]interface{}{
			"api_key": "tok", "operation": "list_issues",
		}},
		{"service.asana (access_token)", &AsanaNode{}, map[string]interface{}{
			"access_token": "tok", "operation": "list_workspaces",
		}},
		{"service.airtable (api_key)", &AirtableNode{}, map[string]interface{}{
			"api_key": "tok", "base_id": "app123", "table": "Table1", "operation": "list_records",
		}},
		{"service.shopify (shop_domain)", &ShopifyNode{}, map[string]interface{}{
			"shop_domain": "mystore.myshopify.com", "access_token": "tok", "operation": "list_orders",
		}},
		{"service.github (token)", &GitHubNode{}, map[string]interface{}{
			"token": "tok", "operation": "list_repos",
		}},
	}

	for _, tc := range cases {
		_, err := tc.node.Execute(context.Background(), workflow.NodeInput{}, tc.config)
		if err == nil {
			// A live network call succeeding in a test sandbox is implausible but
			// not itself a failure of this test's assertion.
			continue
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "is required") {
			t.Errorf("%s: got a 'required' validation error with the correct registry key set: %v", tc.name, err)
		}
	}
}
