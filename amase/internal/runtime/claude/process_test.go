package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/its-aryansingh/amase/internal/runtime"
	"github.com/its-aryansingh/amase/internal/runtime/fake"
)

// TestMain lets the test binary stand in for the claude CLI: a session under
// test spawns this binary with AMASE_FAKE_CLAUDE set.
func TestMain(m *testing.M) {
	if mode := os.Getenv("AMASE_FAKE_CLAUDE"); mode != "" {
		os.Exit(fakeClaude(mode))
	}
	os.Exit(m.Run())
}

// ---- argv, settings, env, launcher (unit) ------------------------------------

func TestArgvMatchesGolden(t *testing.T) {
	raw, err := readGolden("argv.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := launch{providerID: "<session-uuid>", settingsPath: "<settings.json>", model: "haiku", maxUSD: 1}.args()
	if !slices.Equal(got, want) {
		t.Fatalf("argv:\n got %q\nwant %q", got, want)
	}

	bare := launch{providerID: "id"}.args()
	for _, flag := range []string{"--settings", "--max-budget-usd", "--model", "--max-turns", "--resume"} {
		if slices.Contains(bare, flag) {
			t.Errorf("argv without options contains %s: %q", flag, bare)
		}
	}
	if got := (launch{providerID: "id", maxUSD: 0.25}).args(); !slices.Contains(got, "0.25") {
		t.Errorf("budget not formatted plainly: %q", got)
	}
}

func TestDefaultSettingsMatchGolden(t *testing.T) {
	want, err := readGolden("hook-settings.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := defaultSettings("http://127.0.0.1:<port>/hooks/claude/probe-token")
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, want)
}

func TestCheckHookURL(t *testing.T) {
	const token = "s3cret-token"
	cases := []struct {
		url string
		ok  bool
	}{
		{"http://127.0.0.1:7337/hooks/claude/" + token, true},
		{"http://localhost:7337/hooks/claude/" + token, true},
		{"http://[::1]:7337/hooks/claude/" + token, true},
		{"https://127.0.0.1:7337/hooks/claude/" + token, false},
		{"http://example.com/hooks/claude/" + token, false},
		{"http://10.0.0.1:7337/hooks/claude/" + token, false},
		{"127.0.0.1:7337/hooks/claude/" + token, false},
		{"http:///hooks/claude/" + token, false},
	}
	for _, tc := range cases {
		err := checkHookURL(tc.url)
		if (err == nil) != tc.ok {
			t.Errorf("checkHookURL(%q) = %v", tc.url, err)
		}
		if err != nil && strings.Contains(err.Error(), token) {
			t.Errorf("error leaks the hook token: %v", err)
		}
	}
}

func TestChildEnv(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"CLAUDECODE=1",
		"claudecode=1",
		"CLAUDE_CODE_ENTRYPOINT=sdk-ts",
		"Claude_Code_Session_Id=abc",
		"CLAUDE_CODE_CHILD_SESSION=1",
		"CLAUDE_PID=42",
		"CLAUDE_AGENT_SDK_VERSION=0.1",
		"CLAUDE_EFFORT=high",
		"CLAUDE_CONFIG_DIR=/cfg",
		"CLAUDECODEX=1",
		"ANTHROPIC_API_KEY=k",
		"=C:=C:\\work",
		"AMASE_HOME=/state",
	}
	got := childEnv(environ, []string{"EXTRA=1"})
	want := []string{"PATH=/usr/bin", "CLAUDE_CONFIG_DIR=/cfg", "CLAUDECODEX=1", "ANTHROPIC_API_KEY=k",
		"=C:=C:\\work", "AMASE_HOME=/state", "EXTRA=1"}
	if !slices.Equal(got, want) {
		t.Fatalf("childEnv:\n got %q\nwant %q", got, want)
	}
	if envName("=C:=C:\\work") != "=C:" || envName("NOVALUE") != "NOVALUE" || envName("") != "" {
		t.Fatal("envName")
	}
}

func TestResolveLauncher(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shim := range []string{"claude.cmd", "CLAUDE.CMD", "claude.bat"} {
		if got, err := resolveLauncher(filepath.Join(dir, shim)); err != nil || got != exe {
			t.Errorf("resolveLauncher(%s) = %q, %v", shim, got, err)
		}
	}
	native := filepath.Join(dir, "claude.exe")
	if got, err := resolveLauncher(native); err != nil || got != native {
		t.Errorf("resolveLauncher(native) = %q, %v", got, err)
	}
	if _, err := resolveLauncher(filepath.Join(t.TempDir(), "claude.cmd")); err == nil {
		t.Error("a .cmd shim without claude.exe beside it was accepted")
	}
}

