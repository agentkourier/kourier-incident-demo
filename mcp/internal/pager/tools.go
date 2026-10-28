package pager

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Register adds page_team to s.
func Register(s *mcp.Server, p *Pager) error {
	in, err := jsonschema.For[Input](nil)
	if err != nil {
		return fmt.Errorf("page_team input schema: %w", err)
	}
	sev := in.Properties["severity"]
	sev.Minimum, sev.Maximum = new(1.0), new(4.0)
	mcp.AddTool(s, &mcp.Tool{
		Name: "page_team",
		Description: "Page a team's on-call: posts one message to the team's Slack channel mentioning its on-call " +
			"group and primary on-call, with the severity and your one-line summary. At most one page per team " +
			"every 10 minutes.",
		InputSchema: in,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in Input) (*mcp.CallToolResult, Output, error) {
		out, err := p.Page(ctx, in)
		return nil, out, err
	})
	return nil
}
