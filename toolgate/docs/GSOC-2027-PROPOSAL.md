# A deterministic tool inspector for goose

Draft proposal · GSoC 2027 · Aryan Raj Singh

---

## Summary

goose decides whether a tool call needs human approval. When it has no stored
permission for a call and is in `SmartApprove` mode, it asks a language model
whether the call is read-only. This proposal adds a deterministic inspector in
front of that model, resolves the common cases in microseconds without tokens,
and measures what the change costs and buys.

The work is additive. Nothing is removed; the existing judge remains the fallback
for everything the analyser cannot decide.

## The problem, located in the source

`crates/goose/src/permission/permission_judge.rs` builds a synthetic tool named
`platform__tool_by_tool_permission` and sends the tool name and arguments to the
configured provider, asking it to classify the operation as read-only.
`crates/goose/src/permission/permission_inspector.rs` calls it from
`PermissionInspector::inspect` when no stored permission and no `read_only_hint`
annotation apply.

Four consequences:

1. **A provider round trip on the approval path.** Every first-time tool call
   waits for it.
2. **Tokens spent to classify the user's own shell command.** Billed to them, on
   every unseen call.
3. **A non-deterministic security decision.** The same command can be classified
   differently on two runs. Note that `InspectionResult` already carries a
   `confidence: f32` field — the data model anticipates this.
4. **An input the attacker can reach.** The classifier reads tool arguments,
   which the agent composed after reading repository text. A false "read-only"
   verdict means the call executes with no approval prompt.

Point 4 is the security case; points 1–3 are the case a maintainer will feel.

## The seam already exists

`crates/goose/src/tool_inspection.rs` defines:

```rust
pub trait ToolInspector: Send + Sync {
    fn name(&self) -> &'static str;
    async fn inspect(&self, session_id: &str, tool_requests: &[ToolRequest],
                     messages: &[Message], goose_mode: GooseMode)
        -> Result<Vec<InspectionResult>>;
    fn is_enabled(&self) -> bool { true }
    fn as_any(&self) -> &dyn std::any::Any;
}
```

`ToolInspectionManager` runs inspectors in registration order and — importantly —
**logs and continues when one returns `Err`**. A new inspector that fails leaves
goose behaving exactly as it does today. The extension point was built for this;
nothing needs to be invented.

## What I have already built

`toolgate` — a Go static binary, zero third-party dependencies, MCP
`2026-07-28` over stdio, Apache-2.0.

- Hand-written POSIX shell lexer and parser: quote removal, wrapper unwrapping
  (`sudo`, `env`, `timeout`, `xargs`), recursion into `$(…)`, backticks and
  `sh -c "…"` strings.
- Sixteen rule classes with ids, severities, rationales and suggested fixes.
- A deterministic read-only classifier covering verb-sensitive tools (`git`,
  `kubectl`, `docker`, `terraform`, `npm`, `go`) and flag-sensitive ones (`find`,
  `sed`, `sort`).
- Hash-chained append-only journal; `verify` detects both edits and deletions.
- Signed, principal-bound, expiring state handles for the stateless protocol.
- 4,811 lines of Go including 590 of tests; all passing.
- `goose/toolgate_inspector.rs` — a working `ToolInspector` against the real
  trait signature.

Measured: **14.3 µs** per command, 9.8 KB, 70 allocations. Against a
representative pattern-matching guard on eleven spellings of one
download-and-execute, the pattern guard misses five; the parser misses none.

The proposal is not "let me build this." It is "here it is; let me measure it and
land it upstream."

## Deliverables

**D1 — `ToolgateInspector` in goose, behind a config flag, default off.**
Runs before `PermissionInspector`. Returns `Allow` only when the command is
*proved* read-only; `Deny` for critical findings; `RequireApproval` with the rule
id and suggested fix otherwise; `Err` when the guard is unavailable, so the
existing path takes over.

**D2 — the measurement.** A labelled corpus of shell commands drawn from real
goose sessions, hand-labelled read-only or not, scored against both classifiers:

- precision and recall for each, with the disagreements listed individually
- tokens and wall-clock saved per session
- rate at which the LLM judge changes its answer across repeated identical calls
- an injection suite: commands whose text argues for its own safety, measuring
  whether each classifier is moved by the argument

Nobody has published this comparison. It is the part of the work that is
publishable independently of whether the patch lands.

**D3 — rule coverage for non-shell tools.** File writes and manifests, which the
current rule table does not reach.

**D4 — documentation.** A rules reference, a threat model, and a page in the
goose docs on what the inspector does and does not promise.

## Timeline

Twelve weeks, standard GSoC shape.

| Weeks | Work |
|---|---|
| 1–2 | Community bonding. Agree the config surface and the registration point with maintainers. Build the labelled corpus. |
| 3–4 | D1 behind a flag; unit tests in the goose tree; first draft PR. |
| 5–6 | D2 harness: run both classifiers over the corpus, publish the first numbers. |
| 7 | Midterm: numbers in hand, PR in review. |
| 8–9 | Injection suite; act on review feedback. |
| 10–11 | D3 and D4. |
| 12 | Final: merged or maintainer-blessed, with the measurement written up. |

## Before the application opens

The work that decides the outcome happens before the proposal is read.

- Land three or four unrelated, useful PRs in `aaif-goose/goose` so the name is
  familiar. goose's `CONTRIBUTING.md` requires an issue to reach *Ready* on the
  project board before implementation, and says not to open many PRs at once —
  so this is slow on purpose and has to start early.
- Open an issue describing the LLM-judge cost and non-determinism, with numbers.
  If maintainers disagree that it is a problem, that is worth learning in
  September rather than in March.
- Publish toolgate, use it daily, and write up the measurement independently.
- Report the prompt-injection finding through goose's security process before
  writing about it publicly.

## Honest risks

**AAIF may not be a GSoC organisation in 2027.** The Linux Foundation's 2026 GSoC
umbrella covered OpenPrinting, Automotive Grade Linux, Sound Open Firmware,
Zephyr, SPDX and device-tree work — not goose, MCP or AAIF. This proposal
therefore has no guaranteed home, and the plan must survive that: the same work
is a standalone open-source project and an upstream PR whether or not it is ever
funded. If AAIF joins the LF umbrella or applies separately, the proposal is
ready; if not, the artefact still exists. **Treat GSoC as one possible outcome of
the work, never as its purpose.**

**Maintainers may prefer this stay out of tree.** A reasonable position: the
inspector could ship as a separate crate, or the deterministic check could be
folded into `permission_judge` as a pre-filter. Either is a fine result. The
measurement in D2 is the durable contribution.

**The corpus is the hard part.** Labelling commands read-only or not is
subjective at the margins. Labels will be published with the disagreements
visible rather than resolved silently.
