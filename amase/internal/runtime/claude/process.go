package claude

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/its-aryansingh/amase/internal/event"
	"github.com/its-aryansingh/amase/internal/runtime"
)

const (
	defaultStartTimeout = 30 * time.Second
	defaultCloseTimeout = 5 * time.Second
	// After the child is gone its stdout normally reaches EOF at once; a
	// process it started may still hold the pipe, so stop waiting after this.
	streamDrainTimeout = 2 * time.Second
	stderrTailBytes    = 4 << 10
)

// ErrResumeUnverified is returned for a SessionSpec with a ResumeID: the
// --resume flag exists in Claude Code 2.1.331 but its behaviour has not been
// verified (SPEC 4.10, C14), and adapter code must not rely on it.
var ErrResumeUnverified = errors.New("claude: resuming a session is not supported until --resume is verified (C14)")

// Adapter starts Claude Code sessions. The zero value is ready to use: it
// finds claude on PATH and writes hook settings to the OS temp directory.
type Adapter struct {
	// Executable is the claude binary. Empty: look up "claude" on PATH. An npm
	// .cmd shim is replaced by the claude.exe it wraps (C19), so no argument
	// ever passes through cmd.exe quoting.
	Executable string
	// SettingsDir receives each session's --settings file, which holds the
	// hook URL and its token. Empty: os.TempDir(). It must not be the worktree
	// (D-W13: snapshots and diffs would pick the file up).
	SettingsDir string
	// Settings renders the --settings file for a hook URL. Nil: the hook
	// layout recorded with Claude Code 2.1.331 (C7, C8).
	Settings func(hookURL string) ([]byte, error)
	// Env holds KEY=VALUE pairs added to the child's environment after the
	// nesting variables are removed, to pass one through deliberately.
	Env []string
	// Stderr, if set, receives the child's stderr. A short tail is always kept
	// for error messages.
	Stderr       io.Writer
	Logger       *slog.Logger  // debug diagnostics only; nil discards
	EventBuffer  int           // capacity of Events; 0 means 256
	StartTimeout time.Duration // bound on the initialize handshake; 0 means 30 s
	CloseTimeout time.Duration // grace between closing stdin and killing; 0 means 5 s
}

var _ runtime.Adapter = (*Adapter)(nil)

// Harness returns runtime.Claude.
func (a *Adapter) Harness() runtime.Harness { return runtime.Claude }

// Start implements runtime.Adapter. See StartSession.
func (a *Adapter) Start(ctx context.Context, spec runtime.SessionSpec, gate runtime.Gate) (runtime.Session, error) {
	s, err := a.StartSession(ctx, spec, gate)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// StartSession launches `claude -p` in spec.Workdir with the stream-json
// duplex protocol and returns once the initialize handshake (C5) succeeds.
// ctx bounds only the start-up: the session lives until Close, so a request
// context may be passed safely. Every can_use_tool request goes to gate.
//
// With spec.HookURL set, the session's PreToolUse hook posts to it and blocks
// the tool call if amase does not answer (D-W13, SPEC 4.11). Without it only
// can_use_tool is gated, and Claude Code runs the calls it auto-allows (some
// read-only Bash) unseen.
func (a *Adapter) StartSession(ctx context.Context, spec runtime.SessionSpec, gate runtime.Gate) (*Session, error) {
	if err := checkSpec(spec); err != nil {
		return nil, err
	}
	exe, err := a.executable()
	if err != nil {
		return nil, err
	}
	l := launch{providerID: newUUID(), model: spec.Model, maxUSD: spec.MaxUSD}
	if spec.HookURL != "" {
		if l.settingsPath, err = a.writeSettings(spec.HookURL); err != nil {
			return nil, err
		}
	}
	s, err := a.spawn(exe, spec.Workdir, l, gate)
	if err != nil {
		removeSettings(l.settingsPath)
		return nil, err
	}

	timeout := a.StartTimeout
	if timeout <= 0 {
		timeout = defaultStartTimeout
	}
	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := s.conn.Initialize(ictx); err != nil {
		kill, stop := context.WithCancel(context.Background())
		stop() // a cancelled context makes Close kill at once instead of waiting
		_ = s.Close(kill)
		return nil, fmt.Errorf("claude: initialize: %w%s", err, s.stderrSuffix())
	}
	return s, nil
}

func checkSpec(spec runtime.SessionSpec) error {
	if spec.Harness != "" && spec.Harness != runtime.Claude {
		return fmt.Errorf("claude: cannot start harness %q", spec.Harness)
	}
	if spec.ResumeID != "" {
		return ErrResumeUnverified
	}
	if !filepath.IsAbs(spec.Workdir) {
		return fmt.Errorf("claude: workdir %q is not an absolute path", spec.Workdir)
	}
	if fi, err := os.Stat(spec.Workdir); err != nil {
		return fmt.Errorf("claude: workdir: %w", err)
	} else if !fi.IsDir() {
		return fmt.Errorf("claude: workdir %q is not a directory", spec.Workdir)
	}
	if math.IsNaN(spec.MaxUSD) || math.IsInf(spec.MaxUSD, 0) || spec.MaxUSD < 0 {
		return fmt.Errorf("claude: invalid spending cap %v", spec.MaxUSD)
	}
	if strings.HasPrefix(spec.Model, "-") || strings.ContainsAny(spec.Model, " \t\r\n") {
		return fmt.Errorf("claude: invalid model name %q", spec.Model)
	}
	return nil
}

// launch is what varies between sessions on the command line.
type launch struct {
	providerID   string
	settingsPath string
	model        string
	maxUSD       float64
}

// args returns the argv recorded in testdata/golden/claude/argv.json, in its
// order; every flag there was verified against Claude Code 2.1.331. There is
// no --max-turns in that version (C17): the cost cap is --max-budget-usd.
func (l launch) args() []string {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--permission-prompt-tool", "stdio", // every permission prompt becomes can_use_tool (C2)
		"--permission-mode", "manual",
		"--setting-sources", "project,local", // keep the owner's ~/.claude settings out (C16)
	}
	if l.settingsPath != "" {
		args = append(args, "--settings", l.settingsPath)
	}
	args = append(args, "--session-id", l.providerID, "--include-hook-events")
	if l.maxUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(l.maxUSD, 'f', -1, 64))
	}
	if l.model != "" {
		args = append(args, "--model", l.model)
	}
	return args
}

