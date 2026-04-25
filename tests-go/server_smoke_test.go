package testsgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/jgravelle/gocodemunch-mcp/src/server"
)

func TestStdIOServerStartupSmoke(t *testing.T) {
	var in bytes.Buffer
	writeFrame(t, &in, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{},
	})
	writeFrame(t, &in, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})

	var out bytes.Buffer
	mcpServer := server.New(&in, &out, server.WithServerInfo("gocodemunch-mcp", "test"))
	if err := mcpServer.Serve(context.Background()); err != nil {
		t.Fatalf("serve failed: %v", err)
	}

	responses := readFrames(t, &out)
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}

	var initResp map[string]any
	mustJSON(t, responses[0], &initResp)
	if _, ok := initResp["result"]; !ok {
		t.Fatalf("initialize response missing result: %s", responses[0])
	}

	var listResp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	mustJSON(t, responses[1], &listResp)

	if got := len(listResp.Result.Tools); got != 27 {
		t.Fatalf("expected 27 tools, got %d", got)
	}

	toolNames := make(map[string]struct{}, len(listResp.Result.Tools))
	for _, tool := range listResp.Result.Tools {
		toolNames[tool.Name] = struct{}{}
	}

	required := []string{
		"index_repo", "index_folder", "index_file", "list_repos", "resolve_repo",
		"get_file_tree", "get_file_outline", "get_symbol_source", "get_file_content", "search_symbols",
		"invalidate_cache", "search_text", "get_repo_outline", "find_importers", "find_references",
		"check_references", "search_columns", "get_context_bundle", "get_session_stats", "get_dependency_graph",
		"get_symbol_diff", "get_class_hierarchy", "get_related_symbols", "suggest_queries", "get_blast_radius",
		"wait_for_fresh", "check_freshness",
	}
	for _, name := range required {
		if _, ok := toolNames[name]; !ok {
			t.Fatalf("missing tool %q from tools/list result", name)
		}
	}
}

func writeFrame(t *testing.T, out io.Writer, payload any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := fmt.Fprintf(out, "Content-Length: %d\r\n\r\n", len(encoded)); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := out.Write(encoded); err != nil {
		t.Fatalf("write payload: %v", err)
	}
}

func readFrames(t *testing.T, in *bytes.Buffer) []json.RawMessage {
	t.Helper()
	frames := make([]json.RawMessage, 0, 4)
	for in.Len() > 0 {
		headersEnd := bytes.Index(in.Bytes(), []byte("\r\n\r\n"))
		if headersEnd < 0 {
			t.Fatalf("invalid frame headers")
		}
		headers := string(in.Next(headersEnd + 4))

		contentLength := -1
		for _, line := range strings.Split(headers, "\r\n") {
			if !strings.HasPrefix(strings.ToLower(line), "content-length:") {
				continue
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			parsed, err := strconv.Atoi(value)
			if err != nil {
				t.Fatalf("invalid content length %q: %v", value, err)
			}
			contentLength = parsed
		}
		if contentLength < 0 {
			t.Fatalf("missing Content-Length header")
		}

		payload := in.Next(contentLength)
		frames = append(frames, append([]byte(nil), payload...))
	}
	return frames
}

func mustJSON(t *testing.T, payload []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(payload, out); err != nil {
		t.Fatalf("decode json failed: %v\npayload=%s", err, payload)
	}
}
