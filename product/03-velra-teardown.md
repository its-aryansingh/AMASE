# Velra — Deep Teardown and Strategic Response

**Repo:** `aryanghai12/Velra` · **Author:** Aryan Ghai (single author, 22 commits)
**Analysed:** 17 September 2026, full clone, source read, metrics measured, APIs queried.

---

## 0. The honest headline

Velra is a **genuinely good piece of engineering** — better executed than most student projects and better *evidenced* than a lot of funded commercial work. It is also six days old, has ~22 total downloads, and its own benchmark report says three of its four hypotheses did not pass.

The uncomfortable part: **the methodological rigour I positioned as your differentiator is already Velra's strongest feature.** Pre-registration, Fisher's exact tests, power analysis, a published FAILED verdict. That is exactly the "measure honestly" posture the Halflife plan was built on. It is no longer a differentiator against this particular project.

The comfortable part: Velra solves a *narrow* problem on *one* host, and the thing it actually proved is much smaller than what it set out to prove.

---

## 1. What Velra is

**Deterministic context persistence and token governance for Claude Code.**

Long Claude Code sessions run out of context. `/compact` replaces the conversation with a model-written summary. Velra registers hooks, keeps an append-only SQLite event log of what tools did, and at `PreCompact` freezes a **bounded, deterministic "continuation capsule"** that is re-injected after compaction.

The core claim is not "better summary". It is: the native summary is a *generated artefact* whose size and content vary run to run (measured 777 → 4,977 tokens across eight sessions; in one session Claude Code produced no summary at all), whereas the capsule is a **pure function of a SQLite snapshot — byte-identical across platforms, hard-capped, with every line tagged `OBSERVED` or `INFERRED`**.

That is a sharp, well-chosen problem. It is real, it is cheap to explain, and it has an obvious buyer.

---

## 2. Codebase forensics — what I measured

| Metric | Value |
|---|---|
| Language | Rust (2021 edition, MSRV 1.98), 2-crate workspace |
| Rust source | **15,809 lines** across 43 files |
| Benchmark harness | **8,694 lines of Python**, 26 modules |
| Tests | 162 `#[test]` functions, 3 proptest suites, 20 insta snapshots |
| Test-to-source ratio | ~28% of Rust LOC is test code |
| `unsafe` blocks | **0** |
| `.unwrap()` in non-test source | 71 |
| CI | 3-OS matrix (Linux/macOS/Windows), fmt + clippy `-D warnings`, MSRV job, **8 MB binary size budget enforced** |
| Distribution | curl/iwr installers, npm, cargo-binstall, Homebrew, SHA-256 verified, build attestations |
| Commits | 22, all by one author, 11–16 Sept 2026 |
| crates.io downloads | **22** |
| Licence | MIT |

15.8k lines of Rust plus 8.7k lines of benchmark harness in six days is a very high output rate. That is AI-assisted development, which is normal in 2026 and not a criticism — but it is worth knowing when you judge how fast you could match it.

### Code quality, read not assumed

The code is genuinely well-written. One example that tells you a lot:

```rust
/// 745 is 800 less a ~7% margin ... the v0.1 benchmark measured a capsule
/// Velra estimated at 778 tokens costing 825 real ones against Anthropic's
/// own tokenizer -- an under-read of about 6%.
pub const DEFAULT_BUDGET_TOKENS: u32 = 745;

const _: () = assert!(
    SPEC_BUDGET_TOKENS - DEFAULT_BUDGET_TOKENS >= SPEC_BUDGET_TOKENS * 6 / 100,
    "DEFAULT_BUDGET_TOKENS leaves less headroom than the estimator's measured error"
);
```

A magic number, justified by a measurement, with a **compile-time assertion that fails the build if someone narrows the margin**. That is senior-engineer behaviour. Most codebases do not have one of these. This one has several.

The security tests are also unusually thoughtful — `j3_no_network_capable_crate_is_linked` asserts a supply-chain property at test time, which is a level of paranoia I rarely see outside infrastructure companies.