func (a *Adapter) executable() (string, error) {
	if a.Executable != "" {
		return resolveLauncher(a.Executable)
	}
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("claude: %w", err)
	}
	return resolveLauncher(p)
}

// resolveLauncher swaps npm's Windows batch shim for the native claude.exe it
// starts (C19). Go refuses or mangles some arguments to .cmd files, and a
// prompt or path must never be re-parsed by cmd.exe.
func resolveLauncher(p string) (string, error) {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".cmd", ".bat":
	default:
		return p, nil
	}
	exe := filepath.Join(filepath.Dir(p), "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	if fi, err := os.Stat(exe); err == nil && fi.Mode().IsRegular() {
		return exe, nil
	}
	return "", fmt.Errorf("claude: %s is a batch launcher and %s does not exist; set Adapter.Executable to claude.exe", p, exe)
}

// childEnv returns environ without the variables that make Claude Code
// believe it runs nested inside another Claude Code session (C6), then extra.
// Names compare case-insensitively because Windows treats them that way.
func childEnv(environ, extra []string) []string {
	out := make([]string, 0, len(environ)+len(extra))
	for _, kv := range environ {
		if !nestingVar(envName(kv)) {
			out = append(out, kv)
		}
	}
	return append(out, extra...)
}

func envName(kv string) string {
	// Windows keeps per-drive working directories in entries like "=C:=C:\x",
	// so a leading '=' belongs to the name.
	start := 0
	if strings.HasPrefix(kv, "=") {
		start = 1
	}
	if i := strings.IndexByte(kv[start:], '='); i >= 0 {
		return kv[:start+i]
	}
	return kv
}

// nestingVar reports whether name is in the C6 set verified on 2.1.331.
func nestingVar(name string) bool {
	switch n := strings.ToUpper(name); n {
	case "CLAUDECODE", "CLAUDE_PID", "CLAUDE_AGENT_SDK_VERSION", "CLAUDE_EFFORT":
		return true
	default:
		return strings.HasPrefix(n, "CLAUDE_CODE_")
	}
}

// sysProcAttr asks the kernel to kill Claude Code when amase dies, so no tool
// call runs ungoverned after the daemon is gone (SPEC I12, 4.11). Pdeathsig
// exists only on Linux and FreeBSD while this file builds everywhere, so the
// field is set by name. On Windows the child exits when its stdin pipe closes
// with amase; a Job Object is WP-6.3's.
func sysProcAttr() *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{}
	f := reflect.ValueOf(attr).Elem().FieldByName("Pdeathsig")
	if sig := reflect.ValueOf(syscall.SIGKILL); f.IsValid() && f.CanSet() && f.Type() == sig.Type() {
		f.Set(sig)
	}
	return attr
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error since Go 1.24
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---- hook settings (C7, D-W13) ----------------------------------------------

