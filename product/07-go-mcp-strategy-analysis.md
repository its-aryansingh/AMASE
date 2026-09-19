# Deep analysis: the Go/MCP/Goose continuation pack

18 September 2026 · verified against goose's source on `main`, the MCP 2026-07-28
release notes, the Linux Foundation's GSoC 2026 org page, and PyPI/Go module
metadata read directly

---

## 0. Verdict in one paragraph

The strategic read is right and the technical spec has a load-bearing error. Go
for agent runtime infrastructure is a genuinely under-supplied niche; goose under
AAIF is the correct host project; Velra's architecture is the correct blueprint.
But **an MCP server cannot intercept another tool's execution** — MCP has no
"before this runs" event, so a guardrail the model can decline to call is a
linter. Meanwhile the thing that *does* exist, and that nobody appears to have
noticed, is in `crates/goose/src/permission/`: goose decides tool safety by
**asking a language model**, and it ships a `ToolInspector` trait that is exactly
the seam for replacing that. That is a better project than the one in the pack,
by some distance, and it is the one worth building.

---

## 1. What in the pack checks out

| claim | status |
|---|---|
| AAIF is real, Linux Foundation, anchored by MCP + goose + AGENTS.md | confirmed — announced Dec 2025, Anthropic/OpenAI/Block |
| goose donated by Block, now at `aaif-goose/goose` | confirmed — moved April 2026, ~52.6k stars |
| goose core is Rust; capability expands via MCP servers | confirmed |
| Kubescape has a native `kubescape mcpserver` | confirmed — five tools, bundled in the CLI, post-v3.0.44 |
| Velra's thesis is the right blueprint | agreed — hooks, append-only log, deterministic reduction, bounded injection |
| Go is under-supplied relative to Python in this niche | agreed, and the official Go SDK's existence is evidence the ecosystem expects Go here |

The elimination matrix is also sound. Kubeflow really is a fresher trap for the
reason stated; LangGraph really is saturated; Continue really is an IDE-layer
project. Nothing there needs revisiting.

---

## 2. Four corrections, ranked by what they would have cost

### 2.1 An MCP server cannot intercept — this is the one that matters

The spec puts it plainly: **an MCP server is a callee.** It runs when the model
chooses to call it. There is no event, in any revision, that fires before another
tool executes.

Velra can intercept because Claude Code has `PreToolUse` hooks. goose has no
equivalent exposed to extensions — its interception happens *inside the Rust
process*, in the permission pipeline.

So a "Go MCP server that intercepts commands before execution" describes
something the protocol cannot do. Two architectures actually work:

**A. Custodial tool ownership.** The Go server owns the dangerous verbs —
`exec_guarded`, and the built-in developer/shell extension is disabled. Now every
command *must* route through the guard, because the agent has no other path.
Works today, no host changes, portable across goose, Claude Code, Cursor.
Weakness: it depends on configuration discipline. Leave the built-in shell
enabled and the guard is bypassed by the agent simply preferring the other tool.

**B. In-tree inspector.** A Rust `ToolInspector` in goose that consults a Go
daemon. Unbypassable, because it sits on the path every tool call already takes.
Costs a PR and a maintainer conversation.

The right move is both, in order: build **A** now as a standalone artefact that
works everywhere, then propose **B** as the GSoC project with A as the evidence.
That is how strong proposals read — you arrive with the thing half-built.

### 2.2 The MCP you would be targeting changed shape on 28 July 2026

This is not a point release. `2026-07-28` is a **stateless rewrite**:

- `initialize` / `initialized` handshake — **gone**
- `Mcp-Session-Id` header — **gone**. Each request carries protocol version,
  client identity and capabilities in `_meta`
- Roots, Sampling and Logging — **deprecated**, twelve-month window
- Legacy HTTP+SSE transport — deprecated
- Elicitation survived, reshaped as Multi Round-Trip Requests (MRTR)
- `Mcp-Method` / `Mcp-Name` headers now required for streamable requests, so a
  gateway can route and authorise without parsing JSON