func TestCheckSpec(t *testing.T) {
	wd := t.TempDir()
	file := filepath.Join(wd, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		spec runtime.SessionSpec
		ok   bool
	}{
		{"valid", runtime.SessionSpec{Harness: runtime.Claude, Workdir: wd, Model: "haiku", MaxUSD: 0.5}, true},
		{"harness defaults to claude", runtime.SessionSpec{Workdir: wd}, true},
		{"other harness", runtime.SessionSpec{Harness: runtime.Codex, Workdir: wd}, false},
		{"resume is unverified", runtime.SessionSpec{Workdir: wd, ResumeID: "x"}, false},
		{"relative workdir", runtime.SessionSpec{Workdir: "repo"}, false},
		{"missing workdir", runtime.SessionSpec{Workdir: filepath.Join(wd, "nope")}, false},
		{"workdir is a file", runtime.SessionSpec{Workdir: file}, false},
		{"negative budget", runtime.SessionSpec{Workdir: wd, MaxUSD: -1}, false},
		{"NaN budget", runtime.SessionSpec{Workdir: wd, MaxUSD: math.NaN()}, false},
		{"infinite budget", runtime.SessionSpec{Workdir: wd, MaxUSD: math.Inf(1)}, false},
		{"model that reads as a flag", runtime.SessionSpec{Workdir: wd, Model: "--dangerously-skip-permissions"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkSpec(tc.spec); (err == nil) != tc.ok {
				t.Fatalf("checkSpec = %v", err)
			}
		})
	}
	if err := checkSpec(runtime.SessionSpec{Workdir: wd, ResumeID: "x"}); !errors.Is(err, ErrResumeUnverified) {
		t.Fatalf("resume: %v", err)
	}
}

func TestNewUUID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := newUUID(), newUUID()
	if !re.MatchString(a) || a == b {
		t.Fatalf("newUUID = %q, %q", a, b)
	}
}

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{max: 8}
	for _, s := range []string{"abc", "defgh", "ijklmnopq"} {
		if n, err := tb.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if got := tb.String(); got != "jklmnopq" {
		t.Fatalf("tail = %q", got)
	}
}

func TestSysProcAttrBuildsEverywhere(t *testing.T) {
	if sysProcAttr() == nil {
		t.Fatal("nil SysProcAttr")
	}
}

// ---- the process, with this test binary as claude ---------------------------

// fakeReport is what the fake CLI records about how it was started.
type fakeReport struct {
	Args     []string `json:"args"`
	Cwd      string   `json:"cwd"`
	EnvNames []string `json:"env_names"` // names only: values may be secrets
	Settings string   `json:"settings"`
}

