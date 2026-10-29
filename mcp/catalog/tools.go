package catalog

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// LookupInput is lookup_service's input.
type LookupInput struct {
	Name string `json:"name" jsonschema:"the service name, for example checkout or payments"`
}

// ListOutput is list_services' output.
type ListOutput struct {
	Services []Summary `json:"services" jsonschema:"every service in the catalog"`
}

// Register adds lookup_service and list_services to s.
func Register(s *mcp.Server, c *Catalog) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false)}
	mcp.AddTool(s, &mcp.Tool{
		Name: "lookup_service",
		Description: "Look a service up in the software catalog: its system, owning team, the team's Slack channel " +
			"and on-call names, its GitHub repository and its tier.",
		Annotations: readOnly,
	}, func(_ context.Context, _ *mcp.CallToolRequest, in LookupInput) (*mcp.CallToolResult, Service, error) {
		svc, err := c.Service(in.Name)
		return nil, svc, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_services",
		Description: "List every service in the software catalog with its owning team.",
		Annotations: readOnly,
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, ListOutput, error) {
		return nil, ListOutput{Services: c.Services()}, nil
	})
}
