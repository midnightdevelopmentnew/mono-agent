// Package noderegistry builds the canonical workflow node-type registry
// shared by the CLI and the desktop app, so the set of available nodes
// has a single source of truth.
package noderegistry

import (
	"database/sql"

	"github.com/rs/zerolog"

	ainodes "monoagent/internal/ai/nodes"
	cfgpkg "monoagent/internal/config"
	agentnodes "monoagent/internal/nodes/agent"
	"monoagent/internal/nodes"
	crawlnodes "monoagent/internal/nodes/ai/crawl"
	"monoagent/internal/nodes/comm"
	"monoagent/internal/nodes/control"
	"monoagent/internal/nodes/data"
	dbnodes "monoagent/internal/nodes/db"
	httpnodes "monoagent/internal/nodes/http"
	imagenodes "monoagent/internal/nodes/image"
	peoplenodes "monoagent/internal/nodes/people"
	"monoagent/internal/nodes/service"
	"monoagent/internal/nodes/system"
	"monoagent/internal/workflow"
)

// Build creates a registry with all built-in node types registered.
// If db is non-nil, DB-backed node packages get the connection.
func Build(db *sql.DB) *workflow.NodeTypeRegistry {
	registry := workflow.NewNodeTypeRegistry()
	control.RegisterAll(registry)
	data.RegisterAll(registry)
	httpnodes.RegisterAll(registry)
	system.RegisterAll(registry)
	dbnodes.RegisterAll(registry)
	comm.RegisterAll(registry)
	service.RegisterAll(registry)
	nodes.RegisterBrowserNodes(registry)
	peoplenodes.RegisterAll(registry, db)

	// Local AI agent nodes (monomind delegation) — no store needed.
	agentnodes.RegisterAll(registry)

	// Historical ai.* provider nodes are deprecated: fail-fast stubs keep
	// saved workflows actionable instead of "unknown node type".
	ainodes.RegisterDeprecated(registry)

	// Image processing nodes (Tier 1)
	imagenodes.RegisterAll(registry)

	// AI crawl nodes (natural extraction runs on a local agent)
	crawlnodes.RegisterAll(registry, cfgpkg.NewAgentGenerator(zerolog.Nop()))

	// Register legacy (unprefixed) aliases so old workflows still resolve.
	for legacy, canonical := range map[string]string{
		"google_sheets": "service.google_sheets", "gmail": "service.gmail", "google_drive": "service.google_drive",
		"github": "service.github", "notion": "service.notion", "airtable": "service.airtable",
		"jira": "service.jira", "linear": "service.linear", "asana": "service.asana",
		"stripe": "service.stripe", "shopify": "service.shopify", "salesforce": "service.salesforce",
		"hubspot": "service.hubspot",
		"slack": "comm.slack", "discord": "comm.discord", "telegram": "comm.telegram",
		"twilio": "comm.twilio", "whatsapp": "comm.whatsapp",
		"email_send": "comm.email_send", "email_read": "comm.email_read",
		"mysql": "db.mysql", "postgres": "db.postgres", "mongodb": "db.mongodb", "redis": "db.redis",
		"datetime": "data.datetime", "crypto": "data.crypto", "html": "data.html",
		"xml": "data.xml", "markdown": "data.markdown", "spreadsheet": "data.spreadsheet",
		"compression": "data.compression", "write_binary_file": "data.write_binary_file",
		"if": "core.if", "switch": "core.switch", "merge": "core.merge", "set": "core.set",
		"code": "core.code", "filter": "core.filter", "sort": "core.sort", "limit": "core.limit",
		"aggregate": "core.aggregate", "wait": "core.wait",
		"http_request": "http.request", "http_response": "http.response",
		"execute_command": "system.execute_command", "rss_read": "system.rss_read",
		"read_write_file": "system.read_write_file",
	} {
		registry.Alias(legacy, canonical)
	}

	return registry
}
