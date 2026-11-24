// Package ledger is the exported face of toolgate's hash-chained NDJSON
// journal, so the amase workspace can keep its event log and receipt ledger in
// the same tamper-evident format that `toolgate verify` already checks.
package ledger

import (
	"io"

	"github.com/its-aryansingh/toolgate/internal/journal"
)

type (
	Journal      = journal.Journal
	Record       = journal.Record
	Kind         = journal.Kind
	VerifyResult = journal.VerifyResult
)

// GenesisHash is the chain's fixed starting value.
const GenesisHash = journal.GenesisHash

// Open opens or creates a journal at path. With fsync set, every append is
// synced to disk before it returns.
func Open(path string, fsync bool) (*Journal, error) { return journal.Open(path, fsync) }

// Verify recomputes the whole chain read from r.
func Verify(r io.Reader) (VerifyResult, error) { return journal.Verify(r) }
