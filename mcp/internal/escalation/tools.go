package escalation

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register adds escalate_incident and ask_oncall to s.
func Register(s *mcp.Server, e *Escalator) error {
	escIn, err := jsonschema.For[EscalateInput](nil)
	if err != nil {
		return fmt.Errorf("escalate_incident input schema: %w", err)
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "escalate_incident",
		Description: "Hand an incident to #incidents when you suspect a dependency caused it. The tool checks the " +
			"evidence itself before posting: the impacted service is failing, the suspected service rolled out " +
			"shortly before the errors began, the impacted service's calls to it are failing, and its own error " +
			"rate is low. It posts only when every check passes, and returns each check with what it measured. " +
			"At most one escalation per impacted service every 30 minutes. dry_run evaluates without posting.",
		InputSchema: escIn,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in EscalateInput) (*mcp.CallToolResult, EscalateOutput, error) {
		out, err := e.Escalate(ctx, in)
		return nil, out, err
	})

	askIn, err := jsonschema.For[AskInput](nil)
	if err != nil {
		return fmt.Errorf("ask_oncall input schema: %w", err)
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "ask_oncall",
		Description: "Ask a person for help when you cannot confirm the cause: replies in the alert's thread, " +
			"mentioning the team's on-call, with what you checked and ruled out. At most one question per thread " +
			"every 10 minutes.",
		InputSchema: askIn,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AskInput) (*mcp.CallToolResult, AskOutput, error) {
		out, err := e.AskOncall(ctx, in)
		return nil, out, err
	})
	return nil
}
