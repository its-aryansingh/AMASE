// Package store is amase's durable memory: an append-only, hash-chained event
// journal, a separate receipt ledger, an in-memory projection rebuilt by
// replay, and a subscription feed for the UI.
//
// Producers never block. Emit and Receipt are non-blocking sends onto bounded
// queues drained by one writer goroutine, so the decision hot path never waits
// on a disk. The price is stated, not hidden: if the daemon dies, events still
// in a queue are lost, and the next start records that a receipt gap is possible.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/its-aryansingh/amase/internal/event"
	"github.com/its-aryansingh/amase/internal/paths"
	"github.com/its-aryansingh/toolgate/ledger"
)

// Options configures a Store. Zero values pick production defaults.
type Options struct {
	Layout  paths.Layout
	Version string
	// NoFsync skips the per-record fsync. Tests set it; a process kill loses
	// nothing either way (the OS keeps written data), only power loss does.
	NoFsync bool

	CriticalQueue int // default 8192
	LowQueue      int // default 4096
	ReceiptQueue  int // default 8192
	MemoryEvents  int // committed events kept in memory for subscribers; default 10000

	pauseWriter chan struct{} // tests: the writer waits on this before its first event
}

// Committed is an event the journal has accepted.
type Committed struct {
	Seq  uint64         `json:"seq"`
	Hash string         `json:"hash"`
	TS   time.Time      `json:"ts"`
	Env  event.Envelope `json:"env"`
}

// Recovery describes what startup found and repaired.
type Recovery struct {
	PrevShutdown     string
	TornTailBytes    int64
	InterruptedTurns []TurnRef
	ExpiredApprovals []string
}