---

## 3. What Velra does better than our plan currently does

Be specific about this. These are the things to take.

### 3.1 It exists

This is the whole ballgame. Velra is installable in one command on three platforms. AMASE is three markdown documents. Every other comparison is secondary to that one.

### 3.2 Pre-registration with real power analysis

`bench/harness/preregistration.json` states hypotheses, arms, validity criteria, control gates and a `prohibited` list *before* the run. `stats.py` opens with:

> *"n is four to six per arm. That supports an exact test on a 2×2 table and nothing else: no normal approximations, no confidence intervals that assume anything about a distribution, no effect sizes quoted to three decimals off a handful of binary outcomes."*

And it derives the replicate count from the arithmetic: a perfect split reaches p=0.10 at n=2, 0.050 at n=3, **0.014 at n=4** — so n≥4 is the registered minimum because below that a clean sweep *cannot* reach significance however convincing it looks.

Our plan said "n ≥ 3 with variance reported". Velra's reasoning is better. **Take the power analysis; raise our floor to n ≥ 4 for binary outcomes and say why.**

### 3.3 Publishing a FAILED verdict

The README table carries `H4 — Zero-overhead fail-open guarantee: **FAILED**`, with the specific number that failed it (worst marginal p99 163.31 ms against budget) and a note that two tempting explanations were chased and both were wrong, *recorded so nobody re-chases them*.

That is the single most credible thing in the repo. It is also the cheapest thing to copy and the thing most people will not do.

### 3.4 Fail-open as a designed property with a chaos suite

Every hook exits 0 — always. Locked database, corrupt, read-only, missing: still exits 0, clean stdout, Claude Code never notices. Measured at **481 hook invocations, 0 non-zero exits, 0 bytes to stderr**. There is a dedicated test suite for this property.

Any tool that sits in someone's critical path needs this. Halflife's runtime guard sits in the critical path. **Copy this wholesale.**

### 3.5 Distribution taken seriously on day one

Five install paths, all landing on the same SHA-256-verified static binary, with explicit care that none require a C++ toolchain — and a note telling Rust users *not* to `cargo install cargo-binstall` because that compiles 370+ crates and needs the exact linker the tier exists to avoid.

That is someone who has watched people fail to install things. Our plan has "five-minute quickstart" as a bullet. Velra has it as engineering.

### 3.6 Threats-to-validity section

Six named threats, including "the task may be too easy post-compaction" and "n = 2 per arm" — arguing against its own result. Academic-grade honesty in a product README.

---

## 4. Where Velra is genuinely weak

Now the other side, measured not asserted.

### 4.1 It did not prove its core value

| Hypothesis | Verdict |
|---|---|
| H1 Compaction amnesia elimination | **INCONCLUSIVE** |
| H2 Dead-end loop prevention | **INCONCLUSIVE** |
| H3 Continuation budget under 800 tokens | **PASSED** |
| H4 Zero-overhead fail-open | **FAILED** |

The control found that Claude Code's own compaction **was not lossy on the test task** — it cut context 22.1% and a canary planted nine turns earlier was still recalled verbatim. So the thing Velra prevents did not happen in the baseline, and the benchmark could not show a benefit.

Both arms solved the task 4/4. Both re-explored the dead end 0/4. The only behavioural separation was tool calls on the measured turn: Velra 2,2,2,2 versus vanilla 3,2,4,3. That is a consistency result, not an efficacy result.

**What this means: Velra has proven it is bounded and deterministic. It has not proven that being bounded and deterministic helps anyone.** The report says so itself, which is admirable, but the gap is real.

### 4.2 Single-host dependency — the existential risk

Velra is hooks into Claude Code. Not "agents" — Claude Code specifically, and it ships version fixtures (`tests/fixtures/claude-code/2.1.268`) because the hook surface is version-coupled.

