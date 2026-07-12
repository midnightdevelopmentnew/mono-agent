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

// TestMongoDBRefusesMatchAllMutations is a regression test: destructive
// operations (delete_one/delete_many/update_one/update_many) must refuse a
// missing/empty 'filter' instead of treating an empty bson.D{} as match-all,
// which MongoDB would use to wipe/mutate the entire collection. The guard must
// fire before any network round-trip, mirroring the SQL nodes' refusal to run
// DELETE without a WHERE clause.
func TestMongoDBRefusesMatchAllMutations(t *testing.T) {
	node := &MongoDBNode{}
	for _, op := range []string{"delete_one", "delete_many", "update_one", "update_many"} {
		config := map[string]interface{}{
			"operation":         op,
			"connection_string": "mongodb://localhost:27017",
			"database":          "d",
			"collection":        "c",
			// no "filter" provided -> would resolve to bson.D{} (match all)
		}
		_, err := node.Execute(context.Background(), workflow.NodeInput{}, config)
		if err == nil {
			t.Errorf("%s: expected error when 'filter' is missing, got nil", op)
			continue
		}
		if !strings.Contains(err.Error(), "filter") {
			t.Errorf("%s: expected error mentioning 'filter', got: %v", op, err)
		}
	}
}