// Store is safe for concurrent use.
type Store struct {
	opts     Options
	events   *ledger.Journal
	receipts *ledger.Journal

	crit, low chan event.Envelope
	rcpt      chan any

	mu      sync.RWMutex
	state   *State
	log     []Committed // most recent committed events, oldest first
	notify  chan struct{}
	lastSeq uint64

	droppedLow   atomic.Int64
	overflowCrit atomic.Int64
	droppedRcpt  atomic.Int64
	writeErrors  atomic.Int64
	receiptsOK   atomic.Int64

	closed   atomic.Bool
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// Open repairs, verifies and replays the journals in opts.Layout, records the
// start of this daemon run, and starts the writer.
func Open(opts Options) (*Store, Recovery, error) {
	if opts.CriticalQueue <= 0 {
		opts.CriticalQueue = 8192
	}
	if opts.LowQueue <= 0 {
		opts.LowQueue = 4096
	}
	if opts.ReceiptQueue <= 0 {
		opts.ReceiptQueue = 8192
	}
	if opts.MemoryEvents <= 0 {
		opts.MemoryEvents = 10000
	}
	if err := opts.Layout.Ensure(); err != nil {
		return nil, Recovery{}, err
	}

	var rec Recovery
	for _, p := range []string{opts.Layout.Events(), opts.Layout.Receipts()} {
		n, err := repairTail(p)
		if err != nil {
			return nil, rec, err
		}
		if p == opts.Layout.Events() {
			rec.TornTailBytes = n
		}
		if _, err := verifyFile(p); err != nil {
			return nil, rec, err
		}
	}

	s := &Store{
		opts:   opts,
		crit:   make(chan event.Envelope, opts.CriticalQueue),
		low:    make(chan event.Envelope, opts.LowQueue),
		rcpt:   make(chan any, opts.ReceiptQueue),
		state:  newState(),
		notify: make(chan struct{}),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}

	var lastType event.Type
	empty := true
	err := readRecords(opts.Layout.Events(), 0, func(r ledger.Record) bool {
		c, ok := toCommitted(r)
		if ok {
			s.remember(c)
			lastType = c.Env.Type
			empty = false
		}
		return true
	})
	if err != nil {
		return nil, rec, err
	}

	switch {
	case empty:
		rec.PrevShutdown = event.ShutdownFirstRun
	case lastType == event.DaemonStopped:
		rec.PrevShutdown = event.ShutdownClean
	default:
		rec.PrevShutdown = event.ShutdownUnclean
	}

	if s.events, err = ledger.Open(opts.Layout.Events(), !opts.NoFsync); err != nil {
		return nil, rec, err
	}
	if s.receipts, err = ledger.Open(opts.Layout.Receipts(), !opts.NoFsync); err != nil {
		s.events.Close()
		return nil, rec, err
	}

	// Anything still running or pending belongs to a daemon that no longer
	// exists: its agent processes died with their stdio pipes.
	reason := "daemon_restart"
	if rec.PrevShutdown == event.ShutdownUnclean {
		reason = "daemon_crash"
	}
	running := s.state.runningTurns()
	pending := s.state.pendingApprovals()

	start := event.MustNew(event.DaemonStarted, event.DaemonStartedData{
		Version: opts.Version, PID: os.Getpid(), PrevShutdown: rec.PrevShutdown,
		TornTailBytes: rec.TornTailBytes, InterruptedTurns: len(running), ExpiredApprovals: len(pending),
		PossibleReceiptGap: rec.PrevShutdown == event.ShutdownUnclean,
	})
	if err := s.commit(start); err != nil {
		s.closeJournals()
		return nil, rec, err
	}
	for _, t := range running {
		e := event.MustNew(event.TurnInterrupted, event.TurnInterruptedData{Reason: reason}).With(t.Repo, t.Task, t.Session, t.Turn)
		if err := s.commit(e); err != nil {
			s.closeJournals()
			return nil, rec, err
		}
		rec.InterruptedTurns = append(rec.InterruptedTurns, t)
	}
	for _, a := range pending {
		e := event.MustNew(event.ApprovalExpired, event.ApprovalExpiredData{ApprovalID: a.ID, CallID: a.CallID, Reason: reason}).
			With("", a.Task, a.Session, a.Turn)
		if err := s.commit(e); err != nil {
			s.closeJournals()
			return nil, rec, err
		}
		rec.ExpiredApprovals = append(rec.ExpiredApprovals, a.ID)
	}

	go s.writer()
	return s, rec, nil
}

func toCommitted(r ledger.Record) (Committed, bool) {
	var env event.Envelope
	if err := json.Unmarshal(r.Body, &env); err != nil || env.Type == "" {
		return Committed{}, false
	}
	ts, _ := time.Parse(time.RFC3339Nano, r.TS)
	return Committed{Seq: r.Seq, Hash: r.Hash, TS: ts, Env: env}, true
}

// remember applies c to the projection and the in-memory tail, and wakes subscribers.
// Callers must not hold s.mu.
func (s *Store) remember(c Committed) {
	s.mu.Lock()
	s.state.apply(c)
	s.log = append(s.log, c)
	// Trim in chunks: memory holds between N and 2N events, and the copy happens
	// once per N commits instead of on every one.
	if len(s.log) > 2*s.opts.MemoryEvents {
		s.log = append([]Committed(nil), s.log[len(s.log)-s.opts.MemoryEvents:]...)
	}
	s.lastSeq = c.Seq
	close(s.notify)
	s.notify = make(chan struct{})
	s.mu.Unlock()
}

// commit appends e to the event journal and folds it into memory. Only the
// writer goroutine (and Open, before the writer starts) calls it.
func (s *Store) commit(e event.Envelope) error {
	r, err := s.events.Append(ledger.Kind(e.Type), e.Session, "amase", e)
	if err != nil {
		s.writeErrors.Add(1)
		return fmt.Errorf("appending %s: %w", e.Type, err)
	}
	ts, _ := time.Parse(time.RFC3339Nano, r.TS)
	s.remember(Committed{Seq: r.Seq, Hash: r.Hash, TS: ts, Env: e})
	return nil
}

// Emit queues e for the journal without blocking. Critical event types go on
// the critical queue. It returns false if the event was dropped.
func (s *Store) Emit(e event.Envelope) bool {
	if e.Type.Priority() == event.PriorityCritical {
		return s.EmitCritical(e)
	}
	if s.closed.Load() {
		return false
	}
	select {
	case s.low <- e:
		return true
	default:
		s.droppedLow.Add(1)
		return false
	}
}

// EmitCritical queues e on the critical queue without blocking. A full critical
// queue means the disk has stalled for thousands of events; the overflow is
// counted and reported rather than allowed to block a decision.
func (s *Store) EmitCritical(e event.Envelope) bool {
	if s.closed.Load() {
		return false
	}
	select {
	case s.crit <- e:
		return true
	default:
		s.overflowCrit.Add(1)
		return false
	}
}

// Receipt queues one policy receipt for the ledger without blocking.
func (s *Store) Receipt(v any) bool {
	if s.closed.Load() {
		return false
	}
	select {
	case s.rcpt <- v:
		return true
	default:
		s.droppedRcpt.Add(1)
		return false
	}
}

func (s *Store) writer() {
	defer close(s.done)
	if s.opts.pauseWriter != nil {
		select {
		case <-s.opts.pauseWriter:
		case <-s.stop:
		}
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var reportedLow, reportedCrit, reportedRcpt int64

	report := func() {
		for _, q := range []struct {
			name string
			n    *atomic.Int64
			last *int64
		}{{"low", &s.droppedLow, &reportedLow}, {"critical", &s.overflowCrit, &reportedCrit}, {"receipts", &s.droppedRcpt, &reportedRcpt}} {
			if n := q.n.Load(); n != *q.last {
				_ = s.commit(event.MustNew(event.QueueDropped, event.QueueDroppedData{Queue: q.name, Count: n - *q.last}))
				*q.last = n
			}
		}
	}

	for {
		select {
		case e := <-s.crit:
			_ = s.commit(e)
			continue
		default:
		}
		select {
		case r := <-s.rcpt:
			s.writeReceipt(r)
			continue
		default:
		}
		select {
		case e := <-s.crit:
			_ = s.commit(e)
		case r := <-s.rcpt:
			s.writeReceipt(r)
		case e := <-s.low:
			_ = s.commit(e)
		case <-tick.C:
			report()
		case <-s.stop:
			s.drain()
			report()
			_ = s.commit(event.MustNew(event.DaemonStopped, event.DaemonStoppedData{Reason: "shutdown"}))
			s.closeJournals()
			return
		}
	}
}

func (s *Store) writeReceipt(v any) {
	if _, err := s.receipts.Append(ledger.Kind("receipt"), "", "amase", v); err != nil {
		s.writeErrors.Add(1)
		return
	}
	s.receiptsOK.Add(1)
}

func (s *Store) drain() {
	for {
		select {
		case e := <-s.crit:
			_ = s.commit(e)
		case r := <-s.rcpt:
			s.writeReceipt(r)
		case e := <-s.low:
			_ = s.commit(e)
		default:
			return
		}
	}
}

func (s *Store) closeJournals() {
	if s.events != nil {
		_ = s.events.Close()
	}
	if s.receipts != nil {
		_ = s.receipts.Close()
	}
}

// Close drains the queues, records DaemonStopped and closes the journals.
func (s *Store) Close(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.closed.Store(true)
		close(s.stop)
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("store close: %w", ctx.Err())
	}
}

// State returns a deep copy of the current projection.
func (s *Store) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.clone()
}

