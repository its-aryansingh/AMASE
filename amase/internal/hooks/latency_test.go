package hooks

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/its-aryansingh/amase/internal/event"
	"github.com/its-aryansingh/amase/internal/policy"
)

// gateCall is one PreToolUse request in the latency-gate mix.
type gateCall struct {
	class string // read | benign | risky
	body  []byte
	want  string // the permissionDecision the policy must give
}

// gateMix builds the SPEC 4.15 payload mix from the golden PreToolUse payload:
// 70 % read tools, 20 % benign Bash, 10 % risky Bash, in a fixed shuffled
// order of 100 calls that the gate cycles through.
func gateMix(t testing.TB, root string) []gateCall {
	t.Helper()
	base := loadGolden(t)[0].Payload
	mk := func(class, tool string, input map[string]string, want string, n int) gateCall {
		return gateCall{class: class, want: want, body: withFields(t, base, map[string]any{
			"tool_name": tool, "tool_input": input, "tool_use_id": "toolu_gate_" + class + "_" + string(rune('a'+n)),
		})}
	}
	reads := []gateCall{
		mk("read", "Read", map[string]string{"file_path": filepath.Join(root, "calc", "calc.go")}, "allow", 0),
		mk("read", "Glob", map[string]string{"pattern": "**/*.go"}, "allow", 1),
		mk("read", "Grep", map[string]string{"pattern": "func Add", "path": root}, "allow", 2),
		mk("read", "Read", map[string]string{"file_path": filepath.Join(root, "README.md")}, "allow", 3),
	}
	benign := []gateCall{
		mk("benign", "Bash", map[string]string{"command": "echo amase-probe > probe.txt"}, "allow", 0),
		mk("benign", "Bash", map[string]string{"command": "go test ./..."}, "allow", 1),
		mk("benign", "Bash", map[string]string{"command": "git status"}, "allow", 2),
		mk("benign", "Bash", map[string]string{"command": "ls -la calc"}, "allow", 3),
	}
	var risky []gateCall
	for i, cmd := range downloadAndExecute {
		risky = append(risky, mk("risky", "Bash", map[string]string{"command": cmd}, "deny", i))
	}

	mix := make([]gateCall, 0, 100)
	for i := 0; i < 70; i++ {
		mix = append(mix, reads[i%len(reads)])
	}
	for i := 0; i < 20; i++ {
		mix = append(mix, benign[i%len(benign)])
	}
	for i := 0; i < 10; i++ {
		mix = append(mix, risky[i%len(risky)])
	}
	r := rand.New(rand.NewPCG(4, 15))
	r.Shuffle(len(mix), func(i, j int) { mix[i], mix[j] = mix[j], mix[i] })
	return mix
}

// queueRecorder stands in for the store on the hot path: a non-blocking send
// onto a bounded queue that another goroutine drains. The journal writer
// itself is on the warm path (SPEC 4.4) and is not part of the gate.
type queueRecorder struct {
	q       chan any
	dropped atomic.Int64
}

func newQueueRecorder(t testing.TB) *queueRecorder {
	r := &queueRecorder{q: make(chan any, 8192)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range r.q {
		}
	}()
	t.Cleanup(func() { close(r.q); <-done })
	return r
}

