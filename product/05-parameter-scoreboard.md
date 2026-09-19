# Beating Velra on Every Parameter — The Domination Plan

17 September 2026 · written from the cloned repo, measured numbers, and a fresh literature pass

---

## 0. Read this first

You asked to beat Velra on every parameter. Here is the honest structure of that:

- **On ~14 parameters you can win by an order of magnitude or more.** These are real, and the techniques are known.
- **On ~6 parameters Velra is already at the ceiling.** Zero `unsafe` cannot be beaten, only matched. 481 hook invocations with 0 non-zero exits cannot be improved on. On these, you **match and then add a level above** — which is still winning, but by extension rather than by exceeding.
- **On 1 parameter you are behind and will stay behind for weeks.** It exists and you don't. Only shipping fixes that.

Anyone who tells you all 30 parameters can be beaten 1000× is selling something. What follows is what can actually be done, with the technique and the proof for each.

---

## 1. The scoreboard

Velra's numbers are from its own `BENCHMARK_REPORT.md` and source, read directly.

| # | Parameter | Velra | Target | Multiple |
|---|---|---|---|---|
| **A. Economic** ||||
| 1 | Value per intervention | $0.004–$0.031 | $2–$15 typical | **~1,200×** |
| 2 | Intervention frequency | 1–2 per session | every tool call | **~50×** |
| 3 | Value compounding | none (pure function) | calibration corpus | **∞ → finite** |
| **B. Performance** ||||
| 4 | Hook handler type | `command` (spawn per call) | `http` (resident server) | architectural |
| 5 | Process-spawn floor | 4.27 ms p50 / 24.95 ms p99 | **eliminated** | **∞** |
| 6 | Marginal p50 | 3.455–10.635 ms | < 0.2 ms | **~25×** |
| 7 | Worst marginal p99 | **163.307 ms** | < 1 ms | **~163×** |
| 8 | Own latency verdict | **FAILED** | PASSED | qualitative |
| 9 | Tokens consumed by the tool | 0 | 0 | tie (both correct) |
| **C. Coverage** ||||
| 10 | Hosts supported | 1 (Claude Code) | 3+ via adapter | **3×** |
| 11 | Handler types used | 1 of 4 | 2 of 4 (`http` + `command` fallback) | **2×** |
| 12 | Version coupling | fixtures per CC version | host-agnostic core | structural |
| **D. Correctness assurance** ||||
| 13 | `unsafe` blocks | 0 | 0 | **tie at ceiling** |
| 14 | Tests | 162 + 3 proptest + 20 snapshots | match, then exceed | ~2× |
| 15 | `.unwrap()` in non-test src | 71 | **0**, enforced by lint | **71 → 0** |
| 16 | Formal verification | none | **Kani proof harnesses** | **0 → n** |
| 17 | Fuzzing | none evident | `cargo-fuzz` on trace parser | **0 → n** |
| 18 | Fail-open record | 481 invocations, 0 failures | match + chaos matrix | **tie at ceiling** |
| **E. Supply chain** ||||
| 19 | Install paths | 5 | 5 | tie |
| 20 | Checksums + attestations | yes | yes | tie |
| 21 | Reproducible builds | not claimed | **bit-identical, verified in CI** | **0 → 1** |
| 22 | SLSA level | unstated | **L3, stated and verifiable** | qualitative |
| 23 | Binary size budget | 8 MB | < 3 MB | **~2.7×** |
| **F. Scientific method** ||||
| 24 | Pre-registration | yes | yes | tie |
| 25 | Statistical test | fixed-n Fisher exact | **anytime-valid sequential** | qualitative |
| 26 | Stopping rule | fixed n≥4 | data-dependent, still valid | structural |
| 27 | Guarantee offered | byte-determinism | **certified recall bound** | different class |
| 28 | Hypotheses passed | **1 of 4** | 3 of 4 minimum | **3×** |
| **G. Product** ||||
| 29 | Docs | ~145 KB, 5 files | match + API docs site | ~1.5× |
| 30 | Traction | 22 downloads | — | **behind** |
| 31 | Existence | **shipped** | — | **behind** |

---

## 2. The architectural unlock — and it is a big one

**Claude Code has supported HTTP hooks since February 2026.** Four handler types exist: `command`, `http`, `prompt`, `agent`. Velra uses only `command`.

```json
{
  "type": "http",
  "url": "http://127.0.0.1:8787/hooks/pre-tool-use",
  "timeout": 30,
  "headers": { "Authorization": "Bearer $SCRAM_TOKEN" },
  "allowedEnvVars": ["SCRAM_TOKEN"]
}
```