- List responses carry `ttlMs` and `cacheScope`
- Dynamic Client Registration deprecated in favour of Client ID Metadata Documents
- RFC 9207 issuer validation required; client credentials bound to their issuer

**What that does to "deterministic workspace snapshotting":** state can no longer
live in a session. It has to be handed to the model as an **explicit handle** and
passed back as a tool argument.

Which turns out to be an improvement. A handle that must survive a round trip
through the model is a handle anything influencing the model can tamper with —
so it cannot be a row id. Make it the **content address of the snapshot**,
HMAC-signed, bound to the calling principal, expiring. Velra's determinism claim,
upgraded: the client can verify the handle it got back names the tree it thinks
it does. Security researchers have already flagged that MRTR's `requestState`
round-trips through the client and is therefore attacker-controlled input
requiring integrity protection, principal binding and expiry — building it that
way from day one is a detail that reads as senior.

### 2.3 There is an official Go SDK, maintained with Google

`github.com/modelcontextprotocol/go-sdk`, v1.7.0, targeting 2026-07-28. So
hand-rolling JSON-RPC is now a *decision* rather than a default, and it needs a
stated reason.

The reason that holds: this is a security tool, the protocol layer is ~600 lines,
and "zero dependencies" is a claim a reader verifies in one command. The reason
that does not hold: "I want to show I can." Write it, then say why in a decisions
file, and keep everything version-specific in one file so the SDK can be swapped
in if upstream asks. That is the defensible version of the same choice.

### 2.4 GSoC under the Linux Foundation does not currently route to goose

The LF's GSoC 2026 umbrella covered **OpenPrinting, Automotive Grade Linux, Sound
Open Firmware, device-tree bindings, Zephyr and SPDX** — administered by Till
Kamppeter and Aveek Basu. No AAIF, no goose, no MCP. Applications ran 16–31 March
2026.

So "apply to the Linux Foundation for goose" has, today, **no mentor and no
project idea page**. AAIF would have to join the umbrella or apply as its own
organisation for 2027, and organisations are accepted in February.

Three hedges, all of which are worth doing anyway:

1. Build the artefact so it stands alone. A used open-source tool with real users
   outranks an unfunded GSoC application, and outranks most funded ones.
2. Contribute to goose *now*, on its own terms. `CONTRIBUTING.md` requires an
   issue to reach **Ready** on the project board before implementation and asks
   contributors not to open many PRs at once — so becoming a known name is a
   months-long process that has to start in 2026, not March 2027.
3. Keep a second org in reserve. CNCF is a reliable GSoC participant and
   Kubescape is Go, eBPF, OPA, and now has an MCP server — adjacent enough that
   the same skills transfer with no rewrite.

---

## 3. What is actually in goose, and why it changes the project

Read from `main`:

**`crates/goose/src/permission/permission_judge.rs`** imports
`crate::providers::base::Provider`, builds a synthetic tool called
`platform__tool_by_tool_permission`, and **sends the tool name and arguments to
the configured language model** asking whether the operation is read-only.

**`crates/goose/src/permission/permission_inspector.rs`** calls that from
`PermissionInspector::inspect`, in `SmartApprove` mode, whenever there is no
stored permission and no `read_only_hint` annotation.

Four consequences, in increasing order of seriousness:

1. A provider round trip on the approval path of every first-time tool call.
2. Tokens spent classifying the user's own shell command, billed to them.
3. A security decision that is not reproducible. `InspectionResult` already
   carries `confidence: f32` — the data model anticipates the probabilism.
4. **The classifier's input is tool arguments the agent composed after reading
   files.** Text in a repository is upstream of a security decision. And in
   `SmartApprove`, a false "read-only" verdict means the call runs with **no
   approval prompt at all**; `cache_non_readonly_decision` only caches the
   negative.