type hookCommand struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Timeout   int    `json:"timeout"`
	OnFailure string `json:"onFailure,omitempty"`
}

type hookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

// defaultSettings renders the hook layout of testdata/golden/claude/
// hook-settings.json: PreToolUse blocks the call when amase fails to answer
// (fail-closed); the telemetry hooks fail open (SPEC 4.11).
func defaultSettings(hookURL string) ([]byte, error) {
	block := hookCommand{Type: "http", URL: hookURL, Timeout: 5, OnFailure: "block"}
	open := hookCommand{Type: "http", URL: hookURL, Timeout: 5}
	s := struct {
		Hooks map[string][]hookMatcher `json:"hooks"`
	}{map[string][]hookMatcher{
		"PreToolUse":   {{Matcher: "*", Hooks: []hookCommand{block}}},
		"PostToolUse":  {{Matcher: "*", Hooks: []hookCommand{open}}},
		"SessionStart": {{Hooks: []hookCommand{open}}},
		"Stop":         {{Hooks: []hookCommand{open}}},
	}}
	return json.MarshalIndent(s, "", "  ")
}

// checkHookURL accepts only plain HTTP to a loopback address, where the
// resident hook server listens (SPEC 4.13). Errors never echo the URL: its
// path carries the per-boot token.
func checkHookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return errors.New("claude: the hook URL must be an http:// URL")
	}
	if h := u.Hostname(); h != "localhost" {
		if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
			return errors.New("claude: the hook URL must point at a loopback address")
		}
	}
	return nil
}

func (a *Adapter) writeSettings(hookURL string) (string, error) {
	if err := checkHookURL(hookURL); err != nil {
		return "", err
	}
	render := a.Settings
	if render == nil {
		render = defaultSettings
	}
	data, err := render(hookURL)
	if err != nil {
		return "", fmt.Errorf("claude: hook settings: %w", err)
	}
	dir := a.SettingsDir
	if dir == "" {
		dir = os.TempDir()
	}
	// CreateTemp opens the file 0600: it holds the hook token.
	f, err := os.CreateTemp(dir, "amase-claude-settings-*.json")
	if err != nil {
		return "", fmt.Errorf("claude: hook settings: %w", err)
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		removeSettings(f.Name())
		return "", fmt.Errorf("claude: hook settings: %w", err)
	}
	return f.Name(), nil
}

func removeSettings(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("claude: remove hook settings: %w", err)
	}
	return nil
}

// ---- the child process ------------------------------------------------------

// Session is one running `claude -p` process: a Conn over its stdio plus the
// process lifecycle. It implements runtime.Session.
type Session struct {
	conn     *Conn
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *os.File
	stderr   *tailBuffer
	settings string
	grace    time.Duration
	cancel   context.CancelFunc

	exited  chan struct{} // closed once cmd.Wait returns
	waitErr error
	runDone chan struct{} // closed once conn.Run returns

	closeOnce sync.Once
	closeErr  error
}

var _ runtime.Session = (*Session)(nil)

func (a *Adapter) spawn(exe, workdir string, l launch, gate runtime.Gate) (*Session, error) {
	// The session outlives StartSession's context; Close ends it.
	sctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(sctx, exe, l.args()...)
	cmd.Dir = workdir
	cmd.Env = childEnv(os.Environ(), a.Env)
	cmd.SysProcAttr = sysProcAttr()
	// Bounds Wait when a grandchild keeps the stderr pipe open.
	cmd.WaitDelay = streamDrainTimeout

	stderr := &tailBuffer{max: stderrTailBytes}
	cmd.Stderr = stderr
	if a.Stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, a.Stderr)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claude: %w", err)
	}
	// stdout is an os.Pipe of our own rather than cmd.StdoutPipe: Wait closes
	// the latter as soon as the process exits, which can drop frames still
	// buffered in the pipe. Ours reads to EOF and is closed after Run returns.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		cancel()
		stdin.Close()
		return nil, fmt.Errorf("claude: %w", err)
	}
	cmd.Stdout = stdoutW
	if err := cmd.Start(); err != nil {
		cancel()
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("claude: start %s: %w", filepath.Base(exe), err)
	}
	stdoutW.Close() // the child holds the write end now; EOF arrives when it exits

	s := &Session{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdoutR,
		stderr:   stderr,
		settings: l.settingsPath,
		grace:    a.CloseTimeout,
		cancel:   cancel,
		exited:   make(chan struct{}),
		runDone:  make(chan struct{}),
	}
	if s.grace <= 0 {
		s.grace = defaultCloseTimeout
	}
	s.conn = NewConn(stdoutR, stdin, Config{
		SessionID:   event.NewID("ses"),
		ProviderID:  l.providerID,
		Gate:        gate,
		EventBuffer: a.EventBuffer,
		Logger:      a.Logger,
	})
	go func() {
		s.waitErr = cmd.Wait()
		close(s.exited)
	}()
	go func() {
		// Run's error is not kept: an abnormal end already reached Events as
		// SessionError, and Close reports how the process exited.
		_ = s.conn.Run(sctx)
		close(s.runDone)
	}()
	return s, nil
}

