package crawl

import (
	cfgpkg "monoagent/internal/config"
	"monoagent/internal/workflow"
)

func RegisterAll(r *workflow.NodeTypeRegistry, generator *cfgpkg.AgentGenerator) {
	r.Register("ai.read_page", func() workflow.NodeExecutor {
		return &ReadPageNode{}
	})
	r.Register("ai.extract_page", func() workflow.NodeExecutor {
		return &ExtractPageNode{Generator: generator}
	})
}
