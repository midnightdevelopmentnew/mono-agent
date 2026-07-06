package service

import "monoagent/internal/workflow"

func RegisterAll(r *workflow.NodeTypeRegistry) {
	RegisterGroupA(r)
	RegisterGroupB(r)
}