func (r *queueRecorder) put(v any) bool {
	select {
	case r.q <- v:
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

func (r *queueRecorder) Emit(e event.Envelope) bool { return r.put(e) }
func (r *queueRecorder) Receipt(v any) bool         { return r.put(v) }

func gateServer(t testing.TB) (http.Handler, []gateCall, *queueRecorder) {
	t.Helper()
	rec := newQueueRecorder(t)
	srv, err := New(Options{Engine: policy.NewEngine(), Recorder: rec})
	if err != nil {
		t.Fatal(err)
	}
	ws := policy.Workspace{Root: t.TempDir(), Home: t.TempDir()}
	if err := srv.Register(testToken, Binding{Repo: "repo1", Task: "task1", Session: "ses1", Workspace: ws, Turn: func() int { return 1 }}); err != nil {
		t.Fatal(err)
	}
	return srv.Handler(), gateMix(t, ws.Root), rec
}

const gatePath = "/hooks/claude/" + testToken

func newGateRequest(body []byte) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, gatePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req, httptest.NewRecorder()
}

// TestLatencyGateMix proves the gate measures real decisions: every payload
// in the mix gets the verdict its class implies.
func TestLatencyGateMix(t *testing.T) {
	h, mix, _ := gateServer(t)
	counts := map[string]int{}
	for _, c := range mix {
		counts[c.class]++
		req, w := newGateRequest(c.body)
		h.ServeHTTP(w, req)
		var out output
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s: status %d body %s", c.class, w.Code, w.Body)
		}
		if got := out.HookSpecificOutput.PermissionDecision; got != c.want {
			t.Errorf("%s call %s: decision %q, want %q", c.class, c.body, got, c.want)
		}
	}
	if counts["read"] != 70 || counts["benign"] != 20 || counts["risky"] != 10 {
		t.Errorf("mix = %v, want read 70 / benign 20 / risky 10", counts)
	}
}

var sinkReq *http.Request

// TestLatencyGate is the SPEC 4.15 gate: 10,000 warm-up calls, then 200,000
// timed PreToolUse calls through the in-process handler (no socket). It fails
// if p99 exceeds 1.0 ms. CI gates on ubuntu-latest; elsewhere the numbers are
// recorded, not trusted.
func TestLatencyGate(t *testing.T) {
	if os.Getenv("AMASE_LATENCY_GATE") != "1" {
		t.Skip("set AMASE_LATENCY_GATE=1 to run the hot-path latency gate")
	}
	const (
		warmup = 10_000
		timed  = 200_000
		limit  = time.Millisecond
	)
	h, mix, rec := gateServer(t)

	for i := 0; i < warmup; i++ {
		req, w := newGateRequest(mix[i%len(mix)].body)
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("warm-up call %d: status %d", i, w.Code)
		}
	}

	samples := make([]time.Duration, timed)
	var total time.Duration
	goruntime.GC()
	for i := range samples {
		req, w := newGateRequest(mix[i%len(mix)].body)
		start := time.Now()
		h.ServeHTTP(w, req)
		samples[i] = time.Since(start)
		total += samples[i]
	}
	slices.Sort(samples)
	pct := func(p float64) time.Duration { return samples[int(p*float64(len(samples)-1))] }
	p50, p95, p99, maxD := pct(0.50), pct(0.95), pct(0.99), samples[len(samples)-1]

	// Allocations of the handler alone: building the request and recorder is
	// measured separately and subtracted.
	i := 0
	withServe := testing.AllocsPerRun(5000, func() {
		req, w := newGateRequest(mix[i%len(mix)].body)
		h.ServeHTTP(w, req)
		i++
	})
	i = 0
	buildOnly := testing.AllocsPerRun(5000, func() {
		sinkReq, _ = newGateRequest(mix[i%len(mix)].body)
		i++
	})

	t.Logf("latency gate: %d calls (70%% read / 20%% benign Bash / 10%% risky Bash) on %s/%s %s, GOMAXPROCS=%d",
		timed, goruntime.GOOS, goruntime.GOARCH, goruntime.Version(), goruntime.GOMAXPROCS(0))
	t.Logf("p50 %v · p95 %v · p99 %v · max %v · mean %v · handler allocs/op %.0f · queue drops %d",
		p50, p95, p99, maxD, total/timed, withServe-buildOnly, rec.dropped.Load())
	if zeros, _ := slices.BinarySearch(samples, 1); zeros > 0 {
		t.Logf("%d of %d samples read 0: this machine's monotonic clock is coarser than one call, so the percentiles are quantized and only the mean is exact", zeros, timed)
	}
	if goruntime.GOOS == "windows" {
		t.Logf("not gated on Windows (SPEC 4.15): numbers recorded only")
		return
	}
	if p99 > limit {
		t.Fatalf("p99 %v exceeds the %v budget", p99, limit)
	}
}

func BenchmarkPreToolUse(b *testing.B) {
	h, mix, _ := gateServer(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, w := newGateRequest(mix[i%len(mix)].body)
		h.ServeHTTP(w, req)
	}
}
