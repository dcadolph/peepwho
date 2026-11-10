package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// testServer returns a server with one echo tool and one failing tool.
func testServer() *Server {
	s := New("whotoask", "test", io.Discard)
	s.AddTool(Tool{
		Name:        "echo",
		Description: "Echo the input back.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(_ context.Context, args json.RawMessage) (string, error) {
		return string(args), nil
	})
	s.AddTool(Tool{
		Name:        "boom",
		Description: "Always fails.",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}, func(context.Context, json.RawMessage) (string, error) {
		return "", fmt.Errorf("it broke")
	})
	return s
}

// serveLines runs the server over the given input lines and returns one
// decoded response per output line.
func serveLines(t *testing.T, lines ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	err := testServer().Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")), &out)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var responses []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(out.String()))
	for scanner.Scan() {
		var resp map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", scanner.Text(), err)
		}
		responses = append(responses, resp)
	}
	return responses
}

// TestHandshakeAndTools verifies the full session: initialize echoes the
// client's protocol version, the initialized notification is silent, ping
// answers, tools/list names the tools, and tools/call returns the payload.
func TestHandshakeAndTools(t *testing.T) {
	t.Parallel()
	responses := serveLines(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize",`+
			`"params":{"protocolVersion":"2026-03-26","capabilities":{},`+
			`"clientInfo":{"name":"claude-code","version":"2.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call",`+
			`"params":{"name":"echo","arguments":{"q":"billing"}}}`,
	)
	if len(responses) != 4 {
		t.Fatalf("responses = %d, want 4 (notification is silent)", len(responses))
	}

	init := responses[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2026-03-26" {
		t.Errorf("protocolVersion = %v, want the client's echoed back", init["protocolVersion"])
	}
	server := init["serverInfo"].(map[string]any)
	if server["name"] != "whotoask" {
		t.Errorf("serverInfo = %v", server)
	}

	list := responses[2]["result"].(map[string]any)
	tools := list["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "echo" {
		t.Errorf("tools = %v", tools)
	}

	call := responses[3]["result"].(map[string]any)
	if call["isError"] != false {
		t.Errorf("isError = %v, want false", call["isError"])
	}
	content := call["content"].([]any)[0].(map[string]any)
	if !strings.Contains(content["text"].(string), "billing") {
		t.Errorf("content = %v, want the echoed arguments", content)
	}
}

// TestToolErrorAndProtocolErrors verifies a failing tool returns an isError
// result while unknown methods and tools are protocol errors.
func TestToolErrorAndProtocolErrors(t *testing.T) {
	t.Parallel()
	responses := serveLines(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ghost","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
		`not json at all`,
	)
	if len(responses) != 4 {
		t.Fatalf("responses = %d, want 4", len(responses))
	}

	boom := responses[0]["result"].(map[string]any)
	if boom["isError"] != true {
		t.Errorf("failing tool isError = %v, want true", boom["isError"])
	}
	text := boom["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "it broke") {
		t.Errorf("failing tool text = %q", text)
	}

	ghost := responses[1]["error"].(map[string]any)
	if ghost["code"].(float64) != codeInvalidParams {
		t.Errorf("unknown tool code = %v, want invalid params", ghost["code"])
	}
	missing := responses[2]["error"].(map[string]any)
	if missing["code"].(float64) != codeMethodNotFound {
		t.Errorf("unknown method code = %v, want method not found", missing["code"])
	}
	parse := responses[3]["error"].(map[string]any)
	if parse["code"].(float64) != codeParseError {
		t.Errorf("bad json code = %v, want parse error", parse["code"])
	}
}

// TestServeRecoversFromOversizedLine verifies a request past the size limit is
// rejected without ending the session and later requests still answer.
func TestServeRecoversFromOversizedLine(t *testing.T) {
	t.Parallel()
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` +
		strings.Repeat("x", 1<<20) + `"}}`
	ping := `{"jsonrpc":"2.0","id":2,"method":"ping"}`

	var out strings.Builder
	if err := testServer().Serve(context.Background(),
		strings.NewReader(big+"\n"+ping+"\n"), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	var sawTooLarge, sawPing bool
	scanner := bufio.NewScanner(strings.NewReader(out.String()))
	for scanner.Scan() {
		var resp map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
			t.Fatalf("decode response %q: %v", scanner.Text(), err)
		}
		if e, ok := resp["error"].(map[string]any); ok && e["code"].(float64) == codeParseError {
			sawTooLarge = true
		}
		if id, ok := resp["id"].(float64); ok && id == 2 && resp["result"] != nil {
			sawPing = true
		}
	}
	if !sawTooLarge {
		t.Error("want a parse error for the oversized request")
	}
	if !sawPing {
		t.Error("want the session to answer the ping after the oversized request")
	}
}
