package mcp

import (
	"encoding/json"
	"io"

	"github.com/nlink-jp/rdns-lookup/internal/config"
	"github.com/nlink-jp/rdns-lookup/internal/engine"
)

// defaultProtocolVersion is advertised when the client sends none.
const defaultProtocolVersion = "2025-06-18"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent/null ⇒ notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// toolResult is the tools/call payload.
type toolResult struct {
	Content []contentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func textResult(isErr bool, text string) toolResult {
	return toolResult{Content: []contentItem{{Type: "text", Text: text}}, IsError: isErr}
}

// server holds the shared lookup engine; every tool call goes through it, so
// MCP and CLI behaviour cannot diverge.
type server struct {
	e       *engine.Engine
	cfg     *config.Config
	version string
}

// Serve runs the MCP protocol loop until in reaches EOF. It is safe to point in
// at os.Stdin and out at os.Stdout; diagnostics must go to stderr only.
func Serve(e *engine.Engine, cfg *config.Config, version string, in io.Reader, out io.Writer) error {
	s := &server{e: e, cfg: cfg, version: version}
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	for {
		var req request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			// The stream position is unrecoverable after malformed JSON;
			// report a parse error and stop rather than spin on the same bytes.
			_ = enc.Encode(response{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			return nil
		}
		resp, skip := s.handle(&req)
		if skip {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

func (s *server) handle(req *request) (response, bool) {
	// A missing or null id marks a JSON-RPC notification: never reply.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		return response{}, true
	}
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, s.initializeResult(req.Params)), false
	case "ping":
		return s.ok(req.ID, struct{}{}), false
	case "tools/list":
		return s.ok(req.ID, toolsList()), false
	case "tools/call":
		res, rerr := s.toolsCall(req.Params)
		if rerr != nil {
			return response{JSONRPC: "2.0", ID: req.ID, Error: rerr}, false
		}
		return s.ok(req.ID, res), false
	default:
		return response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}, false
	}
}

func (s *server) ok(id json.RawMessage, result any) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *server) initializeResult(params json.RawMessage) any {
	pv := defaultProtocolVersion
	if len(params) > 0 {
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(params, &p) == nil && p.ProtocolVersion != "" {
			pv = p.ProtocolVersion
		}
	}
	return map[string]any{
		"protocolVersion": pv,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "rdns-lookup", "version": s.version},
		"instructions":    Instructions,
	}
}
