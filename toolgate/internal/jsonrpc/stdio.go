package jsonrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MaxMessageBytes caps a single inbound line. A tool-call payload carrying a
// large file is legitimate, so this is generous; it exists to stop a runaway or
// hostile client from turning the read loop into an allocator.
const MaxMessageBytes = 16 << 20 // 16 MiB

// Handler answers one request. Returning a nil Response for a notification is
// correct and expected; returning one for a call is a bug the Conn will catch.
type Handler func(ctx context.Context, req *Request) *Response

// Conn is a newline-delimited JSON-RPC connection, which is what MCP's stdio
// transport is.
//
// Dispatch is deliberately serial. It would be easy to spawn a goroutine per
// request and the throughput would look better on a chart, but this server
// writes an order-dependent hash chain to its journal: if two tool calls are
// inspected concurrently their journal order stops matching the order the agent
// actually issued them, and `toolgate replay` then reconstructs a history that
// never happened. Auditability beats concurrency here, and the work per request
// is microseconds anyway.
type Conn struct {
	in  *bufio.Reader
	out io.Writer
	log io.Writer

	writeMu sync.Mutex
}

// NewConn wraps a reader and writer. log receives human-readable diagnostics and
// must never be the same stream as out -- on stdio transport, anything written
// to stdout that is not a JSON-RPC message corrupts the session. That mistake is
// the single most common way a hand-written MCP server fails, usually via a
// stray fmt.Println left in a handler.
func NewConn(in io.Reader, out, log io.Writer) *Conn {
	return &Conn{in: bufio.NewReaderSize(in, 64<<10), out: out, log: log}
}

// Serve reads messages until EOF or ctx is done.
func (c *Conn) Serve(ctx context.Context, h Handler) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		line, err := c.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			// A malformed frame has no usable id, so the reply carries null --
			// which is what the spec says to do, and is also why RequestID has
			// to be able to represent an explicit null distinctly from absent.
			c.write(NewError(RequestID{raw: json.RawMessage("null"), present: true},
				Errorf(CodeParseError, "%v", err)))
			continue
		}

		resp := h(ctx, &req)

		if req.IsNotification() {
			if resp != nil {
				fmt.Fprintf(c.log, "toolgate: handler returned a response for notification %q; dropped\n", req.Method)
			}
			continue
		}
		if resp == nil {
			resp = NewError(req.ID, Errorf(CodeInternalError, "handler produced no response for %q", req.Method))
		}
		if err := c.write(resp); err != nil {
			return err
		}
	}
}

// readLine returns one framed message without its terminating newline.
//
// bufio.Scanner is the obvious choice and the wrong one: it caps tokens at
// 64 KiB by default and, worse, reports the overflow as a plain "token too long"
// that is easy to mistake for a protocol error. Reading explicitly lets an
// oversized frame be named as such.
func (c *Conn) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := c.in.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > MaxMessageBytes {
			return nil, fmt.Errorf("jsonrpc: message exceeds %d bytes", MaxMessageBytes)
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(buf) > 0 {
			break // last line without a trailing newline
		}
		return nil, err
	}
	return trimEOL(buf), nil
}

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (c *Conn) write(resp *Response) error {
	resp.JSONRPC = Version
	b, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("jsonrpc: marshalling response: %w", err)
	}
	// json.Marshal escapes control characters, so b cannot contain a raw
	// newline and the framing is safe. Asserting it anyway costs nothing and
	// would catch a future change to a custom marshaller.
	for _, ch := range b {
		if ch == '\n' {
			return errors.New("jsonrpc: encoded response contains a newline")
		}
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.out.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("jsonrpc: writing response: %w", err)
	}
	return nil
}

// Logf writes a diagnostic to the log stream.
func (c *Conn) Logf(format string, args ...any) {
	fmt.Fprintf(c.log, "toolgate: "+format+"\n", args...)
}
