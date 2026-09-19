package mcp

import "encoding/json"

// ProtocolVersion is the MCP revision this server implements.
//
// A caveat worth keeping in the source rather than a changelog: the wire shapes
// in this file were written against the 2026-07-28 release notes, and the exact
// spelling of the _meta envelope fields should be diffed against the published
// schema before anyone calls this compatible. Everything version-specific is
// deliberately confined to this one file so that correction is a single-file
// change rather than a refactor.
const ProtocolVersion = "2026-07-28"

// Method names. 2026-07-28 dropped the initialize/initialized handshake, so
// there is nothing to answer before tools/list -- a fresh client may call any
// of these as its first message.
const (
	MethodToolsList      = "tools/list"
	MethodToolsCall      = "tools/call"
	MethodServerDiscover = "server/discover"
	MethodPing           = "ping"
)

// Meta is the per-request envelope that replaced session state. Because every
// request carries it, a server behind a round-robin load balancer needs no
// shared store -- which was the point of the rewrite.
type Meta struct {
	ProtocolVersion string          `json:"protocolVersion,omitempty"`
	Client          *ClientInfo     `json:"client,omitempty"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
}

// ClientInfo identifies the calling agent. It is self-asserted, so it is useful
// for journal attribution and for binding state handles, and useless as an
// authorisation input. toolgate treats it as the former only.
type ClientInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// Principal returns the identity a state handle should be bound to.
func (m *Meta) Principal() string {
	if m == nil || m.Client == nil || m.Client.Name == "" {
		return "anonymous"
	}
	return m.Client.Name
}

// Implementation describes this server.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// Tool is one entry in the tool catalogue.
type Tool struct {
	Name         string           `json:"name"`
	Title        string           `json:"title,omitempty"`
	Description  string           `json:"description"`
	InputSchema  json.RawMessage  `json:"inputSchema"`
	OutputSchema json.RawMessage  `json:"outputSchema,omitempty"`
	Annotations  *ToolAnnotations `json:"annotations,omitempty"`
}

// ToolAnnotations are behaviour hints.
//
// ReadOnlyHint is the field that makes this whole project necessary. goose reads
// it in PermissionInspector.apply_tool_annotations and, when it is absent, falls
// back to asking a language model whether the call looks read-only. A hint is a
// claim by the server about itself, so a hostile server sets read_only_hint on
// its "delete everything" tool and is believed. toolgate sets it honestly on its
// own tools and, separately, computes the same property for other people's tools
// from the command text -- which is the part that cannot be lied about.
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// ListToolsParams is the request body for tools/list.
type ListToolsParams struct {
	Meta   *Meta  `json:"_meta,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// ListToolsResult carries the catalogue plus the caching directives 2026-07-28
// added. TTLMs and CacheScope let a gateway cache the catalogue; "session" scope
// means a cached copy must not be shared between clients, which matters here
// because the catalogue is identical for everyone and the honest answer is that
// it can be shared.
type ListToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
	TTLMs      int64  `json:"ttlMs,omitempty"`
	CacheScope string `json:"cacheScope,omitempty"`
}

// CallToolParams is the request body for tools/call.
type CallToolParams struct {
	Meta      *Meta           `json:"_meta,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// Content is one piece of a tool result.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// TextContent is the only content kind toolgate emits.
func TextContent(s string) Content { return Content{Type: "text", Text: s} }

// CallToolResult is the tool/call response.
//
// IsError true is not a protocol error: the call succeeded and the tool is
// reporting a domain failure, which the model is expected to read and react to.
// A denied command is exactly that -- the audit ran correctly and the answer was
// no -- so a denial comes back here with IsError set, not as a JSON-RPC error.
// Returning a protocol error instead is a common bug: many clients drop the
// payload of an error response, so the model never learns why it was stopped and
// retries the same command.
type CallToolResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

// DiscoverResult answers the optional server/discover RPC.
type DiscoverResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ServerInfo      Implementation `json:"serverInfo"`
	Capabilities    map[string]any `json:"capabilities"`
	Instructions    string         `json:"instructions,omitempty"`
}