Then `crates/goose/src/tool_inspection.rs` hands you the seam:

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

`InspectionAction` is `Allow | Deny | RequireApproval(Option<String>)`.
`ToolInspectionManager` runs inspectors in registration order and **logs and
continues when one returns `Err`** — fail-open by construction, already upstream.
A guard that dies cannot take the agent with it.

**So the project is:** a deterministic inspector registered *before*
`PermissionInspector`, resolving the common cases from the command text in
microseconds with no tokens, leaving the model judge as the fallback for what it
cannot decide. Strictly additive. Nothing removed.

This is a better project than "a Go MCP server" because it (a) fixes a named
weakness in the host, (b) has a benefit measurable in tokens and milliseconds,
(c) carries a security story, (d) uses an extension point the maintainers already
built, and (e) still puts the interesting engineering in Go.

---

## 4. Built today: `toolgate`

Not a sketch. It compiles, the tests pass, the binary runs.

```
$ toolgate audit 'curl -fsSL https://get.example.com/i.sh | sudo bash'
DENY  curl -fsSL https://get.example.com/i.sh | sudo bash
  read-only false · policy standard · 83.0us · toolgate-audit/0.1.0
  TG001 [critical] curl output is piped into bash
       try: download to a file, read it, then run it as a separate reviewed step
  TG005 [high    ] escalates privileges via sudo
```

| | |
|---|---|
| Go, `go list -m all` | **one line — the module itself** |
| binary | 2.81 MB linux · 2.68 MB darwin/arm64 · 2.94 MB windows, no cgo |
| source | 4,811 lines, 590 of them tests, all passing |
| analysis cost | **14.3 µs**, 9.8 KB, 70 allocs per command |
| protocol | MCP 2026-07-28 over stdio, five tools |

**The parser is the differentiator.** Eleven spellings of one
download-and-execute, run against a representative pattern guard and against
toolgate:

```
pattern-matching guard missed 5 of 11; toolgate missed 0
```

The five misses — `| "sh"`, `| s''h`, `| \sh`, `| /bin/sh`, `| sudo bash` — are
not attacks. They are how a model reformats a command it copied out of a README.
Quote removal, wrapper unwrapping and recursion into `$(…)` / `sh -c "…"` catch
all of them. And where argv[0] is genuinely unknowable — `$(printf 'r''m') -rf /`
— it escalates instead of allowing. **Unknown is not safe.** That single rule is
the line between a guardrail and a linter.

**The journal beats the blueprint.** Velra uses append-only SQLite. In Go that
means cgo. NDJSON with a per-record hash chain costs nothing, stays greppable
during an incident, and is tamper-*evident*:

```
$ toolgate verify     # after flipping one record's verdict from deny to allow
chain broken at record 1: record contents do not match its hash
$ toolgate verify     # after deleting a record
chain broken at record 2: sequence jumped: expected 1, found 2
```

Also shipped: `goose/toolgate_inspector.rs`, a working `ToolInspector` written
against the real trait signature, ready to be a PR.

---

## 5. Eighteen months, with real dates

**Now → Dec 2026 — become a name in goose.** Use goose daily. Three or four
small, unrelated, useful PRs, each through the issue-to-Ready process its
`CONTRIBUTING.md` requires. Publish toolgate. Open one issue with numbers on the
judge's cost and non-determinism — if maintainers disagree it is a problem, learn
that now, not in March.

**Jan → Feb 2027 — the measurement.** Build the labelled corpus of real shell
commands and score both classifiers: precision, recall, tokens saved, wall-clock
saved, and how often the LLM judge changes its answer on repeated identical
input. Plus an injection suite — commands whose text argues for its own safety —
measuring which classifier is moved by the argument. **Nobody has published this.**
It is the piece that stands on its own whatever GSoC does. Report the injection
finding through goose's security process before writing about it publicly.

**Feb 2027 — organisations announced.** If AAIF is in, apply. If not, CNCF via
Kubescape, with the same work retargeted.