// LastSeq returns the sequence number of the newest committed event.
func (s *Store) LastSeq() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSeq
}

// Stats reports the counters an operator needs to trust the log.
type Stats struct {
	DroppedLow, OverflowCritical, DroppedReceipts, WriteErrors, Receipts int64
}

func (s *Store) Stats() Stats {
	return Stats{
		DroppedLow: s.droppedLow.Load(), OverflowCritical: s.overflowCrit.Load(),
		DroppedReceipts: s.droppedRcpt.Load(), WriteErrors: s.writeErrors.Load(), Receipts: s.receiptsOK.Load(),
	}
}

// Subscribe streams every committed event with a sequence number greater than
// fromSeq: first history (from memory, or from disk if it has aged out), then
// live events. The channel closes when cancel is called or the store closes.
// Each subscriber has its own goroutine, so a slow reader slows only itself.
func (s *Store) Subscribe(fromSeq uint64) (<-chan Committed, func()) {
	ch := make(chan Committed, 256)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(ch)
		cursor := fromSeq
		for {
			batch, wait, err := s.after(cursor, 512)
			if err != nil {
				return
			}
			for _, c := range batch {
				select {
				case ch <- c:
					cursor = c.Seq
				case <-ctx.Done():
					return
				}
			}
			if len(batch) > 0 {
				continue
			}
			select {
			case <-wait:
			case <-ctx.Done():
				return
			case <-s.done:
				// Deliver anything committed during shutdown, then stop.
				if rest, _, _ := s.after(cursor, 1<<20); len(rest) > 0 {
					for _, c := range rest {
						select {
						case ch <- c:
						case <-ctx.Done():
							return
						}
					}
				}
				return
			}
		}
	}()
	return ch, cancel
}

// after returns up to max committed events with Seq > cursor, or, when there
// are none yet, a channel that closes on the next commit.
func (s *Store) after(cursor uint64, max int) ([]Committed, <-chan struct{}, error) {
	s.mu.RLock()
	if cursor >= s.lastSeq {
		w := s.notify
		s.mu.RUnlock()
		return nil, w, nil
	}
	if len(s.log) > 0 && s.log[0].Seq <= cursor+1 {
		i := int(cursor + 1 - s.log[0].Seq)
		end := i + max
		if end > len(s.log) {
			end = len(s.log)
		}
		out := append([]Committed(nil), s.log[i:end]...)
		s.mu.RUnlock()
		return out, nil, nil
	}
	s.mu.RUnlock()

	var out []Committed
	err := readRecords(s.opts.Layout.Events(), cursor, func(r ledger.Record) bool {
		if c, ok := toCommitted(r); ok {
			out = append(out, c)
		}
		return len(out) < max
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return out, nil, nil
}