The server receives *the exact same JSON that command hooks get on stdin*, as a POST body. To block a tool call you return a **2xx** response with the decision fields in the body — not a non-2xx status.

### Why this is the whole ballgame on performance

Velra's own measurements isolate a **spawn control** — the same binary with `VELRA_DISABLE=1`, exiting immediately without touching the database:

| | p50 | p99 |
|---|---:|---:|
| Spawn control (38 MiB DB machine) | **4.27 ms** | **24.95 ms** |
| Spawn control (small DB machine) | 3.87 ms | 7.22 ms |

**That cost is paid before Velra executes a single instruction.** It is OS process creation, and no amount of Rust optimisation removes it. It is the floor of the `command` architecture, and it is why H4 failed: worst absolute p99 **172.259 ms**, of which **163.307 ms is marginal**.

With an HTTP hook there is no fork, no exec, no dynamic linking, no SQLite open per call. A resident Rust server handling a small JSON POST on loopback operates in **tens of microseconds**. The 163 ms tail does not get optimised away — **it ceases to exist as a category**.

Velra's README presents "no daemon, no background process" as a feature. It is a real tradeoff — a daemon is one more thing to manage — but it is precisely the tradeoff that cost them their one failed hypothesis. The correct answer is **both**: HTTP by default, `command` binary as automatic fallback when the server is not running, so you get the latency of a daemon and the robustness of no-daemon.

### Design consequences

- Hot path does **zero** disk I/O. Append to an in-memory SPSC ring buffer; a background thread drains to disk.
- Hot path does **zero** allocation. Pre-allocated arenas, `serde` into stack buffers, no `String` construction.
- The predictor runs on fixed-size feature vectors updated incrementally — no recomputation over history.
- **Enforce it in CI:** a criterion benchmark that fails the build if p99 exceeds 1 ms. Velra has an 8 MB binary budget gate; this is the same discipline applied to the number that actually failed for them.

---

## 3. Correctness: matching the ceiling, then going above it

### 3.1 The `.unwrap()` gap — 71 → 0

Velra has **71 `.unwrap()` calls in non-test source**. Their architecture justifies it: a panic-catching hook wrapper always exits 0, so a panic degrades to a no-op. That is defensible.

But it means correctness rests on one wrapper being right in every path. In an HTTP server handling concurrent requests, a panic is a different proposition — it can poison state or kill a worker.

**Target: zero `.unwrap()` in non-test source, enforced by `#![deny(clippy::unwrap_used, clippy::expect_used, clippy::panic)]` at crate root.** This is checkable in one line of CI and is a visible, objective win.

### 3.2 Formal verification — the level above tests

Velra has 162 tests, 3 proptest suites, 20 snapshots. Excellent, and above the norm. The level above it is **Kani**, AWS's bounded model checker for Rust.

Kani compiles proof harnesses from Rust MIR into CBMC and verifies:

- absence of undefined behaviour — invalid/dangling pointers, misaligned casts, invalid enum discriminants
- absence of runtime panics — arithmetic overflow, division by zero, undefined shift, out-of-bounds indexing, `unwrap()` on `None`
- functional correctness against contracts

It is production-proven: proof harnesses run on every code change in **Firecracker**, **s2n-quic**, **Hifitime**, and the Rust standard library verification campaign runs **over 16,000 harnesses per code change**. It found eleven previously unknown bugs across industrial projects. Verification is fast — a worked GCD contract verified 202 checks in **0.54 s**.

**What to verify in SCRAM:**

| Harness | Property |
|---|---|
| `ring_buffer_never_overruns` | the SPSC buffer cannot corrupt on wraparound |
| `feature_update_no_overflow` | incremental feature arithmetic cannot overflow for any input sequence |
| `gate_decision_total` | the governor returns a decision for every possible feature vector — no panic path |
| `threshold_monotone` | raising the recall target can never increase the abort rate |
| `redaction_never_leaks` | no secret pattern survives the redactor, for any input |

That last one is the killer. Velra tests redaction with a corpus (`j1_every_secret_is_redacted`). **Proving it for all inputs within a bound is a categorically stronger claim**, and it is the kind of statement that makes a security-minded reviewer sit up.

### 3.3 Fuzzing

`cargo-fuzz` on the trace parser and the hook JSON deserialiser, run in CI with a seed corpus. Velra shows no fuzzing. Adding it is cheap and closes a class Kani's bounds do not reach.

### 3.4 Fail-open — match, do not pretend to beat