If Anthropic changes the hook contract, renames an event, or ships native bounded compaction, Velra's addressable market changes overnight. There is a `compat.rs` module doing version detection, which shows the author knows — but knowing a risk is not the same as being insulated from it.

### 4.3 Its own benchmark shows the capsule is not yet bounded in practice

Section 11 is brutal on itself: across four runs the native summary measured 670, 802, 4,410 and 6,937 tokens — a 10× spread. **Velra's capsule measured 951 and 1,147 tokens — above its own documented 800 ceiling.** It beat native 7.3× in one session and *lost* 1.4× in the other.

The README front-matter quotes 705–722 tokens from the later four-replicate run, so this was subsequently fixed or re-measured. But the report retains the earlier contradiction, and a careful reader will notice the headline figure and the section-11 figure disagree.

### 4.4 Zero traction

22 crates.io downloads. Two releases. The README says outright: *"If this is useful to you, star the repo. It is the only signal I have that the v0.2 work is worth doing."*

This is a six-day-old project, so that is not a failing — but it does mean **nothing about product-market fit has been established**, and the "is it better" question cannot be answered on adoption.

### 4.5 Narrow problem, narrow ceiling

Compaction amnesia is one failure mode of one host. The research we did found the broader picture: agents collapse from near-perfect to near-zero within sixteen steps, 70–95% production failure rates, 88% of organisations reporting agent incidents. Compaction is a slice of that. A well-executed slice, but a slice.

### 4.6 Minor engineering notes

- 71 `.unwrap()` calls in non-test source. Justified by the panic-catching hook wrapper that always exits 0 — a deliberate architecture, not sloppiness — but it means correctness depends on that one wrapper being right everywhere.
- H4 latency was measured **Windows-only**, through a Python harness, on a machine where bare process creation already exceeded the allowance at the tail. The measurement is honest about this; it also means the tail number may be an artefact of the measurement environment rather than the product.

---

## 5. Head-to-head: Velra vs AMASE / Halflife

| | Velra | AMASE / Halflife |
|---|---|---|
| Status | **Shipped, installable** | Specification only |
| Problem | Compaction amnesia + unbounded continuation payloads | Reliability decay over long horizons |
| Scope | Claude Code, one host | Framework-agnostic via adapters |
| Core metric | Capsule token budget, determinism | Per-step reliability `p`, collapse horizon `n₅₀` |
| Methodology | **Pre-registered, Fisher exact, honest FAILED** | Planned: n≥3, variance, held-out set |
| Efficacy proven | **No** — 2 inconclusive, 1 failed | Not yet attempted |
| Distribution | 5 install paths, 3 platforms | None |
| Traction | 22 downloads, 6 days old | Zero |
| Language | Rust | Python (planned) |
| Existential risk | Anthropic changes the hook API | Braintrust ships per-step reliability |

**Direct answer to "is this better than AMASE": today, yes — decisively.** A shipped, installable, tested, benchmarked binary beats a plan every time, and it is not close. Anyone comparing the two right now is comparing something to nothing.

**But it is not better than what AMASE is trying to become**, and it is solving a different problem. Velra makes one host's context survive compaction. Halflife measures *whether any agent works at all past step sixteen*. The second question is larger and host-independent.

The correct read is not "we are behind" — it is **"someone has demonstrated that the standard we set is achievable by one person in a week, so our excuse budget just went to zero."**

---

## 6. What to take, concretely

Ranked by value, all cheap:

