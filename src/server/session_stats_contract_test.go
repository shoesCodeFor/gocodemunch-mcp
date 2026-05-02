package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/telemetry"
)

func TestGetSessionStatsTrendWindowsContract(t *testing.T) {
	storageRoot := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storageRoot)
	t.Setenv("VECTOR_BACKEND", "sqlite")
	t.Setenv("EMBEDDING_PROVIDER", "ollama")
	t.Setenv("EMBEDDING_MODEL", "bge-m3")
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434")
	t.Setenv("GOCODEMUNCH_SAVINGS_TELEMETRY_ENABLED", "true")
	t.Setenv("GOCODEMUNCH_SAVINGS_SNAPSHOT_INTERVAL_MS", "60000")
	t.Setenv("GOCODEMUNCH_SAVINGS_CODEX_INPUT_USD_PER_MTOK", "1.50")
	t.Setenv("GOCODEMUNCH_SAVINGS_CODEX_OUTPUT_USD_PER_MTOK", "6.00")
	t.Setenv("GOCODEMUNCH_SAVINGS_CLAUDE_CODE_INPUT_USD_PER_MTOK", "3.00")
	t.Setenv("GOCODEMUNCH_SAVINGS_CLAUDE_CODE_OUTPUT_USD_PER_MTOK", "15.00")
	t.Setenv("GOCODEMUNCH_SAVINGS_AMP_INPUT_USD_PER_MTOK", "1.50")
	t.Setenv("GOCODEMUNCH_SAVINGS_AMP_OUTPUT_USD_PER_MTOK", "6.00")

	now := time.Now().UTC().Truncate(time.Second)
	store, err := storage.NewSQLiteTelemetryStoreWithOptions(storageRoot, storage.SQLiteTelemetryStoreOptions{
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create telemetry store: %v", err)
	}

	if err := store.SaveSnapshot(context.Background(), telemetry.PersistedCumulativeSnapshot{
		CapturedAt: now.Add(-time.Hour),
		Cumulative: telemetry.CumulativeSnapshot{
			FirstRecordedAt:   now.Add(-40 * 24 * time.Hour),
			LastRecordedAt:    now.Add(-time.Hour),
			SessionCount:      4,
			CallCount:         9,
			RequestTokens:     270,
			ResponseTokens:    90,
			TotalTokens:       360,
			InputTokensSaved:  90,
			OutputTokensSaved: 30,
			TokensSaved:       120,
			CostAvoidedUSD: map[string]float64{
				"claude_code": 0.00072,
				"codex":       0.000315,
				"amp":         0.000315,
			},
			ToolBreakdown: map[string]telemetry.ToolSnapshot{
				"seeded_tool": {
					CallCount:         9,
					RequestTokens:     270,
					ResponseTokens:    90,
					TotalTokens:       360,
					InputTokensSaved:  90,
					OutputTokensSaved: 30,
					TokensSaved:       120,
					CostAvoidedUSD: map[string]float64{
						"claude_code": 0.00072,
						"codex":       0.000315,
						"amp":         0.000315,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("seed persisted telemetry snapshot: %v", err)
	}

	if err := store.SaveCallEvents(context.Background(), []telemetry.PersistedCallEvent{
		{
			CapturedAt: now.Add(-2 * time.Hour),
			Call: telemetry.CallSnapshot{
				ToolName:          "search_text",
				StartedAt:         now.Add(-2*time.Hour - time.Second),
				FinishedAt:        now.Add(-2 * time.Hour),
				RequestTokens:     24,
				ResponseTokens:    16,
				TotalTokens:       40,
				InputTokensSaved:  9,
				OutputTokensSaved: 6,
				TokensSaved:       15,
				LogicalCalls:      2,
				CostAvoidedUSD: map[string]float64{
					"claude_code": 0.000117,
					"codex":       0.0000495,
					"amp":         0.0000495,
				},
			},
		},
		{
			CapturedAt: now.Add(-14 * 24 * time.Hour),
			Call: telemetry.CallSnapshot{
				ToolName:          "get_context_bundle",
				StartedAt:         now.Add(-14*24*time.Hour - time.Second),
				FinishedAt:        now.Add(-14 * 24 * time.Hour),
				RequestTokens:     12,
				ResponseTokens:    8,
				TotalTokens:       20,
				InputTokensSaved:  4,
				OutputTokensSaved: 3,
				TokensSaved:       7,
				LogicalCalls:      1,
				CostAvoidedUSD: map[string]float64{
					"claude_code": 0.000057,
					"codex":       0.000024,
					"amp":         0.000024,
				},
			},
		},
	}); err != nil {
		t.Fatalf("seed persisted telemetry call events: %v", err)
	}

	responses := runServerRequestsAndClose(t, []map[string]any{
		initializeServerRequest(1),
		toolCallServerRequest(2, "list_repos", map[string]any{}),
		toolCallServerRequest(3, "get_session_stats", map[string]any{
			"trend_windows": []any{"last_24h", "last_30d"},
		}),
	})

	listPayload := toolResponsePayload(t, responses[1])
	if got := intFieldFromMap(listPayload, "count"); got != 0 {
		t.Fatalf("expected empty repo list in contract test, got %#v", listPayload)
	}

	stats := toolResponsePayload(t, responses[2])
	if got := intFieldFromMap(stats, "session_calls"); got != 2 {
		t.Fatalf("expected live session_calls to preserve legacy contract, got %#v", stats)
	}
	if got := intFieldFromMap(stats, "total_calls"); got != 11 {
		t.Fatalf("expected cumulative total_calls to include restored telemetry plus live calls, got %#v", stats)
	}
	if got := intFieldFromMap(stats, "total_sessions"); got != 5 {
		t.Fatalf("expected cumulative total_sessions to include one live session, got %#v", stats)
	}

	toolBreakdown := mapFieldFromMap(stats, "tool_breakdown")
	if _, ok := toolBreakdown["list_repos"]; !ok {
		t.Fatalf("expected legacy tool_breakdown to include list_repos, got %#v", stats)
	}
	if _, ok := toolBreakdown["get_session_stats"]; !ok {
		t.Fatalf("expected legacy tool_breakdown to include get_session_stats, got %#v", stats)
	}

	totalToolBreakdown := mapFieldFromMap(stats, "total_tool_breakdown")
	seededTool := mapFieldFromMap(totalToolBreakdown, "seeded_tool")
	if got := intFieldFromMap(seededTool, "call_count"); got != 9 {
		t.Fatalf("expected total_tool_breakdown to preserve restored seeded_tool totals, got %#v", stats)
	}

	sessionRollups := mapFieldFromMap(stats, "session_rollups")
	sessionCompetitors := mapFieldFromMap(sessionRollups, "competitor_breakdown")
	if got := intFieldFromMap(mapFieldFromMap(sessionCompetitors, "codex"), "tokens_saved"); got != intFieldFromMap(stats, "session_tokens_saved") {
		t.Fatalf("expected session_rollups.competitor_breakdown codex totals to match legacy session totals, got %#v", stats)
	}

	totalRollups := mapFieldFromMap(stats, "total_rollups")
	totalCompetitors := mapFieldFromMap(totalRollups, "competitor_breakdown")
	if got := intFieldFromMap(mapFieldFromMap(totalCompetitors, "claude_code"), "tokens_saved"); got != intFieldFromMap(stats, "total_tokens_saved") {
		t.Fatalf("expected total_rollups.competitor_breakdown claude_code totals to match legacy totals, got %#v", stats)
	}

	trendWindows := mapFieldFromMap(stats, "trend_windows")
	if len(trendWindows) != 2 {
		t.Fatalf("expected requested trend windows to be present in MCP response, got %#v", stats)
	}

	last24h := mapFieldFromMap(trendWindows, "last_24h")
	if got := stringFieldFromMap(last24h, "window"); got != "last_24h" {
		t.Fatalf("expected last_24h contract window name, got %#v", last24h)
	}
	if got := stringFieldFromMap(last24h, "bucket"); got != "hour" {
		t.Fatalf("expected last_24h hourly buckets, got %#v", last24h)
	}
	if got := intFieldFromMap(last24h, "call_count"); got != 2 {
		t.Fatalf("expected last_24h call_count=2 from persisted events, got %#v", last24h)
	}
	last24hTools := mapFieldFromMap(last24h, "tool_breakdown")
	if got := intFieldFromMap(mapFieldFromMap(last24hTools, "search_text"), "call_count"); got != 2 {
		t.Fatalf("expected last_24h tool_breakdown.search_text.call_count=2, got %#v", last24h)
	}
	last24hCompetitors := mapFieldFromMap(last24h, "competitor_breakdown")
	if got := intFieldFromMap(mapFieldFromMap(last24hCompetitors, "codex"), "tokens_saved"); got != 15 {
		t.Fatalf("expected last_24h competitor breakdown to mirror persisted token savings, got %#v", last24h)
	}
	points, ok := last24h["points"].([]any)
	if !ok || len(points) == 0 {
		t.Fatalf("expected non-empty trend points in last_24h contract payload, got %#v", last24h)
	}

	last30d := mapFieldFromMap(trendWindows, "last_30d")
	if got := stringFieldFromMap(last30d, "bucket"); got != "day" {
		t.Fatalf("expected last_30d daily buckets, got %#v", last30d)
	}
	if got := intFieldFromMap(last30d, "call_count"); got != 3 {
		t.Fatalf("expected last_30d call_count=3 from retained persisted events, got %#v", last30d)
	}
	if _, ok := mapFieldFromMap(last30d, "tool_breakdown")["get_context_bundle"]; !ok {
		t.Fatalf("expected last_30d tool_breakdown to include retained get_context_bundle event, got %#v", last30d)
	}

	meta := mapFieldFromMap(stats, "_meta")
	if got := intFieldFromMap(meta, "total_tokens_saved"); got != intFieldFromMap(stats, "total_tokens_saved") {
		t.Fatalf("expected _meta.total_tokens_saved to remain aligned with legacy total_tokens_saved, got %#v", stats)
	}
}

func runServerRequestsAndClose(t *testing.T, requests []map[string]any) []map[string]any {
	t.Helper()

	var in bytes.Buffer
	for _, request := range requests {
		writeServerFrame(t, &in, request)
	}

	var out bytes.Buffer
	srv := New(&in, &out, WithServerInfo("gocodemunch-mcp", "test"))
	defer func() {
		if err := srv.Close(); err != nil {
			t.Fatalf("close failed: %v", err)
		}
	}()

	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("serve failed: %v", err)
	}

	frames := readServerFrames(t, &out)
	if len(frames) != len(requests) {
		t.Fatalf("expected %d responses, got %d", len(requests), len(frames))
	}

	decoded := make([]map[string]any, 0, len(frames))
	for _, frame := range frames {
		var payload map[string]any
		mustServerJSON(t, frame, &payload)
		decoded = append(decoded, payload)
	}
	return decoded
}

func initializeServerRequest(id int) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "initialize",
		"params":  map[string]any{},
	}
}

func toolCallServerRequest(id int, name string, arguments map[string]any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": arguments,
		},
	}
}

func toolResponsePayload(t *testing.T, response map[string]any) map[string]any {
	t.Helper()

	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("response missing result: %#v", response)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("response missing content envelope: %#v", response)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("response content has invalid shape: %#v", response)
	}
	text, _ := first["text"].(string)

	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode tool payload: %v\ntext=%s", err, text)
	}
	return payload
}

func writeServerFrame(t *testing.T, out io.Writer, payload any) {
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

func readServerFrames(t *testing.T, in *bytes.Buffer) []json.RawMessage {
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

func mustServerJSON(t *testing.T, payload []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(payload, out); err != nil {
		t.Fatalf("decode json failed: %v\npayload=%s", err, payload)
	}
}

func intFieldFromMap(m map[string]any, key string) int {
	value := m[key]
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func stringFieldFromMap(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func mapFieldFromMap(m map[string]any, key string) map[string]any {
	value, _ := m[key].(map[string]any)
	return value
}
