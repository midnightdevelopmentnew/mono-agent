package dbnodes

import (
	"context"
	"strings"
	"testing"

	"monoagent/internal/workflow"
)

// TestNodesRequireConnectionString is a regression test: db.postgres, db.mysql,
// db.mongodb, and db.redis must all read their DSN from the "connection_string"
// config key (matching the "connection_string" credential field every database
// platform in internal/connections/registry.go stores under). Previously
// postgres/mysql/redis read split host/port/user/password/addr fields that no
// schema or credential ever populated, silently connecting to localhost instead
// of failing loudly.
func TestNodesRequireConnectionString(t *testing.T) {
	cases := []struct {
		name string
		node workflow.NodeExecutor
	}{
		{"db.postgres", &PostgresNode{}},
		{"db.mysql", &MySQLNode{}},
		{"db.mongodb", &MongoDBNode{}},
		{"db.redis", &RedisNode{}},
	}
	for _, tc := range cases {
		config := map[string]interface{}{"operation": "get", "database": "d", "collection": "c", "key": "k"}
		_, err := tc.node.Execute(context.Background(), workflow.NodeInput{}, config)
		if err == nil {
			t.Errorf("%s: expected error when connection_string is missing, got nil", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "connection_string") {
			t.Errorf("%s: expected error mentioning 'connection_string', got: %v", tc.name, err)
		}
	}
}