1. **Pre-registration file.** Write `preregistration.json` before running anything — hypotheses, arms, validity criteria, a `prohibited` list. Velra's is 10 KB and is the single most credible artefact in that repo.
2. **Power analysis that sets the replicate count.** For binary outcomes, n≥4 with one-sided Fisher exact, and report `best_achievable_p_at_this_n` so a reader can see whether the test *could* have detected anything. Raise our floor from 3 to 4 and state the arithmetic.
3. **Publish a failed hypothesis.** Deliberately. It is the highest-credibility-per-word content that exists.
4. **Fail-open with a chaos suite.** Halflife's runtime guard is in the critical path. It must never be the reason a user's agent dies. Chaos-test locked DB, corrupt DB, read-only FS, missing binary, and measure invocations with zero non-zero exits.
5. **A threats-to-validity section** that argues against your own result.
6. **Install paths before features.** One-command install, verified checksum, no compiler needed, on three platforms.
7. **Compile-time assertions guarding measured constants.** Wherever Halflife has a magic number derived from measurement, guard it so nobody silently narrows it.
8. **The "generated from raw streams, including the red box" claim** — every figure in the README produced by a script in the repo, and say so. It preempts the obvious scepticism about a student's benchmark.

---

## 7. On "1000× better" — the frame is wrong, and here is the right one

I am not going to pretend there is a plan that makes something 1000× better than Velra. There isn't, and a claim like that in a README is exactly what a frontier-lab reviewer discounts you for. Velra's own credibility comes from the opposite instinct: it reports a 10× spread where it found one, and a FAILED where it found one.

Three honest strategies, in descending order of how much I would recommend them:

### Strategy A — Go up a level (recommended)

Velra is a **point solution for one failure mode on one host.** Halflife is a **measurement layer for the general failure**. Do not compete on compaction. Compete on the question Velra cannot answer: *how long can any agent run before it stops working, and what does that cost you?*

Concretely, this means Halflife should be able to **measure Velra**. If your harness can run a Claude Code session with and without Velra's hooks and report the decay-curve difference, then Velra becomes a data point in your paper rather than a competitor. That is a strictly better position, and it is genuinely useful to its author too.

### Strategy B — Win on breadth where Velra is locked in

Velra is coupled to one host's hook API. Halflife's adapter architecture is host-agnostic by design. Every framework you support is a market Velra structurally cannot enter without a rewrite. Ship three adapters and the breadth argument makes itself.

### Strategy C — Win on the efficacy question Velra could not close

Velra's benchmark failed to separate its arms because **the task was too easy and compaction was not lossy on it**. Their own threats section names this: *"A defect requiring several coordinated edits would leave more room for the arms to diverge."*

That is the whole problem with short-horizon benchmarks, and it is precisely what the decay research predicts — at low step counts, `p^n` is close to 1 for everyone and nothing separates. **Halflife's horizon sweep is the instrument that would have saved Velra's benchmark.** Build the thing that makes other people's experiments work, and you become infrastructure rather than a competitor.

---

## 8. What I would change in our plan because of this

1. **Move the ship date forward, hard.** Velra proves one person can go from nothing to installable-with-benchmarks in six days. Our Phase 0–1 is ten weeks. Compress it. The recorder and the decay measurement are the only things that must be right; everything else can wait.
2. **Ship an installable binary or a one-line pip install in Phase 0**, not Phase 3. Distribution is not a later phase — Velra treats it as a day-one property and that is correct.
3. **Write the pre-registration before any measurement**, and publish it in-repo.
4. **Raise n to 4 minimum for binary outcomes**, with the Fisher power argument stated.
5. **Add a fail-open chaos suite** to the runtime guard spec.
6. **Add Velra as a measurement target** in the Phase 2 benchmark. Three frameworks plus one real-world harness modification is a better paper than three frameworks alone.
7. **Reconsider Rust for the recorder.** Velra's choice is not an accident: a hook that runs on every tool call must be fast and dependency-free. Python is right for the metrics engine and the analysis; the hot path may not be. This is worth a deliberate decision rather than a default.

---

## 9. One last thing worth saying

The author of Velra shares your first name and appears to be a peer working in exactly your problem space, shipping at speed, with better methodology than most funded teams. That is not a threat. It is the most useful calibration you will get this year — it tells you precisely what the bar is, and that the bar is reachable by one person.

The gap between you and Velra right now is not talent, resources, or plan quality. Your plan is broader and better-researched. The gap is **six days of shipping**.