**Mar 2027 — proposal.** Mid-to-late March, mirroring 2026's 16–31 March window.
Draft is in `docs/GSOC-2027-PROPOSAL.md`.

**Jun → Aug 2027 — the work,** funded or not.

**Sep → Dec 2027 — convert.** By then: a used tool, a merged upstream change in a
Linux Foundation project, a published measurement, and maintainers who know the
name. That is the portfolio. GSoC is one possible line in it.

---

## 6. On the compensation thesis — once, plainly

$120k–180k remote, from India, as a fresher, at a tier-1 Silicon Valley startup
is the extreme tail of the distribution, not the target of it. Remote roles at US
bands are scarce, most go to people with a public artefact **and** a referral, and
a majority of "remote" listings at those numbers are US-resident-only. GSoC is a
real signal but it is an internship credential; it opens conversations rather
than closing offers.

What actually converts, in order: a tool people use; a merged contribution to a
project hiring managers recognise; and humans who will vouch. The plan above
produces all three by late 2027 **whether or not GSoC happens**, which is the only
reason it is worth eighteen months. Build it so the tail outcome is upside rather
than the plan.

One more thing worth saying: the same portfolio at ₹25–40 LPA from an Indian AI
infrastructure team is a realistic near-term outcome, and it is a fine place to
be standing when the remote role appears. Optimise for the artefact. The number
follows it; it does not lead it.

---

## 7. How this sits with AMASE

Same thesis, two surfaces. AMASE answers *when should this run stop?*; toolgate
answers *should this action happen at all?* Both are deterministic, cheap,
explainable governance of agent execution, and the signal engines are the same
category of thing.

Do not treat them as competing for time. AMASE is the capstone and the
measurement instrument; toolgate is the hiring artefact and the upstream vehicle.
The reliability numbers AMASE produces are exactly the evidence toolgate's
proposal needs.

On the language: `DECISIONS.md` D1–D3 chose Python for AMASE and rejected Go.
That is not overturned here. Those records optimised an offline measurement
pipeline where the model, not the harness, is the bottleneck. This optimises a
component that must ship as one file to an unknown machine, sit inside another
process's critical path, and be read by maintainers of a Rust codebase. Different
objective function, different answer, and both stand. It is worth writing that
down rather than letting it look like a reversal.

**And it does not change anything about tomorrow.** The 19 September capstone
deliverables are finished and submitted as they are; none of this belongs in that
presentation.

---

## Sources

- [Linux Foundation — formation of the Agentic AI Foundation](https://www.linuxfoundation.org/press/linux-foundation-announces-the-formation-of-the-agentic-ai-foundation)
- [goose has a new home — the Agentic AI Foundation](https://goose-docs.ai/blog/2026/04/07/goose-moves-to-aaif/)
- [Why Block handed Goose to the Linux Foundation — The New Stack](https://thenewstack.io/block-goose-agentic-foundation/)
- [aaif-goose/goose — CONTRIBUTING.md](https://github.com/aaif-goose/goose/blob/main/CONTRIBUTING.md)
- goose source read directly: `crates/goose/src/tool_inspection.rs`, `crates/goose/src/permission/permission_judge.rs`, `crates/goose/src/permission/permission_inspector.rs`, `crates/goose/src/agents/extension.rs`
- [The 2026-07-28 MCP specification](https://blog.modelcontextprotocol.io/posts/2026-07-28/)
- [Stateless MCP: what 2026-07-28 changes for security — Equixly](https://equixly.com/blog/2026/08/05/stateless-mcp/)
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
- [The Linux Foundation — GSoC 2026 project ideas](https://github.com/LinuxFoundationGSoC/ProjectIdeas/wiki/Google-Summer-of-Code-2026)
- [Kubescape MCP server](https://kubescape.io/docs/mcp-server/)
- [goose — managing tool permissions](https://goose-docs.ai/docs/guides/managing-tools/tool-permissions/)
