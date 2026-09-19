# Decisions

Each entry records what was chosen, what was measured, and what would overturn it.

---

## T1 · Go, and the protocol layer is hand-written

**Decision.** toolgate is Go with no third-party dependencies, including the
JSON-RPC and MCP layers, which are implemented in `internal/jsonrpc` and
`internal/mcp` rather than taken from the official SDK.

### What the official SDK offers

`github.com/modelcontextprotocol/go-sdk` is real, at v1.7.0, maintained in
collaboration with Google, and targets the same 2026-07-28 revision. It is a
better-tested implementation of the protocol than this one, and choosing not to
use it needs a reason rather than a preference.

### The reason

This is a security tool. Its argument is that a deterministic component with no
moving parts belongs on a path where the incumbent is a language model. A tool
making that argument while pulling a dependency tree into the thing that parses
untrusted input has undercut itself before it starts.

Concretely, after the whole protocol layer was written:

| | toolgate |
|---|---|
| `go list -m all` | one line — the module itself |
| third-party packages | 0 |
| binary, linux/amd64 | 2.81 MB |
| binary, darwin/arm64 | 2.68 MB |
| binary, windows/amd64 | 2.94 MB |
| cgo | none; cross-compiles with `GOOS=… go build` |
| protocol layer | 4 files, ~600 lines |

Six hundred lines is a weekend and it is auditable in an afternoon. The trade is
worth making at this size and stops being worth making at ten times it.

### What was actually gained, not just saved

Writing the transport surfaced two bugs that an SDK would have hidden and that
the tests now pin:

1. **JSON-RPC ids are three-valued.** A string id, a numeric id, an explicit
   `null`, and *no id at all* are four different things, and the last means
   notification. Flattening id to a string — the obvious shortcut — makes the
   server answer notifications, which desynchronises the stream and surfaces much
   later as an unrelated client error. `RequestID` keeps the raw bytes and a
   presence flag. `TestNotificationsAreNotAnswered` covers it.
2. **`bufio.Scanner` caps tokens at 64 KiB.** A tool call carrying a file is
   ordinary and exceeds that, and the failure arrives as "token too long", which
   reads like a protocol error. `readLine` uses `ReadSlice` and names the real
   limit. `TestLargeFrameIsRead` covers it.

### What would overturn this

- **The spec moves faster than one person can track.** 2026-07-28 was a rewrite,
  not a revision; another of that size would make the SDK's maintenance the
  cheaper option. Everything version-specific lives in `internal/mcp/protocol.go`
  so that the swap is contained.
- **An upstream PR requires it.** If goose or another host wants the SDK used,
  the SDK gets used. `mcp.Server` is small enough to reimplement against it in a
  day.

### Note on the earlier language decision

A sibling project chose Python for the same class of work and recorded Rust as
its port target. That is not in tension with this. That decision optimised a
measurement pipeline where the analysis is offline and the bottleneck is the
model, not the harness. This one optimises a component that must ship as a single
file onto a machine whose toolchain is unknown, run inside another process's
critical path, and be readable by maintainers of a Rust codebase. Different
objective, different answer, and both hold.

---

## T2 · Parse the command; never pattern-match it

**Decision.** Every rule operates on a parsed command graph. No rule may match
against raw command text. The one exception is TG016's fork-bomb literal, which
is a syntactic idiom rather than a command.

### The measurement

`TestEvasionCorpus` runs eleven spellings of one download-and-execute against a
representative pattern guard — `(?:curl|wget)[^|]*\|\s*(?:sh|bash|zsh)\b` — and
against toolgate.

```
pattern-matching guard missed 5 of 11; toolgate missed 0
```

The five misses: `| "sh"`, `| s''h`, `| \sh`, `| /bin/sh`, `| sudo bash`.

None of those is an attack. They are how a model reformats a command, and a guard
that a reformat defeats is a guard that fails on ordinary inputs, not just
adversarial ones.

### The three parser features that do the work

1. **Quote removal.** `s''h`, `"sh"` and `\sh` all reduce to `sh`.
2. **Wrapper unwrapping.** `EffectiveBase` steps through `sudo`, `env`, `nice`,
   `timeout`, `xargs` and friends to find the program that will actually run.
   Without it, `curl … | sudo bash` has a base of `sudo` and no interpreter rule
   fires.
3. **Recursion.** `$(…)`, backticks and `sh -c "…"` are parsed as scripts in
   their own right. `git commit -m "$(curl … | sh)"` is one simple command to a
   parser that stops at the top level.

### The principle that falls out

**Unknown is not safe.** When argv[0] comes from a substitution, no static
analysis can say what runs, so TG002 escalates. When the input is too long to
parse, the verdict is marked `partial`, read-only is forced false, and allow is
upgraded to approval. A guard that guesses in the permissive direction under
uncertainty is worse than none, because it manufactures confidence.

### What would overturn this

A shell parser good enough to be worth depending on — `mvdan.cc/sh` is the
obvious candidate and is genuinely excellent — would replace `shell_lex.go` at
the cost of T1. If the rule table grows past the point where a hand-written lexer
is the limiting factor, that trade becomes correct.

---

## T3 · The read-only classifier replaces a model call, and is one-directional

**Decision.** `IsReadOnly` returns true only when every command in the script is
provably read-only. Everything else is false, including everything unknown.

### What it is replacing

