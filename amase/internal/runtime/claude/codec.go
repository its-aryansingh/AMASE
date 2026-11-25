// Package claude drives Claude Code through its stream-json duplex protocol:
// `claude -p --input-format stream-json --output-format stream-json` with
// `--permission-prompt-tool stdio`, so every permission prompt arrives as a
// can_use_tool control request that amase answers. codec.go frames the NDJSON
// stream; control.go runs the protocol over it; process.go owns the child.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Frame is one line Claude Code wrote. Only the fields used for routing are
// decoded here; the payload fields stay raw until a handler needs them.
type Frame struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype,omitempty"`
	RequestID       string          `json:"request_id,omitempty"` // control_request, control_cancel_request
	SessionID       string          `json:"session_id,omitempty"`
	ParentToolUseID *string         `json:"parent_tool_use_id,omitempty"`
	Request         json.RawMessage `json:"request,omitempty"`  // control_request
	Response        json.RawMessage `json:"response,omitempty"` // control_response
	Message         json.RawMessage `json:"message,omitempty"`  // assistant, user
	Raw             json.RawMessage `json:"-"`                  // the whole line, without its newline
}

// SyntaxError reports a line that is not a JSON object. The decoder stays
// usable: call Next again for the following line.
type SyntaxError struct {
	Line []byte // up to the first 256 bytes of the offending line
	Err  error
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("stream-json: malformed line %q: %v", e.Line, e.Err)
}

func (e *SyntaxError) Unwrap() error { return e.Err }

// Decoder reads frames from Claude Code's stdout.
type Decoder struct {
	r *bufio.Reader
}

// NewDecoder returns a decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next frame. Blank lines are skipped; a line that is not a
// JSON object yields a *SyntaxError; frame types this package does not know
// are returned like any other frame, for the caller to ignore. A final line
// without a newline is still returned. At the end of the stream Next returns
// io.EOF.
func (d *Decoder) Next() (Frame, error) {
	for {
		// ReadBytes, not bufio.Scanner: one line carries a whole tool result or
		// file and routinely exceeds Scanner's 64 KiB token limit.
		line, err := d.r.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			f, perr := parseFrame(line)
			if perr != nil {
				return Frame{}, perr
			}
			return f, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return Frame{}, io.EOF
			}
			return Frame{}, fmt.Errorf("stream-json: read: %w", err)
		}
	}
}

func parseFrame(line []byte) (Frame, error) {
	var f Frame
	if line[0] != '{' {
		return Frame{}, &SyntaxError{Line: prefix(line), Err: errors.New("not a JSON object")}
	}
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, &SyntaxError{Line: prefix(line), Err: err}
	}
	f.Raw = line
	return f, nil
}

func prefix(b []byte) []byte {
	const n = 256
	if len(b) > n {
		b = b[:n]
	}
	return append([]byte(nil), b...)
}

// Outbound is a message amase writes to Claude Code's stdin.
type Outbound interface {
	outbound()
}

// UserMessage starts a turn (C1).
type UserMessage struct {
	Type            string      `json:"type"` // "user"
	SessionID       string      `json:"session_id"`
	ParentToolUseID *string     `json:"parent_tool_use_id"`
	Message         UserContent `json:"message"`
}

// UserContent is the body of a UserMessage.
type UserContent struct {
	Role    string `json:"role"` // "user"
	Content string `json:"content"`
}

// NewUserMessage returns the frame that sends prompt as the next user turn.
func NewUserMessage(sessionID, prompt string) UserMessage {
	return UserMessage{Type: "user", SessionID: sessionID, Message: UserContent{Role: "user", Content: prompt}}
}

// ControlRequest is a request amase sends to Claude Code: initialize (C5) or
// interrupt (C4).
type ControlRequest struct {
	Type      string          `json:"type"` // "control_request"
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}

var (
	initializeBody = json.RawMessage(`{"subtype":"initialize","hooks":null}`)
	interruptBody  = json.RawMessage(`{"subtype":"interrupt"}`)
)

// NewInitialize returns the initialize handshake request (C5).
func NewInitialize(requestID string) ControlRequest {
	return ControlRequest{Type: "control_request", RequestID: requestID, Request: initializeBody}
}

// NewInterrupt returns the request that stops the running turn (C4).
func NewInterrupt(requestID string) ControlRequest {
	return ControlRequest{Type: "control_request", RequestID: requestID, Request: interruptBody}
}

// ControlResponse answers a control request Claude Code sent.
type ControlResponse struct {
	Type     string              `json:"type"` // "control_response"
	Response ControlResponseBody `json:"response"`
}

// ControlResponseBody carries either a success payload or an error.
type ControlResponseBody struct {
	Subtype   string            `json:"subtype"` // "success" | "error"
	RequestID string            `json:"request_id"`
	Response  *PermissionResult `json:"response,omitempty"`
	Error     string            `json:"error,omitempty"`
	ErrorCode string            `json:"error_code,omitempty"`
}

// PermissionResult is the answer to can_use_tool (C3): allow echoes the input
// as updatedInput; deny carries the message the model sees.
type PermissionResult struct {
	Behavior     string          `json:"behavior"` // "allow" | "deny"
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Message      string          `json:"message,omitempty"`
}

// NewPermissionResponse returns the control_response for a can_use_tool request.
func NewPermissionResponse(requestID string, r PermissionResult) ControlResponse {
	return ControlResponse{Type: "control_response", Response: ControlResponseBody{
		Subtype: "success", RequestID: requestID, Response: &r}}
}

// NewErrorResponse returns an error control_response, in the shape Claude
// Code itself uses for a subtype it does not support.
func NewErrorResponse(requestID, code, msg string) ControlResponse {
	return ControlResponse{Type: "control_response", Response: ControlResponseBody{
		Subtype: "error", RequestID: requestID, Error: msg, ErrorCode: code}}
}

func (UserMessage) outbound()     {}
func (ControlRequest) outbound()  {}
func (ControlResponse) outbound() {}

// Encoder writes frames to Claude Code's stdin. It is safe for concurrent use:
// permission replies, turns and interrupts come from different goroutines, and
// each line goes out in a single Write so lines never interleave.
type Encoder struct {
	mu sync.Mutex
	w  io.Writer
}

// NewEncoder returns an encoder writing to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// Encode writes m as one NDJSON line.
func (e *Encoder) Encode(m Outbound) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// Keep `>` and `&` literal so updatedInput echoes the command as Claude
	// Code sent it rather than as > escapes.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("stream-json: encode %T: %w", m, err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("stream-json: write: %w", err)
	}
	return nil
}
