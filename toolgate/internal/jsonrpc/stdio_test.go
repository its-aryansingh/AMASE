package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func serve(t *testing.T, input string, h Handler) (string, string) {
	t.Helper()
	var out, logs bytes.Buffer
	conn := NewConn(strings.NewReader(input), &out, &logs)
	if err := conn.Serve(context.Background(), h); err != nil {
		t.Fatalf("serve: %v", err)
	}
	return out.String(), logs.String()
}

func echoHandler(_ context.Context, req *Request) *Response {
	if req.IsNotification() {
		return nil
	}
	return NewResult(req.ID, map[string]string{"method": req.Method})
}

// TestNotificationsAreNotAnswered is the bug this package's id handling exists
// to prevent: replying to a notification desynchronises the stream, and the
// client usually reports it as an unrelated failure much later.
func TestNotificationsAreNotAnswered(t *testing.T) {
	in := `{"jsonrpc":"2.0","method":"notifications/progress","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	out, _ := serve(t, in, echoHandler)
	if n := strings.Count(strings.TrimSpace(out), "\n") + 1; n != 1 {
		t.Fatalf("expected exactly one response, got %d:\n%s", n, out)
	}
}

func TestIDShapeIsPreserved(t *testing.T) {
	cases := map[string]string{
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`:     `"id":7`,
		`{"jsonrpc":"2.0","id":"abc","method":"ping"}`: `"id":"abc"`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`:  `"id":null`,
	}
	for in, want := range cases {
		out, _ := serve(t, in+"\n", echoHandler)
		if !strings.Contains(out, want) {
			t.Errorf("input %s: expected %s in %s", in, want, out)
		}
	}
}

func TestMalformedFrameGetsParseError(t *testing.T) {
	out, _ := serve(t, "{not json}\n", echoHandler)
	var resp Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("response was not JSON: %v", err)
	}
	if resp.Err == nil || resp.Err.Code != CodeParseError {
		t.Fatalf("expected a parse error, got %+v", resp.Err)
	}
}

func TestLargeFrameIsRead(t *testing.T) {
	// Well past bufio's default 64 KiB token limit, which is why this package
	// does not use bufio.Scanner.
	big := strings.Repeat("x", 512<<10)
	in := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"blob":"` + big + `"}}` + "\n"
	out, _ := serve(t, in, echoHandler)
	if !strings.Contains(out, `"id":1`) {
		t.Fatalf("large frame was not handled: %s", truncate([]byte(out)))
	}
}

func TestHandlerResponseToNotificationIsDropped(t *testing.T) {
	bad := func(_ context.Context, req *Request) *Response {
		return NewResult(NewNumberID(1), "oops") // wrong: answers everything
	}
	out, logs := serve(t, `{"jsonrpc":"2.0","method":"notify"}`+"\n", bad)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("a response leaked for a notification: %s", out)
	}
	if !strings.Contains(logs, "notification") {
		t.Errorf("the drop was not logged: %s", logs)
	}
}
