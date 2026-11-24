package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/its-aryansingh/toolgate/ledger"
)

// ErrCorrupt means a journal failed verification somewhere other than a torn
// final line. That is either tampering or real corruption, and amase refuses to
// start on it rather than guess which.
var ErrCorrupt = errors.New("journal failed verification")

// repairTail truncates a torn final line — the bytes a crash left after the last
// newline — and reports how many bytes it removed. Every complete record ends
// in '\n' (the journal writes record and newline in one call), so anything after
// the last newline is a partial write, never a record.
func repairTail(path string) (int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("opening %s for tail repair: %w", path, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	if size == 0 {
		return 0, nil
	}

	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return 0, nil
	}

	keep, err := lastNewline(f, size)
	if err != nil {
		return 0, err
	}
	if err := f.Truncate(keep); err != nil {
		return 0, fmt.Errorf("truncating torn tail of %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	return size - keep, nil
}

// lastNewline returns the offset just past the last '\n' in the file, or 0.
func lastNewline(f *os.File, size int64) (int64, error) {
	const chunk = 64 << 10
	buf := make([]byte, chunk)
	end := size
	for end > 0 {
		start := end - chunk
		if start < 0 {
			start = 0
		}
		n, err := f.ReadAt(buf[:end-start], start)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if i := bytes.LastIndexByte(buf[:n], '\n'); i >= 0 {
			return start + int64(i) + 1, nil
		}
		end = start
	}
	return 0, nil
}

// verifyFile checks a journal's hash chain. A missing file is an empty, valid chain.
func verifyFile(path string) (ledger.VerifyResult, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ledger.VerifyResult{OK: true, Tip: ledger.GenesisHash}, nil
	}
	if err != nil {
		return ledger.VerifyResult{}, err
	}
	defer f.Close()
	res, err := ledger.Verify(f)
	if err != nil {
		return res, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	if !res.OK {
		return res, fmt.Errorf("%w: %s: record %d: %s", ErrCorrupt, path, res.BreakAt, res.BreakWhy)
	}
	return res, nil
}

// readRecords calls fn for every complete record in the journal at path whose
// sequence number is greater than after, stopping early when fn returns false.
// A final line without a newline is a write in progress and is skipped.
func readRecords(path string, after uint64, fn func(ledger.Record) bool) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec ledger.Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return fmt.Errorf("%w: %s: unreadable record: %v", ErrCorrupt, path, err)
		}
		if rec.Seq <= after {
			continue
		}
		if !fn(rec) {
			return nil
		}
	}
}
