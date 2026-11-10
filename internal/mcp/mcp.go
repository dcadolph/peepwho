// Package mcp serves whotoask over the Model Context Protocol, so agent
// clients such as Claude Code and Claude Desktop can ask who knows what
// mid-conversation. The transport is stdio: one JSON-RPC 2.0 message per
// line on stdin and stdout, with stderr free for logs.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// protocolVersion is the MCP revision the server speaks when the client does
// not name one. The tool surface used here is stable across revisions.
const protocolVersion = "2025-06-18"

// maxLine bounds one inbound JSON-RPC message.
const maxLine = 1 << 20

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Tool describes one callable tool for tools/list.
type Tool struct {
	// Name is the tool's identifier.
	Name string `json:"name"`
	// Description tells the model when to call the tool.
	Description string `json:"description"`
	// InputSchema is the JSON Schema for the tool's arguments.
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Handler executes one tool call and returns the text payload for the
// client. An error becomes an isError tool result, not a protocol error.
type Handler func(ctx context.Context, args json.RawMessage) (string, error)

// Server is a minimal MCP stdio server.
type Server struct {
	// name and version identify the server in the initialize handshake.
	name, version string
	// tools lists the callable tools in registration order.
	tools []Tool
	// handlers maps a tool name to its implementation.
	handlers map[string]Handler
	// log receives notices; never the protocol stream.
	log io.Writer
}

// New returns a Server. It panics on an empty name.
func New(name, version string, log io.Writer) *Server {
	if name == "" {
		panic("mcp: New requires a server name")
	}
	if log == nil {
		log = io.Discard
	}
	return &Server{name: name, version: version, handlers: make(map[string]Handler), log: log}
}

// AddTool registers a tool and its handler. It panics on a nil handler or a
// duplicate name.
func (s *Server) AddTool(t Tool, h Handler) {
	if h == nil {
		panic("mcp: AddTool requires a handler")
	}
	if _, ok := s.handlers[t.Name]; ok {
		panic("mcp: duplicate tool " + t.Name)
	}
	s.tools = append(s.tools, t)
	s.handlers[t.Name] = h
}

// request is one inbound JSON-RPC message. A missing id marks a
// notification, which never gets a response.
type request struct {
	// JSONRPC is the protocol version marker, always "2.0".
	JSONRPC string `json:"jsonrpc"`
	// ID is the request identifier, echoed in the response.
	ID json.RawMessage `json:"id"`
	// Method is the RPC method name.
	Method string `json:"method"`
	// Params carries the method's parameters.
	Params json.RawMessage `json:"params"`
}

// errLineTooLong marks a request that exceeded maxLine. The session rejects it
// and keeps serving rather than tearing down over one oversized message.
var errLineTooLong = errors.New("mcp: request too large")

// Serve reads requests from r and writes responses to w until EOF. A client
// closing its end of the pipe ends the session.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, maxLine)
	for {
		line, err := readLine(br)
		if errors.Is(err, errLineTooLong) {
			s.writeError(w, nil, codeParseError, "request too large")
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("mcp: read: %w", err)
		}
		if line = bytes.TrimRight(line, "\r\n"); len(line) > 0 {
			s.handleLine(ctx, w, line)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

// handleLine parses one request line and dispatches it, answering a malformed
// line with a parse error and ignoring notifications.
func (s *Server) handleLine(ctx context.Context, w io.Writer, line []byte) {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(w, nil, codeParseError, "parse error")
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		// A notification: nothing to answer.
		return
	}
	s.dispatch(ctx, w, req)
}

// readLine reads one newline-terminated line. A line that overflows the
// reader's buffer is drained to its end and reported as errLineTooLong, so the
// caller can reject it and keep reading. The trailing newline is kept when
// present; a partial final line returns with io.EOF.
func readLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return line, err
	}
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = br.ReadSlice('\n')
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return nil, errLineTooLong
}

// dispatch answers one request.
func (s *Server) dispatch(ctx context.Context, w io.Writer, req request) {
	switch req.Method {
	case "initialize":
		s.writeResult(w, req.ID, s.initializeResult(req.Params))
	case "ping":
		s.writeResult(w, req.ID, struct{}{})
	case "tools/list":
		s.writeResult(w, req.ID, map[string]any{"tools": s.tools})
	case "tools/call":
		s.callTool(ctx, w, req)
	default:
		s.writeError(w, req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

// initializeResult builds the handshake reply, echoing the client's protocol
// version when it names one.
func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	version := protocolVersion
	var p struct {
		// ProtocolVersion is the revision the client wants to speak.
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
		version = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": s.name, "version": s.version},
	}
}

// callTool runs one tool and writes its result. Tool failures return an
// isError result so the model can read them; only an unknown tool or bad
// params are protocol errors.
func (s *Server) callTool(ctx context.Context, w io.Writer, req request) {
	var p struct {
		// Name is the tool to call.
		Name string `json:"name"`
		// Arguments carries the tool's input.
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		s.writeError(w, req.ID, codeInvalidParams, "tools/call needs a tool name")
		return
	}
	h, ok := s.handlers[p.Name]
	if !ok {
		s.writeError(w, req.ID, codeInvalidParams, "unknown tool: "+p.Name)
		return
	}
	text, err := h(ctx, p.Arguments)
	isError := err != nil
	if isError {
		text = err.Error()
		fmt.Fprintf(s.log, "whotoask mcp: %s: %v\n", p.Name, err)
	}
	s.writeResult(w, req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	})
}

// writeResult writes one JSON-RPC success response as a single line.
func (s *Server) writeResult(w io.Writer, id json.RawMessage, result any) {
	s.write(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// writeError writes one JSON-RPC error response as a single line.
func (s *Server) writeError(w io.Writer, id json.RawMessage, code int, message string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	s.write(w, map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}

// write serializes one message and terminates it with a newline.
func (s *Server) write(w io.Writer, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(s.log, "whotoask mcp: encode: %v\n", err)
		return
	}
	raw = append(raw, '\n')
	if _, err := w.Write(raw); err != nil {
		fmt.Fprintf(s.log, "whotoask mcp: write: %v\n", err)
	}
}
