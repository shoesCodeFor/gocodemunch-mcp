package orchestration

import "context"

// ToolHandler executes a single MCP tool request.
type ToolHandler func(ctx context.Context, arguments map[string]any) (map[string]any, error)

// Tool defines a listable tool contract plus executable handler.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Handler     ToolHandler    `json:"-"`
}
