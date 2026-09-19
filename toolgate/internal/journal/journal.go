// Package journal is the append-only record of everything toolgate was asked
// about and everything it decided.
//
// It is newline-delimited JSON with a hash chain, and both halves of that are
// deliberate.
//
// NDJSON rather than SQLite: the reference implementation this design borrows
// from (Velra) uses an append-only SQLite log, which is a reasonable choice and
// costs a bundled C amalgamation to make in Go. A security tool that ships a C
// dependency to store its own audit trail has made a poor trade. A text file is
// also greppable during an incident, at three in the morning, by someone who
// does not have the tool installed.
//
// A hash chain rather than a plain log: each record commits to the digest of the
// one before it, so removing or editing a past entry invalidates every entry
// after it. An append-only log without a chain is append-only by convention --
// anyone who can write the file can rewrite it. Tamper-evidence is not
// tamper-proofing and this does not pretend otherwise: an attacker who owns the
// file can recompute the whole chain. What it stops is the quiet single-line
// edit, which is the realistic case.
package journal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// GenesisHash is the chain's fixed starting value.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Kind classifies a record.
type Kind string

const (
	KindAudit    Kind = "audit"    // a command was inspected
	KindExec     Kind = "exec"     // a command was run
	KindSnapshot Kind = "snapshot" // a workspace state was recorded
	KindNote     Kind = "note"     // operator or lifecycle event
)

// Record is one journal line.
//
// Hash is computed over every other field, so it is excluded from its own input
// by construction rather than by remembering to zero it -- the digest is taken
// of the marshalled `payload` struct, which has no Hash field at all.
type Record struct {
	Seq     uint64          `json:"seq"`
	TS      string          `json:"ts"`
	Kind    Kind            `json:"kind"`
	Session string          `json:"session,omitempty"`
	Actor   string          `json:"actor,omitempty"`
	Body    json.RawMessage `json:"body"`
	Prev    string          `json:"prev"`
	Hash    string          `json:"hash"`
}

type payload struct {
	Seq     uint64          `json:"seq"`
	TS      string          `json:"ts"`
	Kind    Kind            `json:"kind"`
	Session string          `json:"session,omitempty"`
	Actor   string          `json:"actor,omitempty"`
	Body    json.RawMessage `json:"body"`
	Prev    string          `json:"prev"`
}

func (p payload) digest() (string, error) {
	// json.Marshal on a struct emits fields in declaration order, so this is
	// stable across runs and Go versions. Body arrives as RawMessage and is
	// hashed exactly as it will be written -- re-marshalling it here would risk
	// a different key order between the hash input and the stored bytes.
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Journal appends records to a file.
type Journal struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	seq  uint64
	prev string
	// sync controls whether each append is flushed to disk. Off by default:
	// fsync per tool call would add milliseconds to the hot path, and the
	// failure it protects against (power loss between the verdict and the
	// command) is not the threat this tool is for.
	sync bool
}

// Open opens or creates a journal, reading any existing chain to recover the
// sequence number and tip hash.
func Open(path string, fsync bool) (*Journal, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating journal directory: %w", err)
		}
	}

	seq, prev, err := scanTip(path)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening journal: %w", err)
	}
	return &Journal{f: f, w: bufio.NewWriter(f), seq: seq, prev: prev, sync: fsync}, nil
}

func scanTip(path string) (uint64, string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, GenesisHash, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("reading journal: %w", err)
	}
	defer f.Close()

	var seq uint64
	prev := GenesisHash
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			return 0, "", fmt.Errorf("journal line %d is not valid JSON: %w", seq+1, err)
		}
		seq, prev = r.Seq, r.Hash
	}
	if err := sc.Err(); err != nil {
		return 0, "", fmt.Errorf("scanning journal: %w", err)
	}
	return seq, prev, nil
}

// Append writes one record and returns it.
func (j *Journal) Append(kind Kind, session, actor string, body any) (Record, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Record{}, fmt.Errorf("encoding journal body: %w", err)
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	p := payload{
		Seq:     j.seq + 1,
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
		Kind:    kind,
		Session: session,
		Actor:   actor,
		Body:    raw,
		Prev:    j.prev,
	}
	hash, err := p.digest()
	if err != nil {
		return Record{}, err
	}

	rec := Record{
		Seq: p.Seq, TS: p.TS, Kind: p.Kind, Session: p.Session,
		Actor: p.Actor, Body: p.Body, Prev: p.Prev, Hash: hash,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return Record{}, err
	}
	if _, err := j.w.Write(append(line, '\n')); err != nil {
		return Record{}, fmt.Errorf("writing journal: %w", err)
	}
	if err := j.w.Flush(); err != nil {
		return Record{}, fmt.Errorf("flushing journal: %w", err)
	}
	if j.sync {
		if err := j.f.Sync(); err != nil {
			return Record{}, fmt.Errorf("syncing journal: %w", err)
		}
	}

	j.seq, j.prev = p.Seq, hash
	return rec, nil
}

// Close flushes and closes the file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.w.Flush(); err != nil {
		return err
	}
	return j.f.Close()
}

// Tip returns the current sequence number and chain head.
func (j *Journal) Tip() (uint64, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.seq, j.prev
}

// VerifyResult reports the outcome of checking a chain.
type VerifyResult struct {
	Records  int    `json:"records"`
	OK       bool   `json:"ok"`
	BreakAt  uint64 `json:"break_at,omitempty"`
	BreakWhy string `json:"break_why,omitempty"`
	Tip      string `json:"tip,omitempty"`
}

// Verify recomputes the whole chain.
func Verify(r io.Reader) (VerifyResult, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	res := VerifyResult{OK: true}
	prev := GenesisHash
	var expectSeq uint64 = 1

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return VerifyResult{}, fmt.Errorf("record %d: %w", expectSeq, err)
		}
		res.Records++

		fail := func(why string) (VerifyResult, error) {
			res.OK = false
			res.BreakAt = rec.Seq
			res.BreakWhy = why
			return res, nil
		}
		if rec.Seq != expectSeq {
			return fail(fmt.Sprintf("sequence jumped: expected %d, found %d", expectSeq, rec.Seq))
		}
		if rec.Prev != prev {
			return fail("prev hash does not match the previous record")
		}
		want, err := payload{
			Seq: rec.Seq, TS: rec.TS, Kind: rec.Kind, Session: rec.Session,
			Actor: rec.Actor, Body: rec.Body, Prev: rec.Prev,
		}.digest()
		if err != nil {
			return VerifyResult{}, err
		}
		if want != rec.Hash {
			return fail("record contents do not match its hash")
		}

		prev = rec.Hash
		expectSeq++
	}
	if err := sc.Err(); err != nil {
		return VerifyResult{}, err
	}
	res.Tip = prev
	return res, nil
}
