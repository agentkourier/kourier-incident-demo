package rollout

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HistoryInput is rollout_history's input.
type HistoryInput struct {
	Namespace  string `json:"namespace" jsonschema:"the Deployment's namespace"`
	Deployment string `json:"deployment" jsonschema:"the Deployment's name"`
}

// RollbackInput is rollback_deployment's input.
type RollbackInput struct {
	Namespace  string `json:"namespace" jsonschema:"the Deployment's namespace"`
	Deployment string `json:"deployment" jsonschema:"the Deployment's name"`
	ToRevision int64  `json:"to_revision" jsonschema:"the revision to roll back to, from rollout_history"`
	DryRun     bool   `json:"dry_run,omitempty" jsonschema:"report what would change without changing anything"`
}

// Register adds rollout_history and rollback_deployment to s.
func Register(s *mcp.Server, svc *Service) error {
	mcp.AddTool(s, &mcp.Tool{
		Name: "rollout_history",
		Description: "List a Deployment's rollout revisions (oldest first) with each revision's image, version, " +
			"creation time, replica count and whether it is current. Read-only.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in HistoryInput) (*mcp.CallToolResult, History, error) {
		h, err := svc.History(ctx, in.Namespace, in.Deployment)
		return nil, h, err
	})

	in, err := jsonschema.For[RollbackInput](nil)
	if err != nil {
		return fmt.Errorf("rollback_deployment input schema: %w", err)
	}
	in.Properties["to_revision"].Minimum = new(1.0)
	mcp.AddTool(s, &mcp.Tool{
		Name: "rollback_deployment",
		Description: "Roll a Deployment back to an earlier revision's pod template, like `kubectl rollout undo " +
			"--to-revision`. Only allowlisted deployments. Set dry_run to preview.",
		InputSchema: in,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RollbackInput) (*mcp.CallToolResult, RollbackResult, error) {
		r, err := svc.Rollback(ctx, in.Namespace, in.Deployment, in.ToRevision, in.DryRun)
		return nil, r, err
	})
	return nil
}