// ID returns the amase session id.
func (s *Session) ID() string { return s.conn.ID() }

// ProviderID returns Claude Code's session id (the --session-id amase chose,
// unless system/init reported another).
func (s *Session) ProviderID() string { return s.conn.ProviderID() }

// SendTurn sends prompt as the next user turn and returns its number.
func (s *Session) SendTurn(ctx context.Context, prompt string) (int, error) {
	return s.conn.SendTurn(ctx, prompt)
}

// Interrupt stops the running turn (C4); the session stays usable.
func (s *Session) Interrupt(ctx context.Context) error { return s.conn.Interrupt(ctx) }

// Events returns the normalised event stream. It is closed when the child's
// output ends: after Close, or when the process exits on its own.
func (s *Session) Events() <-chan runtime.Event { return s.conn.Events() }

// PID returns the child's process id.
func (s *Session) PID() int { return s.cmd.Process.Pid }

// MalformedLines counts stdout lines that were not JSON objects.
func (s *Session) MalformedLines() uint64 { return s.conn.MalformedLines() }

// Stderr returns the last few KiB the child wrote to stderr.
func (s *Session) Stderr() string { return s.stderr.String() }

// Close ends the session: it closes Claude Code's stdin so the CLI finishes
// and exits on its own, kills it if it is still running after CloseTimeout
// (5 s) or once ctx ends, waits for Events to close, and removes the settings
// file. It returns the child's exit error only when the child was not killed.
// Calling Close again returns the first result.
func (s *Session) Close(ctx context.Context) error {
	s.closeOnce.Do(func() { s.closeErr = s.shutdown(ctx) })
	return s.closeErr
}

func (s *Session) shutdown(ctx context.Context) error {
	_ = s.stdin.Close()
	killed := false
	grace := time.NewTimer(s.grace)
	defer grace.Stop()
	select {
	case <-s.exited: // checked first: an exit that already happened is not a kill
	default:
		select {
		case <-s.exited:
		case <-grace.C:
			killed = true
		case <-ctx.Done():
			killed = true
		}
	}
	if killed {
		_ = s.cmd.Process.Kill() // fails only if the process is already gone
		<-s.exited               // bounded: Wait returns at most WaitDelay after the exit
	}
	errs := []error{removeSettings(s.settings)}

	drain := time.NewTimer(streamDrainTimeout)
	defer drain.Stop()
	select {
	case <-s.runDone:
	case <-drain.C:
		// Run stops on cancellation once in-flight Gate calls return, even if
		// a leftover grandchild keeps its pipe read blocked.
		s.cancel()
		select {
		case <-s.runDone:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("claude: close: the event stream did not end: %w", ctx.Err()))
		}
	}
	s.cancel()
	_ = s.stdout.Close()

	if !killed && s.waitErr != nil {
		errs = append(errs, fmt.Errorf("claude: exited: %w%s", s.waitErr, s.stderrSuffix()))
	}
	return errors.Join(errs...)
}

// stderrSuffix returns the end of the child's stderr for an error message:
// that is where a CLI prints why it stopped.
func (s *Session) stderrSuffix() string {
	tail := strings.TrimSpace(s.stderr.String())
	if tail == "" {
		return ""
	}
	if cut := len(tail) - 1<<10; cut > 0 {
		for cut < len(tail) && !utf8.RuneStart(tail[cut]) {
			cut++
		}
		tail = "…" + tail[cut:]
	}
	return ": stderr: " + tail
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		n := copy(t.buf, t.buf[over:])
		t.buf = t.buf[:n]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
