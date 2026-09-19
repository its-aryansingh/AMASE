package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/its-aryansingh/toolgate/internal/jsonrpc"
)

// ToolFunc executes one tool. meta is the per-request envelope; args is the raw
// arguments object, left unparsed so each tool owns its own schema.
type ToolFunc func(ctx context.Context, meta *Meta, args json.RawMessage) (*CallToolResult, error)

type registration struct {
	tool Tool
	fn   ToolFunc
}

// Server routes JSON-RPC methods to registered tools.
type Server struct {
	info   Implementation
	signer *HandleSigner
	instr  string

	mu    sync.RWMutex
	tools map[string]registration
}

// NewServer builds a server. The signer is shared with tools that mint handles.
func NewServer(info Implementation, signer *HandleSigner, instructions string) *Server {
	return &Server{
		info:   info,
		signer: signer,
		instr:  instructions,
		tools:  make(map[string]registration),
	}
}

// Signer exposes the handle signer to tool implementations.
func (s *Server) Signer() *HandleSigner { return s.signer }

// Register adds a tool. Registering the same name twice is a programming error
// and panics at startup rather than silently shadowing, because the failure mode
// -- a security tool quietly replaced by a later registration -- is not one to
// discover in production.
func (s *Server) Register(t Tool, fn ToolFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tools[t.Name]; exists {
		panic(fmt.Sprintf("mcp: tool %q registered twice", t.Name))
	}
	s.tools[t.Name] = registration{tool: t, fn: fn}
}

// Handle dispatches one JSON-RPC message.
func (s *Server) Handle(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	switch req.Method {
	case MethodToolsList:
		return s.handleList(req)
	case MethodToolsCall:
		return s.handleCall(ctx, req)
	case MethodServerDiscover:
		return s.handleDiscover(req)
	case MethodPing:
		if req.IsNotification() {
			return nil
		}
		return jsonrpc.NewResult(req.ID, struct{}{})
	default:
		if req.IsNotification() {
			// Unknown notifications are ignored by design. The spec's
			// forward-compatibility rule is that a peer may send notifications
			// you have never heard of, and answering them is worse than
			// dropping them.
			return nil
		}
		return jsonrpc.NewError(req.ID,
			jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "unknown method %q", req.Method))
	}
}

func (s *Server) handleList(req *jsonrpc.Request) *jsonrpc.Response {
	if req.IsNotification() {
		return nil
	}
	var params ListToolsParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return jsonrpc.NewError(req.ID,
				jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err))
		}
	}

	s.mu.RLock()
	out := make([]Tool, 0, len(s.tools))
	for _, r := range s.tools {
		out = append(out, r.tool)
	}
	s.mu.RUnlock()

	// Stable order. Map iteration in Go is randomised, so an unsorted catalogue
	// would come back in a different order on every call -- which breaks client
	// caches, breaks golden-file tests, and quietly changes the prompt the model
	// sees between runs. A tool whose whole claim is determinism does not get to
	// be non-deterministic about its own catalogue.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return jsonrpc.NewResult(req.ID, ListToolsResult{
		Tools:      out,
		TTLMs:      300_000,
		CacheScope: "server",
	})
}

func (s *Server) handleDiscover(req *jsonrpc.Request) *jsonrpc.Response {
	if req.IsNotification() {
		return nil
	}
	return jsonrpc.NewResult(req.ID, DiscoverResult{
		ProtocolVersion: ProtocolVersion,
		ServerInfo:      s.info,
		Capabilities:    map[string]any{"tools": map[string]any{}},
		Instructions:    s.instr,
	})
}

func (s *Server) handleCall(ctx context.Context, req *jsonrpc.Request) *jsonrpc.Response {
	if req.IsNotification() {
		return nil
	}
	var params CallToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return jsonrpc.NewError(req.ID,
			jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "%v", err))
	}

	s.mu.RLock()
	reg, ok := s.tools[params.Name]
	s.mu.RUnlock()
	if !ok {
		return jsonrpc.NewError(req.ID,
			jsonrpc.Errorf(jsonrpc.CodeMethodNotFound, "unknown tool %q", params.Name))
	}

	result, err := reg.fn(ctx, params.Meta, params.Arguments)
	if err != nil {
		// A handler error is an internal fault, distinct from a tool that ran
		// and decided no. The latter comes back as a result with IsError set.
		return jsonrpc.NewError(req.ID,
			jsonrpc.Errorf(jsonrpc.CodeInternalError, "tool %q: %v", params.Name, err))
	}
	return jsonrpc.NewResult(req.ID, result)
}

// Errorf builds a tool-level failure result: the call completed, the answer is
// no, and the model should read the text and adjust.
func Errorf(format string, args ...any) *CallToolResult {
	return &CallToolResult{
		Content: []Content{TextContent(fmt.Sprintf(format, args...))},
		IsError: true,
	}
}

// Result builds a success result carrying both a human-readable rendering and
// the structured payload.
func Result(text string, structured any) (*CallToolResult, error) {
	res := &CallToolResult{Content: []Content{TextContent(text)}}
	if structured != nil {
		b, err := json.Marshal(structured)
		if err != nil {
			return nil, fmt.Errorf("marshalling structured content: %w", err)
		}
		res.StructuredContent = b
	}
	return res, nil
}

// Bool is a helper for the pointer-shaped annotation hints, where absent and
// false mean different things: absent sends goose to its language-model judge,
// false is an assertion.
func Bool(b bool) *bool { return &b }
