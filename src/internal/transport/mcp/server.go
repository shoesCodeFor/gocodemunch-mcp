package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/orchestration"
)

// ToolService is the orchestration contract required by transport.
type ToolService interface {
	ListTools() []orchestration.Tool
	CallTool(ctx context.Context, name string, arguments map[string]any) map[string]any
}

// Server implements MCP stdio JSON-RPC transport.
type Server struct {
	in     *bufio.Reader
	out    *bufio.Writer
	tools  ToolService
	cfg    config.Config
	closed bool
}

// NewServer constructs a stdio MCP server.
func NewServer(in io.Reader, out io.Writer, tools ToolService, cfg config.Config) *Server {
	return &Server{
		in:    bufio.NewReader(in),
		out:   bufio.NewWriter(out),
		tools: tools,
		cfg:   cfg,
	}
}

// Serve processes requests until EOF or context cancellation.
func (s *Server) Serve(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		request, err := readRPCMessage(s.in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		if len(request.ID) == 0 {
			if err := s.handleNotification(ctx, request); err != nil {
				return err
			}
			continue
		}

		response := s.handleRequest(ctx, request)
		if err := writeRPCMessage(s.out, response); err != nil {
			return err
		}
	}
}

func (s *Server) handleNotification(_ context.Context, request rpcRequest) error {
	if request.Method == "notifications/initialized" {
		return nil
	}
	return nil
}

func (s *Server) handleRequest(ctx context.Context, request rpcRequest) rpcResponse {
	result, rpcErr := s.dispatch(ctx, request)
	if rpcErr != nil {
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      request.ID,
			Error:   rpcErr,
		}
	}
	return rpcResponse{
		JSONRPC: "2.0",
		ID:      request.ID,
		Result:  result,
	}
}

func (s *Server) dispatch(ctx context.Context, request rpcRequest) (any, *rpcError) {
	switch request.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    s.cfg.ServerName,
				"version": s.cfg.ServerVersion,
			},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools.ListTools()}, nil
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if len(request.Params) > 0 {
			if err := json.Unmarshal(request.Params, &params); err != nil {
				return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("invalid params: %v", err)}
			}
		}
		if params.Name == "" {
			return nil, &rpcError{Code: -32602, Message: "missing tool name"}
		}
		payload := s.tools.CallTool(ctx, params.Name, params.Arguments)
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return nil, &rpcError{Code: -32603, Message: fmt.Sprintf("encode tool payload: %v", err)}
		}

		result := map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": string(encoded),
			}},
		}
		if _, hasError := payload["error"]; hasError {
			result["isError"] = true
		}
		return result, nil
	default:
		return nil, &rpcError{Code: -32601, Message: fmt.Sprintf("method not found: %s", request.Method)}
	}
}