Velra: **481 in-session invocations, 0 non-zero exits, 0 stderr bytes**, plus 4,050 more under load. Perfect. You cannot beat perfect.

**Match it and extend the matrix.** Velra chaos-tests locked, corrupt, read-only and missing databases. Add: server down, server hung past timeout, port taken, token invalid, disk full, clock skew, concurrent session storm. Report the same 0/N table over a larger N and a wider fault set.

---

## 4. Supply chain: two things Velra does not claim

Velra already does SHA-256 verification and build attestations. Two levels above:

**Reproducible builds.** Build the same tag twice on different machines, assert bit-identical artefacts, in CI. Requires pinned toolchain, `--remap-path-prefix`, `SOURCE_DATE_EPOCH`, sorted inputs, no embedded timestamps. Velra does not claim this. A verified-reproducible security tool is a materially stronger claim than a checksummed one — it means a user can rebuild and confirm the binary matches the source, without trusting your CI.

**SLSA Level 3, stated and verifiable.** Velra emits attestations; the level is unstated. Generate provenance from a trusted builder and publish the `slsa-verifier` command in the README so anyone can check it in one line.

**Binary size.** Velra's CI gate is 8 MB. Target under 3 MB: `opt-level = "z"`, `lto = "fat"`, `codegen-units = 1`, `strip = true`, `panic = "abort"` where the architecture permits, and audit the dependency tree. Note Velra deliberately uses `panic = "unwind"` because their wrapper catches panics — with zero `unwrap()` and Kani-proven panic-freedom, `abort` becomes available, which is both smaller and faster.

---

## 5. Scientific method: the level above Fisher

Velra's `stats.py` is genuinely good — one-sided Fisher exact, and n≥4 derived from the arithmetic that a perfect split reaches p=0.10 at n=2, 0.050 at n=3, **0.014 at n=4**.

Its limitation is structural: **it is a fixed-n design.** You must choose n in advance. Stop early because the result looks clear and the p-value is invalid. Continue because it looks close and it is invalid too.

### Anytime-valid sequential testing

