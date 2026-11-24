package store

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/its-aryansingh/amase/internal/event"
	"github.com/its-aryansingh/amase/internal/paths"
)

func testOptions(dir string) Options {
	return Options{Layout: paths.Layout{Root: dir}, Version: "test", NoFsync: true}
}

func openT(t *testing.T, opts Options) (*Store, Recovery) {
	t.Helper()
	s, rec, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

func closeT(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitSeq(t *testing.T, s *Store, seq uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.LastSeq() < seq {
		if time.Now().After(deadline) {
			t.Fatalf("store stuck at seq %d, waiting for %d", s.LastSeq(), seq)
		}
		time.Sleep(time.Millisecond)
	}
}

// emitOpenTask records a task with a running turn and a pending approval: the
// state a crash leaves behind.
func emitOpenTask(s *Store) int {
	evs := []event.Envelope{
		event.MustNew(event.RepoRegistered, event.RepoRegisteredData{RepoID: "r1", Path: "/work/demo"}),
		event.MustNew(event.TaskCreated, event.TaskCreatedData{TaskID: "t1", RepoID: "r1", Harness: "claude", BaseRef: "HEAD", Prompt: "fix it"}).With("r1", "t1", "", 0),
		event.MustNew(event.SessionStarted, event.SessionStartedData{ProviderID: "p1"}).With("r1", "t1", "s1", 0),
		event.MustNew(event.TurnStarted, event.TurnStartedData{Prompt: "fix it"}).With("r1", "t1", "s1", 1),
		event.MustNew(event.ApprovalRequested, event.ApprovalRequestedData{ApprovalID: "a1", CallID: "c1", Tool: "Bash", Command: "rm -rf docs", Deadline: time.Now().Add(time.Hour)}).With("r1", "t1", "s1", 1),
	}
	for _, e := range evs {
		s.Emit(e)
	}
	return len(evs)
}

func countType(t *testing.T, s *Store, typ event.Type) int {
	t.Helper()
	ch, cancel := s.Subscribe(0)
	defer cancel()
	n, last := 0, s.LastSeq()
	for c := range ch {
		if c.Env.Type == typ {
			n++
		}
		if c.Seq >= last {
			break
		}
	}
	return n
}

func TestFirstRunThenCleanRestart(t *testing.T) {
	dir := t.TempDir()
	s, rec := openT(t, testOptions(dir))
	if rec.PrevShutdown != event.ShutdownFirstRun {
		t.Fatalf("prev shutdown %q, want first_run", rec.PrevShutdown)
	}
	n := emitOpenTask(s)
	waitSeq(t, s, uint64(1+n))
	s.Emit(event.MustNew(event.TurnCompleted, event.TurnCompletedData{Status: event.StatusOK}).With("r1", "t1", "s1", 1))
	s.Emit(event.MustNew(event.ApprovalResolved, event.ApprovalResolvedData{ApprovalID: "a1", Decision: "allow", By: "human"}))
	waitSeq(t, s, uint64(3+n))
	closeT(t, s)

	s2, rec2 := openT(t, testOptions(dir))
	defer closeT(t, s2)
	if rec2.PrevShutdown != event.ShutdownClean || len(rec2.InterruptedTurns) != 0 || len(rec2.ExpiredApprovals) != 0 {
		t.Fatalf("clean restart reported %+v", rec2)
	}
	st := s2.State()
	if st.Tasks["t1"] == nil || st.Tasks["t1"].Turns[0].Status != event.StatusOK || st.Approvals["a1"].State != event.StateApproved {
		t.Fatalf("projection after replay is wrong: task=%+v approval=%+v", st.Tasks["t1"], st.Approvals["a1"])
	}
	if !st.Daemon.Running || st.Daemon.PrevShutdown != event.ShutdownClean {
		t.Fatalf("daemon info %+v", st.Daemon)
	}
	if got := countType(t, s2, event.DaemonStarted); got != 2 {
		t.Fatalf("%d DaemonStarted records, want 2", got)
	}
	if _, err := verifyFile(paths.Layout{Root: dir}.Events()); err != nil {
		t.Fatal(err)
	}
}

func TestReplayIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	s, _ := openT(t, testOptions(dir))
	n := emitOpenTask(s)
	for i := 0; i < 5; i++ {
		s.Emit(event.MustNew(event.UsageReported, event.UsageReportedData{MessageID: fmt.Sprint(i), Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}).With("r1", "t1", "s1", 1))
	}
	waitSeq(t, s, uint64(1+n+5))
	closeT(t, s)

	first, _ := openT(t, testOptions(dir))
	a := first.State()
	closeT(t, first)
	second, _ := openT(t, testOptions(dir))
	b := second.State()
	closeT(t, second)

	if a.Usage != b.Usage || a.Usage.CacheRead != 15 || a.Repos["r1"] != b.Repos["r1"] || len(a.Tasks) != len(b.Tasks) {
		t.Fatalf("replays disagree: %+v vs %+v", a.Usage, b.Usage)
	}
}

// TestCrashChild is the helper process for TestRecoveryAfterCrash. It writes an
// open task, announces READY, and waits to be killed.
func TestCrashChild(t *testing.T) {
	dir := os.Getenv("AMASE_STORE_CRASH_DIR")
	if dir == "" {
		t.Skip("helper process for TestRecoveryAfterCrash")
	}
	s, _, err := Open(testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	n := emitOpenTask(s)
	for i := 0; i < 20; i++ {
		s.Emit(event.MustNew(event.AssistantText, event.AssistantTextData{Text: fmt.Sprintf("chunk %d", i)}).With("r1", "t1", "s1", 1))
	}
	waitSeq(t, s, uint64(1+n+20))
	fmt.Println("READY")
	time.Sleep(time.Minute)
}

func TestRecoveryAfterCrash(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.v")
	cmd.Env = append(os.Environ(), "AMASE_STORE_CRASH_DIR="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "READY") {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			_ = cmd.Wait()
			t.Fatal("child exited before READY")
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child never became ready")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	s, rec := openT(t, testOptions(dir))
	defer closeT(t, s)
	if rec.PrevShutdown != event.ShutdownUnclean {
		t.Fatalf("prev shutdown %q, want unclean", rec.PrevShutdown)
	}
	if len(rec.InterruptedTurns) != 1 || rec.InterruptedTurns[0].Task != "t1" || len(rec.ExpiredApprovals) != 1 || rec.ExpiredApprovals[0] != "a1" {
		t.Fatalf("recovery %+v", rec)
	}
	st := s.State()
	if st.Tasks["t1"].Turns[0].Status != event.StatusInterrupted || st.Approvals["a1"].State != event.StateExpired {
		t.Fatalf("crash not reflected: turn=%s approval=%s", st.Tasks["t1"].Turns[0].Status, st.Approvals["a1"].State)
	}
	if got := countType(t, s, event.AssistantText); got != 20 {
		t.Fatalf("%d of 20 committed text chunks survived the kill", got)
	}
	if _, err := verifyFile(paths.Layout{Root: dir}.Events()); err != nil {
		t.Fatal(err)
	}
}

func TestTornTailIsRepaired(t *testing.T) {
	dir := t.TempDir()
	s, _ := openT(t, testOptions(dir))
	waitSeq(t, s, uint64(emitOpenTask(s)+1))
	closeT(t, s)

	garbage := []byte(`{"seq":99,"ts":"2026-11-25T00:00:00Z","kind":"Turn`)
	f, err := os.OpenFile(paths.Layout{Root: dir}.Events(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(garbage); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s2, rec := openT(t, testOptions(dir))
	defer closeT(t, s2)
	if rec.TornTailBytes != int64(len(garbage)) {
		t.Fatalf("repaired %d bytes, want %d", rec.TornTailBytes, len(garbage))
	}
	if _, err := verifyFile(paths.Layout{Root: dir}.Events()); err != nil {
		t.Fatal(err)
	}
}

func TestMidFileCorruptionRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	s, _ := openT(t, testOptions(dir))
	waitSeq(t, s, uint64(emitOpenTask(s)+1))
	closeT(t, s)

	path := paths.Layout{Root: dir}.Events()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(raw, []byte("rm -rf docs"), []byte("ls -la docs"), 1)
	if bytes.Equal(raw, edited) {
		t.Fatal("test did not find the record to tamper with")
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(testOptions(dir)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered journal opened; err = %v", err)
	}
}

func TestProducersNeverBlock(t *testing.T) {
	opts := testOptions(t.TempDir())
	opts.CriticalQueue, opts.LowQueue, opts.ReceiptQueue = 2, 2, 2
	gate := make(chan struct{})
	opts.pauseWriter = gate
	s, _ := openT(t, opts)

	start := time.Now()
	for i := 0; i < 1000; i++ {
		s.Emit(event.MustNew(event.TurnStarted, event.TurnStartedData{}))
		s.Emit(event.MustNew(event.AssistantText, event.AssistantTextData{Text: "x"}))
		s.Receipt(map[string]int{"i": i})
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("3000 emits against a stalled writer took %v; producers must not block", took)
	}
	st := s.Stats()
	if st.OverflowCritical == 0 || st.DroppedLow == 0 || st.DroppedReceipts == 0 {
		t.Fatalf("drops not counted: %+v", st)
	}
	close(gate)
	closeT(t, s)
}

func TestSubscribeServesHistoryThenLive(t *testing.T) {
	opts := testOptions(t.TempDir())
	opts.MemoryEvents = 3 // force the disk path for older history
	s, _ := openT(t, opts)
	defer closeT(t, s)
	for i := 0; i < 10; i++ {
		s.Emit(event.MustNew(event.TurnStarted, event.TurnStartedData{Prompt: fmt.Sprint(i)}))
	}
	waitSeq(t, s, 11)

	ch, cancel := s.Subscribe(0)
	defer cancel()
	var seqs []uint64
	for len(seqs) < 11 {
		select {
		case c := <-ch:
			seqs = append(seqs, c.Seq)
		case <-time.After(5 * time.Second):
			t.Fatalf("history stalled after %v", seqs)
		}
	}
	for i, q := range seqs {
		if q != uint64(i+1) {
			t.Fatalf("history out of order: %v", seqs)
		}
	}

	s.Emit(event.MustNew(event.TurnStarted, event.TurnStartedData{Prompt: "live"}))
	select {
	case c := <-ch:
		if c.Seq != 12 {
			t.Fatalf("live event seq %d, want 12", c.Seq)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live event never arrived")
	}

	tail, cancelTail := s.Subscribe(9)
	defer cancelTail()
	if c := <-tail; c.Seq != 10 {
		t.Fatalf("resume from 9 started at %d", c.Seq)
	}
}

func TestReceiptsChainSeparately(t *testing.T) {
	dir := t.TempDir()
	s, _ := openT(t, testOptions(dir))
	for i := 0; i < 3; i++ {
		if !s.Receipt(map[string]any{"fingerprint": fmt.Sprint(i), "verdict": "allow"}) {
			t.Fatal("receipt dropped")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for s.Stats().Receipts < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("receipts not written: %+v", s.Stats())
		}
		time.Sleep(time.Millisecond)
	}
	closeT(t, s)
	res, err := verifyFile(paths.Layout{Root: dir}.Receipts())
	if err != nil || res.Records != 3 {
		t.Fatalf("receipt ledger: %+v %v", res, err)
	}
}

func TestStateIsACopy(t *testing.T) {
	s, _ := openT(t, testOptions(t.TempDir()))
	defer closeT(t, s)
	waitSeq(t, s, uint64(emitOpenTask(s)+1))
	st := s.State()
	st.Tasks["t1"].Prompt = "mutated"
	st.Tasks["t1"].Turns[0].Status = "mutated"
	if s.State().Tasks["t1"].Prompt != "fix it" || s.State().Tasks["t1"].Turns[0].Status != event.StatusRunning {
		t.Fatal("mutating a returned State changed the store")
	}
}

func TestEmitAfterCloseIsRefused(t *testing.T) {
	s, _ := openT(t, testOptions(t.TempDir()))
	closeT(t, s)
	if s.Emit(event.MustNew(event.TurnStarted, event.TurnStartedData{})) || s.Receipt(1) {
		t.Fatal("emit after close was accepted")
	}
	closeT(t, s) // idempotent
}
