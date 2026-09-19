// Package jsonrpc implements the subset of JSON-RPC 2.0 that MCP actually uses,
// with no dependencies outside the standard library.
//
// Rolling this by hand rather than importing the official SDK is a deliberate
// trade recorded in docs/DECISIONS.md. The short version: this is a security
// tool, and a security tool that drags in a dependency tree is an argument
// against itself. The whole protocol layer is about four hundred lines and the
// spec is stable enough to own.
//
// The one place hand-rolling genuinely helps correctness is id handling. JSON-RPC
// ids may be a string, a number, or null, and "null" is not the same as absent:
// absent means notification, and a notification must never be answered. Most
// naive implementations flatten id to a string and then cheerfully reply to
// notifications, which desynchronises the stream. Here RequestID keeps the raw
// bytes and remembers which shape it had.
package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Version is the only protocol version this package speaks.
const Version = "2.0"

// Standard JSON-RPC 2.0 error codes, plus the MCP-reserved range.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// CodeToolDenied is server-defined (the -32000..-32099 implementation range).
	// It is distinct from an execution failure: the tool did not run, on purpose.
	CodeToolDenied = -32001
)

// RequestID is a JSON-RPC id, preserving the distinction between a string id,
// a numeric id, an explicit null, and no id at all.
type RequestID struct {
	raw     json.RawMessage
	present bool
}

// NewStringID builds a string-shaped id. Used by tests and by client code.
func NewStringID(s string) RequestID {
	b, _ := json.Marshal(s)
	return RequestID{raw: b, present: true}
}

// NewNumberID builds a number-shaped id.
func NewNumberID(n int64) RequestID {
	b, _ := json.Marshal(n)
	return RequestID{raw: b, present: true}
}

// Present reports whether an id field was supplied at all. A request without
// one is a notification and must not be answered.
func (r RequestID) Present() bool { return r.present }

// IsNull reports whether the id was supplied as literal null.
func (r RequestID) IsNull() bool {
	return r.present && bytes.Equal(bytes.TrimSpace(r.raw), []byte("null"))
}

// String renders the id for logs. Not a protocol value -- never send this back.
func (r RequestID) String() string {
	if !r.present {
		return "<notification>"
	}
	return string(r.raw)
}

func (r RequestID) MarshalJSON() ([]byte, error) {
	if !r.present {
		return []byte("null"), nil
	}
	return r.raw, nil
}

func (r *RequestID) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	switch {
	case len(trimmed) == 0:
		*r = RequestID{}
		return nil
	case trimmed[0] == '"', trimmed[0] == '-', trimmed[0] >= '0' && trimmed[0] <= '9',
		bytes.Equal(trimmed, []byte("null")):
		dup := make(json.RawMessage, len(trimmed))
		copy(dup, trimmed)
		*r = RequestID{raw: dup, present: true}
		return nil
	default:
		return fmt.Errorf("jsonrpc: id must be string, number or null, got %s", truncate(trimmed))
	}
}

// Request is an incoming call or notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      RequestID       `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether this message expects no reply.
func (r *Request) IsNotification() bool { return !r.ID.Present() }

// UnmarshalJSON exists so that a missing id stays missing. encoding/json leaves
// the field untouched when the key is absent, which is exactly the behaviour we
// want, but only if RequestID's zero value means "absent" -- it does.
func (r *Request) UnmarshalJSON(b []byte) error {
	type alias Request
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*r = Request(a)
	if r.JSONRPC != Version {
		return fmt.Errorf("jsonrpc: unsupported version %q", r.JSONRPC)
	}
	if r.Method == "" {
		return fmt.Errorf("jsonrpc: missing method")
	}
	return nil
}

// Error is the JSON-RPC error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("jsonrpc %d: %s", e.Code, e.Message) }

// Errorf builds an Error with a formatted message and no data.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithData attaches a structured payload. Marshal failure is swallowed on
// purpose: an error response that cannot be built is worse than one missing its
// detail field, and this is on the path that reports other failures.
func (e *Error) WithData(v any) *Error {
	if b, err := json.Marshal(v); err == nil {
		e.Data = b
	}
	return e
}

// Response is an outgoing reply. Exactly one of Result and Err is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      RequestID       `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Err     *Error          `json:"error,omitempty"`
}

// NewResult builds a success response, marshalling v.
func NewResult(id RequestID, v any) *Response {
	b, err := json.Marshal(v)
	if err != nil {
		return NewError(id, Errorf(CodeInternalError, "marshalling result: %v", err))
	}
	return &Response{JSONRPC: Version, ID: id, Result: b}
}

// NewError builds a failure response.
func NewError(id RequestID, e *Error) *Response {
	return &Response{JSONRPC: Version, ID: id, Err: e}
}

func truncate(b []byte) string {
	const max = 64
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