goose's `SmartApprove` mode, on meeting a tool call with no stored permission,
asks the configured provider to classify it — `permission_judge.rs` builds a
synthetic tool called `platform__tool_by_tool_permission` and sends the tool name
and arguments to the model.

| | LLM judge | toolgate |
|---|---|---|
| latency | provider round trip | **14.3 µs** |
| cost | tokens, per unseen call | none |
| same input, same answer | not guaranteed | guaranteed, `TestDeterminism` × 500 |
| input origin | tool arguments the agent composed after reading files | same text, but parsed rather than interpreted |
| explains itself | a verdict | rule id, severity, rationale, suggested fix |

The asymmetry that matters: a false *not*-read-only costs one approval prompt. A
false read-only costs whatever the command did — and in `SmartApprove` that call
runs with no prompt at all.

### Why a name list is not enough

`git`, `kubectl`, `docker`, `terraform`, `npm` and `go` are each read-only or not
depending on the verb; `find`, `sed` and `sort` depending on a flag. Both are
handled explicitly (`readOnlySubcommand`, `readOnlyFlags`) and both are covered by
`TestReadOnlyClassification`. The interesting case is `cat $(echo go.mod)`, which
has a knowable program and an unknowable argument — an unknowable argument can be
`-i`, or a path outside the workspace, so it is not read-only.

### What would overturn this

Measured precision and recall against the LLM judge on a labelled corpus of real
traces. That measurement is the core of the GSoC proposal and does not exist yet;
until it does, the claim here is about cost and determinism, not accuracy.

---

## T4 · NDJSON with a hash chain, not SQLite

**Decision.** The journal is newline-delimited JSON where each record commits to
the digest of its predecessor.

### Against SQLite

The design this borrows from uses an append-only SQLite event log. In Go that
means cgo or a large pure-Go reimplementation, which forfeits T1 for the sake of
storing the audit trail. A text file also survives the situation the journal
exists for: an incident, on a machine where nobody has the tool installed, at an
hour when `grep` is the only thing anyone can be trusted to use.

### For the chain

An append-only log without a chain is append-only by convention. With one,
`toolgate verify` recomputes every record:

```
$ toolgate verify                       # edited one record's verdict deny -> allow
chain broken at record 1: record contents do not match its hash

$ toolgate verify                       # removed one record entirely
chain broken at record 2: sequence jumped: expected 1, found 2
```

Both covered by `TestEditIsDetected` and `TestDeletionIsDetected`.

### Stated plainly

This is tamper-evidence, not tamper-proofing. Anyone who can write the file can
recompute the chain from the edit forward. What it defeats is the quiet
single-line change, which is the realistic case; defeating a determined local
attacker needs an external witness and is out of scope.

Two details that are easy to get wrong and are pinned by tests: the digest is
taken over a struct with no hash field, so it cannot accidentally include itself;
and `Open` recovers the sequence number and tip from the existing file, so a
restart continues the chain instead of forking it (`TestReopenContinuesChain`).

---

## T5 · State handles are signed, principal-bound and expiring

**Decision.** A snapshot handle is `tg1.<base64url(payload)>.<base64url(HMAC)>`,
keyed per process, bound to the calling client, valid for thirty minutes.

### Why this is not a row id

MCP 2026-07-28 removed sessions. There is no `initialize`, no `Mcp-Session-Id`,
and no server-side place to keep per-connection state. Anything remembered between
calls has to be handed to the model and passed back as a tool argument — which
means it travels through the model's context, where anything that can influence
the model can influence it.

So the handle is attacker-reachable input by construction. It needs integrity
protection, a principal binding, and an expiry, and it gets all three. Tests:
`TestHandleRejectsTampering`, `TestHandleBoundToPrincipal`, `TestHandleExpires`.

### The restart behaviour is deliberate

Keys are generated per process and never written to disk, so a handle does not
survive a restart (`TestHandleIsNotPortableAcrossServers`). That is the correct
failure: a handle names a filesystem state that a fresh process has never
observed, and "take a new snapshot" is a better answer than a diff against
something the server is guessing at.

### What would overturn this

A deployment where several toolgate processes sit behind one endpoint — the exact
case the stateless rewrite was designed for. Then the key has to be shared, which
means a real key-management story rather than `rand.Read` at startup. Nothing
about the token format changes; only where the key comes from.

---

## T6 · Five tools, and `exec_guarded` is off by default

**Decision.** The server exposes five tools. The one that can run commands is
disabled unless `--allow-exec` is passed.

### On the count

goose's own guidance is that an agent performs best with fewer than twenty-five
tools enabled across all extensions, and every published tool is prompt text paid
for on every turn. A guardrail that degrades the agent it protects has not helped.
Five leaves room for the extensions the person actually wanted.

### On the default

A server that judges commands and a server that runs them are different things to
trust, and bundling them by default means every user of the first accepts the
second. The interesting deployment is the one where toolgate has no execution
capability at all and the host enforces its verdicts — which is what the goose
`ToolInspector` shim in `goose/` does.

### A note on why a denial is not a protocol error

A refused command comes back as a `CallToolResult` with `isError: true`, not as a
JSON-RPC error. Many clients discard the payload of an error response, so a
protocol-level refusal means the model never learns *why* it was stopped and
retries the same command. The text, the rule id and the suggested fix all need to
reach the model for the denial to change its behaviour rather than merely delay it.