Recent work shows this trade-off is unnecessary. *Anytime validity is free: inducing sequential tests* proves that for any valid fixed-sample test φ_N you can construct a sequence (φ'_n) that is **anytime valid** — Type I error stays ≤ α even when the stopping time depends on the data — and **power-matched**, with φ'_N = φ_N at the planned sample size. The construction is the conditional expectation of φ_N given observations so far, under the null.

The paper's own framing: this "demolishes a common assumption that anytime validity must surely come at a price" in power.

**Practical consequence for you:** you can run agents continuously, monitor the evidence as it accrues, stop the moment a result is certified, and keep going when it is not — all without invalidating anything. For a student paying for inference, that is not just methodologically better, it is **cheaper**, because you stop the moment you have the answer.

Report both: the anytime-valid sequential result *and* the fixed-n Fisher result at the same n, so it is directly comparable to Velra's table.

### A different class of guarantee

Velra guarantees **byte-determinism** — the capsule is a pure function of a snapshot. Strong, and verifiable.

SCRAM guarantees a **certified recall bound**: "at least 95% of runs that would have succeeded survive the cascade," backed by Clopper-Pearson bounds on a frozen held-out split. That is a statement about future behaviour on unseen data, which byte-determinism is not.

The certification arithmetic is fixed and worth planning around: `n ≈ ln(α)/ln(ρ*)` — **114 successful episodes certify up to 0.974, 149 certify 0.98, 299 certify 0.99.** Independent of how good your predictor is. Start collecting on day one.

### Beat the verdict table

This is the one that matters most to a reviewer. Velra's own scoreboard:

| | Velra | SCRAM target |
|---|---|---|
| Hypotheses passed | **1 of 4** | **3 of 4** |
| Why they failed | control found compaction not lossy; task too easy | choose a task class where the effect exists, and prove it exists *before* the main run |

Velra's threats section names the root cause: *"A defect requiring several coordinated edits would leave more room for the arms to diverge."* Their arms could not separate because the baseline never failed.

**Your protocol must include a positive control run first**: demonstrate the phenomenon exists on your task set before spending money measuring an intervention against it. That single procedural addition is what converts INCONCLUSIVE into a real verdict, and it is directly learned from their failure.

---

## 6. The parameters where you are simply behind

Honesty costs nothing here and buys credibility everywhere.

| Parameter | Reality |
|---|---|
| **Existence** | Velra ships. You do not. Nothing on this page matters until that changes. |
| **Traction** | 22 crates.io downloads is near-zero, but it is more than zero. |
| **Elapsed proof** | Six days from nothing to installable, benchmarked, five install paths. That is the bar. |

The only response is the week-one deliverable: `scram waste`, a small tool that reads a trace and reports what fraction of tokens were burned after the first warning signal. Ship it before anything else. It gives strangers a reason to use you, and it is how the calibration corpus gets collected.

---

## 7. Ordered execution — what to do, in what order

Ordered by (credibility gained) ÷ (effort), which is not the same as the order the parameters are listed.

| # | Action | Effort | Wins parameter |
|---|---|---|---|
| 1 | Ship `scram waste` | 2 days | existence, traction, corpus |
| 2 | HTTP hook server + command fallback | 4 days | 4, 5, 6, 7, 8 |
| 3 | Criterion p99 gate in CI at 1 ms | 1 day | 7, 8 |
| 4 | `deny(unwrap_used)` at crate root | 1 hour | 15 |
| 5 | Fail-open chaos matrix | 3 days | 18 |
| 6 | Pre-registration file, committed before any run | 1 day | 24, 26 |
| 7 | Positive control in the protocol | 1 day | **28** |
| 8 | Kani harnesses, redaction first | 5 days | 16 |
| 9 | Anytime-valid sequential test | 3 days | 25, 26 |
| 10 | Reproducible build verification in CI | 2 days | 21 |
| 11 | `cargo-fuzz` on parsers | 2 days | 17 |
| 12 | SLSA L3 provenance + verify command in README | 2 days | 22 |
| 13 | Binary under 3 MB | 2 days | 23 |
| 14 | Second host adapter | 5 days | 10, 12 |
| 15 | The certified recall result | ongoing | 27, 1, 2, 3 |

Items 1–7 are about three weeks and win the majority of the scoreboard. Items 8–13 are the assurance tier that makes the project read as infrastructure rather than a student build.

---

## 8. The comparison table you will publish

When SCRAM ships, this is the artefact — and every row must be a measurement you can reproduce, not a claim.

| Parameter | Velra 0.1.1 | SCRAM 0.1 |
|---|---|---|
| Intervention point | compaction boundary | every tool call |
| Value per intervention | $0.004–$0.031 | measured, published |
| Hook transport | command (process spawn) | HTTP (resident) + command fallback |
| Marginal p50 | 3.455–10.635 ms | *target* < 0.2 ms |
| Worst marginal p99 | 163.307 ms | *target* < 1 ms |
| Latency verdict | FAILED | *target* PASSED |
| `unsafe` | 0 | 0 |
| `.unwrap()` in src | 71 | 0, lint-enforced |
| Formal verification | — | Kani harnesses |
| Fuzzing | — | cargo-fuzz |
| Fail-open | 481 / 0 failures | matched, wider fault matrix |
| Reproducible build | not claimed | verified in CI |
| Binary budget | 8 MB | < 3 MB |
| Statistics | fixed-n Fisher | anytime-valid + Fisher |
| Guarantee | byte-determinism | certified recall bound |
| Hypotheses passed | 1 of 4 | *target* 3 of 4 |
| Hosts | 1 | 3 |

Publish it with Velra credited by name and its strengths stated plainly. A comparison table that admits the competitor got 0 `unsafe` and a perfect fail-open record is believed; one that claims total superiority is not.

---

## Sources

- Velra repository — source, `BENCHMARK_REPORT.md`, `DECISIONS.md`, read directly from a clone
- [Claude Code hooks reference](https://code.claude.com/docs/en/hooks)
- [Claude Code hooks: all lifecycle events and handler types](https://claudefa.st/blog/tools/hooks/hooks-guide)
- [Kani: A Model Checker for Rust (arXiv:2607.01504)](https://arxiv.org/html/2607.01504v1)
- [Anytime validity is free: inducing sequential tests (arXiv:2501.03982)](https://arxiv.org/html/2501.03982)
- [Anytime validity is free — JRSS-B](https://academic.oup.com/jrsssb/advance-article/doi/10.1093/jrsssb/qkag050/8493290)
- [Doomed from the Start: Early Abort of LLM Agent Episodes (arXiv:2607.06503)](https://arxiv.org/html/2607.06503)
- [SLSA build provenance specification](https://slsa.dev/spec/draft/build-provenance)
- [slsa-verifier](https://github.com/slsa-framework/slsa-verifier)
- [min-sized-rust](https://github.com/johnthagen/min-sized-rust)
- [Anthropic API pricing, September 2026](https://benchlm.ai/anthropic/api-pricing)
