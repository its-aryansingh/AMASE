// Package fake plays a harness's side of a recorded wire trace, so the
// protocol adapters are tested against frames a real CLI sent instead of
// against anyone's memory of the protocol. A Peer is the harness end of a
// duplex NDJSON stream: in-memory pipes from Pipe, or a real process's stdio
// when a test binary stands in for the CLI.
package fake

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
)

// Directions of a recorded frame, as written in the golden traces.
const (
	In  = "in"  // amase → harness
	Out = "out" // harness → amase
)

// Step is one recorded frame.
type Step struct {
	Dir string          `json:"dir"`
	Msg json.RawMessage `json:"msg"`
}

// ParseTrace reads a trace: one {"dir","msg"} object per line. Other fields
// (such as a recorded timestamp) are ignored.
func ParseTrace(r io.Reader) ([]Step, error) {
	br := bufio.NewReader(r)
	var steps []Step
	for n := 1; ; n++ {
		// ReadBytes, not bufio.Scanner: recorded frames exceed 64 KiB.
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			var s Step
			if jerr := json.Unmarshal(line, &s); jerr != nil {
				return nil, fmt.Errorf("fake: trace line %d: %w", n, jerr)
			}
			if s.Dir != In && s.Dir != Out {
				return nil, fmt.Errorf("fake: trace line %d: dir %q is neither %q nor %q", n, s.Dir, In, Out)
			}
			steps = append(steps, s)
		}
		if errors.Is(err, io.EOF) {
			return steps, nil
		}
		if err != nil {
			return nil, fmt.Errorf("fake: read trace: %w", err)
		}
	}
}

// LoadTrace reads the trace file at path.
func LoadTrace(path string) ([]Step, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("fake: %w", err)
	}
	defer f.Close()
	return ParseTrace(f)
}

// Peer is the harness end of a duplex stream. It writes what the harness
// would print and collects, line by line, what the adapter sends.
type Peer struct {
	toAdapter   io.WriteCloser
	fromAdapter io.Closer

	writeMu sync.Mutex
	lines   chan []byte
	readErr error // set before lines is closed
}

// NewPeer returns a Peer that reads the adapter's output from fromAdapter and
// writes harness output to toAdapter. A goroutine buffers the adapter's lines
// so its writes never wait for the test to look at them.
func NewPeer(fromAdapter io.ReadCloser, toAdapter io.WriteCloser) *Peer {
	p := &Peer{toAdapter: toAdapter, fromAdapter: fromAdapter, lines: make(chan []byte, 1024)}
	go p.read(fromAdapter)
	return p
}

// Pipe returns a connected in-memory stream. The adapter reads stdout (what
// the harness prints) and writes stdin; the Peer plays the harness.
func Pipe() (stdout io.ReadCloser, stdin io.WriteCloser, peer *Peer) {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	return outR, inW, NewPeer(inR, outW)
}

func (p *Peer) read(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if line = bytes.TrimRight(line, "\r\n"); len(line) > 0 {
			p.lines <- line
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				p.readErr = err
			}
			close(p.lines)
			return
		}
	}
}

// Send writes one line to the adapter, adding the newline.
func (p *Peer) Send(line []byte) error {
	buf := make([]byte, 0, len(line)+1)
	buf = append(append(buf, line...), '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.toAdapter.Write(buf); err != nil {
		return fmt.Errorf("fake: send: %w", err)
	}
	return nil
}

// Recv returns the next line the adapter wrote, without its newline. It
// returns io.EOF once the adapter has closed its end and every line has been
// received.
func (p *Peer) Recv(ctx context.Context) ([]byte, error) {
	select {
	case l, ok := <-p.lines:
		if !ok {
			if p.readErr != nil {
				return nil, fmt.Errorf("fake: recv: %w", p.readErr)
			}
			return nil, io.EOF
		}
		return l, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CloseSend ends the harness output: the adapter reads EOF.
func (p *Peer) CloseSend() error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return p.toAdapter.Close()
}

// Close closes both directions.
func (p *Peer) Close() error {
	return errors.Join(p.CloseSend(), p.fromAdapter.Close())
}

// Options tunes Replay.
type Options struct {
	// Exact requires each line the adapter writes to equal the recorded frame
	// byte for byte once the recording is compacted (recorders may pretty-print
	// or add spaces). Otherwise the two only need to be equal as JSON values.
	Exact bool
	// Replace rewrites every recorded line before it is sent or compared: ids
	// the adapter chooses itself (request ids, session ids) differ from the
	// recorded ones.
	Replace *strings.Replacer
	// Before, if set, is called before step i is played, so a test can make
	// the adapter act (send a turn, interrupt) at the moment the recording
	// shows it acting. It runs on Replay's goroutine and must not block on
	// anything Replay itself has yet to do.
	Before func(i int, s Step)
}

// MismatchError reports a line the adapter wrote that differs from the
// recording.
type MismatchError struct {
	Step      int
	Want, Got []byte
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("fake: step %d: adapter wrote\n  %s\nwant\n  %s", e.Step, clip(e.Got), clip(e.Want))
}

// Replay plays steps in order. An Out step is written to the adapter as
// recorded, so it may also be a deliberately malformed line. An In step waits
// for the adapter's next line and compares it with the recording. Replay
// returns at the first mismatch, when ctx ends, or after the last step.
func (p *Peer) Replay(ctx context.Context, steps []Step, opt Options) error {
	for i, s := range steps {
		if opt.Before != nil {
			opt.Before(i, s)
		}
		msg := []byte(s.Msg)
		if opt.Replace != nil {
			msg = []byte(opt.Replace.Replace(string(msg)))
		}
		if s.Dir == Out {
			if err := p.Send(msg); err != nil {
				return fmt.Errorf("fake: step %d: %w", i, err)
			}
			continue
		}
		got, err := p.Recv(ctx)
		if err != nil {
			return fmt.Errorf("fake: step %d: waiting for the adapter: %w", i, err)
		}
		if err := match(msg, got, opt.Exact); err != nil {
			return &MismatchError{Step: i, Want: msg, Got: got}
		}
	}
	return nil
}

func match(want, got []byte, exact bool) error {
	if exact {
		var compact bytes.Buffer
		if err := json.Compact(&compact, want); err != nil {
			return err
		}
		if !bytes.Equal(compact.Bytes(), got) {
			return errors.New("bytes differ")
		}
		return nil
	}
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		return err
	}
	if err := json.Unmarshal(got, &g); err != nil {
		return err
	}
	if !reflect.DeepEqual(w, g) {
		return errors.New("JSON values differ")
	}
	return nil
}

func clip(b []byte) string {
	const n = 600
	if len(b) > n {
		return fmt.Sprintf("%s…[+%d bytes]", b[:n], len(b)-n)
	}
	return string(b)
}