func fakeClaude(mode string) int {
	args := os.Args[1:]
	if path := os.Getenv("AMASE_FAKE_REPORT"); path != "" {
		r := fakeReport{Args: args}
		r.Cwd, _ = os.Getwd()
		for _, kv := range os.Environ() {
			r.EnvNames = append(r.EnvNames, envName(kv))
		}
		if i := slices.Index(args, "--settings"); i >= 0 && i+1 < len(args) {
			b, _ := os.ReadFile(args[i+1])
			r.Settings = string(b)
		}
		b, _ := json.Marshal(r)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
	}
	session := ""
	if i := slices.Index(args, "--session-id"); i >= 0 && i+1 < len(args) {
		session = args[i+1]
	}

	switch mode {
	case "fail":
		fmt.Fprintln(os.Stderr, "error: unknown option '--max-turns'")
		return 1
	case "mute":
		// Reads nothing, answers nothing, ignores stdin closing.
		time.Sleep(time.Hour)
		return 0
	case "replay":
		return fakeReplay(session)
	}

	// "serve" answers the protocol minimally; "stubborn" does the same but
	// keeps running after its stdin closes.
	w := bufio.NewWriter(os.Stdout)
	send := func(line string) { w.WriteString(line + "\n"); w.Flush() }
	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if mode == "stubborn" {
				time.Sleep(time.Hour)
			}
			return 0
		}
		var f struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}
		_ = json.Unmarshal(line, &f)
		switch f.Type {
		case "control_request":
			send(`{"type":"control_response","response":{"subtype":"success","request_id":"` + f.RequestID + `","response":{"commands":[]}}}`)
		case "user":
			send(`{"type":"system","subtype":"init","session_id":"` + session + `"}`)
			send(`{"type":"assistant","message":{"id":"msg_1","model":"fake","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"session_id":"` + session + `"}`)
			send(`{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.001,"usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
		}
	}
}

// fakeReplay plays the golden trace over real stdio. The initialize response
// is moved up to follow its request, because StartSession waits for it
// before the test can send the user turn.
func fakeReplay(session string) int {
	steps, err := fake.LoadTrace(os.Getenv("AMASE_FAKE_TRACE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	steps = append([]fake.Step{steps[0], steps[2], steps[1]}, steps[3:]...)
	peer := fake.NewPeer(os.Stdin, os.Stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = peer.Replay(ctx, steps, fake.Options{
		Exact:   true,
		Replace: strings.NewReplacer(`"init_1"`, `"amase_1"`, goldenProvider, session),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	for {
		if _, err := peer.Recv(ctx); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				fmt.Fprintln(os.Stderr, "stdin never closed")
				return 3
			}
			return 0
		}
		fmt.Fprintln(os.Stderr, "amase wrote past the end of the trace")
		return 3
	}
}

type fakeCase struct {
	mode    string
	adapter *Adapter
	spec    runtime.SessionSpec
	report  string
}

func newFakeCase(t *testing.T, mode string) *fakeCase {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeCase{mode: mode, report: filepath.Join(t.TempDir(), "report.json")}
	c.adapter = &Adapter{
		Executable:   self,
		SettingsDir:  t.TempDir(),
		Env:          []string{"AMASE_FAKE_CLAUDE=" + mode, "AMASE_FAKE_REPORT=" + c.report},
		StartTimeout: 10 * time.Second,
	}
	c.spec = runtime.SessionSpec{TaskID: "t1", Harness: runtime.Claude, Workdir: t.TempDir()}
	return c
}

func (c *fakeCase) readReport(t *testing.T) fakeReport {
	t.Helper()
	b, err := os.ReadFile(c.report)
	if err != nil {
		t.Fatal(err)
	}
	var r fakeReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func collect(t *testing.T, s *Session, until runtime.EventKind) []runtime.Event {
	t.Helper()
	var evs []runtime.Event
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatalf("events closed before %s: %+v (stderr %q)", until, evs, s.Stderr())
			}
			evs = append(evs, ev)
			if ev.Kind == until {
				return evs
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s: %+v", until, evs)
		}
	}
}

func assertEventsClosed(t *testing.T, s *Session) {
	t.Helper()
	select {
	case _, ok := <-s.Events():
		if ok {
			for range s.Events() {
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Events not closed after Close")
	}
}

func TestSessionLifecycle(t *testing.T) {
	for _, kv := range []string{"CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli", "CLAUDE_CODE_SESSION_ID=parent",
		"CLAUDE_PID=1", "CLAUDE_AGENT_SDK_VERSION=0.1", "CLAUDE_EFFORT=high", "CLAUDE_CONFIG_DIR=/cfg"} {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	c := newFakeCase(t, "serve")
	c.spec.HookURL = "http://127.0.0.1:7337/hooks/claude/tok"
	c.spec.Model = "haiku"
	c.spec.MaxUSD = 0.25

	s, err := c.adapter.StartSession(testCtx(t), c.spec, allowAll())
	if err != nil {
		t.Fatal(err)
	}
	r := c.readReport(t)

	settings := r.Args[slices.Index(r.Args, "--settings")+1]
	want := launch{providerID: s.ProviderID(), settingsPath: settings, model: "haiku", maxUSD: 0.25}.args()
	if !slices.Equal(r.Args, want) {
		t.Fatalf("argv:\n got %q\nwant %q", r.Args, want)
	}
	if filepath.Dir(settings) != c.adapter.SettingsDir {
		t.Fatalf("settings file %s is not in SettingsDir", settings)
	}
	wantSettings, _ := defaultSettings(c.spec.HookURL)
	if r.Settings != string(wantSettings) {
		t.Fatalf("settings file:\n%s", r.Settings)
	}
	gotWD, _ := filepath.EvalSymlinks(r.Cwd)
	wantWD, _ := filepath.EvalSymlinks(c.spec.Workdir)
	if gotWD != wantWD {
		t.Fatalf("child cwd = %s, want %s", gotWD, wantWD)
	}
	for _, name := range r.EnvNames {
		if nestingVar(name) {
			t.Errorf("child environment still has %s", name)
		}
	}
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "AMASE_FAKE_CLAUDE"} {
		if !slices.Contains(r.EnvNames, name) {
			t.Errorf("child environment lacks %s", name)
		}
	}
	if !strings.HasPrefix(s.ID(), "ses_") || s.PID() <= 0 {
		t.Fatalf("ID = %q, PID = %d", s.ID(), s.PID())
	}

	turn, err := s.SendTurn(testCtx(t), "hello")
	if err != nil || turn != 1 {
		t.Fatalf("SendTurn = %d, %v", turn, err)
	}
	evs := collect(t, s, runtime.TurnCompleted)
	if got := kinds(evs); got != "TurnStarted Text Usage TurnCompleted" {
		t.Fatalf("events = %s", got)
	}
	if last := evs[len(evs)-1]; last.Status != runtime.StatusOK || evs[2].Usage.Output != 2 {
		t.Fatalf("events = %+v", evs)
	}

	start := time.Now()
	if err := s.Close(testCtx(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("graceful Close took %v", d)
	}
	if _, err := os.Stat(settings); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settings file left behind: %v", err)
	}
	assertEventsClosed(t, s)
	if err := s.Close(testCtx(t)); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := s.SendTurn(testCtx(t), "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("SendTurn after Close = %v", err)
	}
}

func TestCloseKillsAfterGrace(t *testing.T) {
	c := newFakeCase(t, "stubborn")
	c.adapter.CloseTimeout = 300 * time.Millisecond
	s, err := c.adapter.StartSession(testCtx(t), c.spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := s.Close(testCtx(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d := time.Since(start); d < 300*time.Millisecond || d > 5*time.Second {
		t.Fatalf("Close took %v, want the 300 ms grace and then a kill", d)
	}
	assertEventsClosed(t, s)
}

func TestCloseWithEndedContextKillsAtOnce(t *testing.T) {
	c := newFakeCase(t, "stubborn")
	c.adapter.CloseTimeout = time.Minute
	s, err := c.adapter.StartSession(testCtx(t), c.spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := s.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Close took %v", d)
	}
	assertEventsClosed(t, s)
}

func TestStartFailureReportsStderrAndCleansUp(t *testing.T) {
	c := newFakeCase(t, "fail")
	c.spec.HookURL = "http://127.0.0.1:7337/hooks/claude/tok"
	_, err := c.adapter.StartSession(testCtx(t), c.spec, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown option '--max-turns'") {
		t.Fatalf("StartSession = %v", err)
	}
	if left, _ := os.ReadDir(c.adapter.SettingsDir); len(left) != 0 {
		t.Fatalf("settings file left behind: %v", left)
	}
}

func TestStartTimesOutWithoutHandshake(t *testing.T) {
	c := newFakeCase(t, "mute")
	c.adapter.StartTimeout = 200 * time.Millisecond
	c.adapter.CloseTimeout = time.Minute // the kill must not wait for this
	start := time.Now()
	_, err := c.adapter.StartSession(testCtx(t), c.spec, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StartSession = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("StartSession took %v", d)
	}
}

func TestStartRejectsBadInputBeforeSpawning(t *testing.T) {
	c := newFakeCase(t, "serve")
	cases := map[string]func(*runtime.SessionSpec){
		"remote hook": func(s *runtime.SessionSpec) { s.HookURL = "http://example.com/hooks/claude/tok" },
		"resume":      func(s *runtime.SessionSpec) { s.ResumeID = "abc" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := c.spec
			mutate(&spec)
			if _, err := c.adapter.Start(testCtx(t), spec, nil); err == nil {
				t.Fatal("Start succeeded")
			}
			if _, err := os.Stat(c.report); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("the CLI was spawned anyway")
			}
		})
	}
}

// TestSessionReplaysGoldenOverRealPipes runs the golden conformance through
// an actual child process, its stdio pipes and Close.
func TestSessionReplaysGoldenOverRealPipes(t *testing.T) {
	c := newFakeCase(t, "replay")
	trace, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "golden", "claude", "duplex-allow-then-hook-deny.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	c.adapter.Env = append(c.adapter.Env, "AMASE_FAKE_TRACE="+trace)
	gate := allowAll()
	s, err := c.adapter.StartSession(testCtx(t), c.spec, gate)
	if err != nil {
		t.Fatal(err)
	}
	var prompt struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(goldenSteps(t)[1].Msg, &prompt)
	if _, err := s.SendTurn(testCtx(t), prompt.Message.Content); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, s, runtime.TurnCompleted)
	if got := kinds(evs); got != goldenKinds {
		t.Fatalf("events:\n got %s\nwant %s\nstderr: %s", got, goldenKinds, s.Stderr())
	}
	if s.ProviderID() == goldenProvider || len(gate.requests()) != 1 {
		t.Fatalf("provider %s, %d approvals", s.ProviderID(), len(gate.requests()))
	}
	// The fake exits 0 only if every amase line matched and stdin then closed.
	if err := s.Close(testCtx(t)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertEventsClosed(t, s)
}
