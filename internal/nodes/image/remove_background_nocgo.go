//go:build !cgo

package image

import (
	"context"
	"fmt"

	"monoagent/internal/workflow"
)

// Execute returns a clear error when the binary was compiled without CGo.
// Rebuild with CGO_ENABLED=1 to enable U2-Net background removal.
func (n *RemoveBackgroundNode) Execute(_ context.Context, _ workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	return nil, fmt.Errorf(
		"image.remove_background requires CGo (ONNX Runtime bindings).\n" +
			"Rebuild the CLI with CGo enabled:\n" +
			"  go install monoagent/cmd/monoagentcli@latest\n" +
			"or:\n" +
			"  CGO_ENABLED=1 go build -o monoagent ./cmd/monoagentcli",
	)
}
