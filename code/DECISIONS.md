# Decisions

Each entry records what was chosen, what was measured, and what would overturn it.

---

## D1 · Python for everything, until measurement says otherwise

**Decision.** AMASE is written in Python. Both halves — the offline `measure`
pipeline and the hot-path `guard` server — start in Python. No Rust, no Go, no
polyglot split.

### The measurement that decided it

The competing product in this space is written in Rust and registers `command`
hooks, so Claude Code spawns a process per tool call. Its published benchmark
isolates a **spawn control** — the same binary exiting immediately, doing no
work at all:

| | p50 | p99 |
|---|---:|---:|
| Rust binary, spawn control only | 4.27 ms | 24.95 ms |
| Rust binary, worst marginal p99 | — | **163.31 ms** |

Against that, Python serving the **full guard workload** over an HTTP hook —
JSON parse, incremental feature update, gate evaluation, allow/deny response —
measured on loopback, 3,000 requests after 200 warm-up:

| | Python + `http.server` |
|---|---:|
| mean | 0.741 ms |
| p50 | **0.722 ms** |
| p90 | 0.876 ms |
| p99 | **1.142 ms** |
| p99.9 | 3.456 ms |
| max | 6.181 ms |

**Python over HTTP is roughly 6× faster at p50 and 22× faster at p99 than Rust
paying for a process spawn.** The comparison is not close, and it is not about
the language.

### Why the language barely matters here

Timing the decision logic alone, with no HTTP transport:

| | pure compute |
|---|---:|
| p50 | **3.43 µs** |
| p99 | 9.18 µs |
| p99.9 | 25.73 µs |

The actual governor arithmetic is **3.4 microseconds**. HTTP transport is
**99.5%** of the 722 µs total. A Rust rewrite of the compute would recover
about 3 µs out of 722 — under half a percent — while the architectural choice
between `http` and `command` hooks is worth two orders of magnitude.

This is the general lesson, and it is why the competing project's Rust did not
save it: **choosing a fast language does not fix a slow architecture.** They
failed their own latency hypothesis while written in Rust, because the
bottleneck was `fork`/`exec`, not instructions.

### The other reasons, in order of weight

1. **The calibration pipeline is the moat, and it is statistical.**
   Clopper-Pearson recall bounds, FSM induction from traces, AUROC curves,
   anytime-valid sequential tests. In Python these are `scipy` calls. In Rust
   they are a project of their own. Spending the moat's build time fighting
   numeric bindings would be a bad trade.
2. **Ship speed is the top risk.** The comparable Rust product runs to ~15,800
   lines. `amase waste` is 1,146 lines of Python with 19 tests. The single most
   likely failure mode for this project is not shipping, and Python removes
   weeks.
3. **Python is where the users and the reviewers are.** Python appears in 75.2%
   of AI engineering job postings. Agent developers already have it installed.
4. **`unsafe`-free by construction.** Memory-safety parity with Rust, for free,
   in the class of bugs that matters for a tool sitting in someone's critical
   path.

### What Python costs, stated plainly

- **Install friction.** `pip install amase` needs Python ≥ 3.10; a Rust binary
  needs nothing. Mitigated by shipping a `uvx amase` path so there is a
  zero-install option, but it is a real disadvantage and not pretended away.
- **Tail latency from GC.** p99.9 of 3.46 ms and a 6.18 ms max show the tail
  exists. It is still an order of magnitude inside the competing product's p99.
- **No single static artefact.** Distribution is a wheel, not a file.

### Measurement caveats

Measured in a Linux container, single client, sequential requests, with the
stdlib `http.server` — the slowest reasonable option, chosen so the number is a
floor rather than a best case. Windows loopback and Windows process creation are
both slower than Linux, which widens the gap in Python's favour, since the
competing product's worst figures are Windows-measured. A tuned `asyncio` or
`uvicorn` server would improve on these numbers.

### What would overturn this

A port is justified when, and only when, one of these is measured — not
anticipated:

- **`guard` p99 exceeds 5 ms** in real use on a real agent session. The CI
  benchmark gates this; if it trips and profiling shows interpreter overhead
  rather than a fixable hot spot, port the server.
- **A hosted tier needs thousands of concurrent sessions.** Single-session
  local governance is not a concurrency problem; a multi-tenant service is.
- **The tail becomes user-visible.** If GC pauses produce a p99.9 that agent
  authors complain about.

**If a port happens, the target is Go, not Rust.** Static binary, no runtime
dependency, sub-millisecond GC pauses, and it is a language already in hand —
which matters more than a marginal instruction-count advantage for a workload
whose compute is 3.4 µs. Rust would be the choice only if the hot path grew
genuinely complex, which the 3.4 µs figure says it has not.

**Only `guard` would be ported.** `measure`, the calibration pipeline and the
analysis stay Python permanently. There is no version of this project where
writing Clopper-Pearson bounds in Rust is the right use of a week.

### Guardrail

`amase guard` ships with a criterion-equivalent latency benchmark in CI that
fails the build above a stated p99 budget. The budget is the contract; the
language is an implementation detail underneath it. That is the correct order,
and it is the one the competing project inverted.

---

## D2 · Go was evaluated against Python and rejected for v0.1

**Decision.** Python for both halves in v0.1. Go stays on the table for `guard`
in v0.2, gated on a specific trigger stated below. No split-language repo yet.

### The controlled measurement

D1 compared Python against a Rust binary paying for process spawn. That
comparison was about architecture. This one is about language: the **same
workload, same machine, same identical Python client**, only the server
implementation differing. Controlling the client matters — a first attempt that
let each language use its own HTTP client reported Go at 28× faster, and almost
all of that was `urllib` being slow on the *client* side, which is not something
either server is responsible for. In production the client is Claude Code, not
`urllib`.

3,000 requests after 200 warm-up, loopback:

| | Go server | Python server | Go advantage |
|---|---:|---:|---:|
| p50 | **0.216 ms** | 0.578 ms | 2.7× |
| p90 | 0.307 ms | 0.762 ms | 2.5× |
| p99 | **0.573 ms** | 1.013 ms | 1.8× |
| p99.9 | 2.482 ms | 2.992 ms | 1.2× |
| max | 4.489 ms | **4.208 ms** | 0.9× |
| mean | 0.239 ms | 0.606 ms | 2.5× |

Go is genuinely faster — about **2.5–2.7× at the median**. But the advantage
narrows sharply into the tail, and at `max` the two are indistinguishable: in
this run Python was marginally better, which is noise, and the honest reading is
that both hit the same OS scheduling ceiling.

Python's async fast path was also measured against its own threading server to
make sure the baseline was not strawmanned: p50 improved from 0.722 ms to
0.577 ms, and p99 barely moved (1.142 → 1.099 ms). **Python's floor on this
workload is ~0.58 ms regardless of which server is used.** The threading server
was not the bottleneck; the interpreter is.

### Why this does not force a language change

The budget is **p99 < 5 ms**. Both pass:

| | p99 | headroom |
|---|---:|---:|
| Go | 0.573 ms | 8.7× |
| Python | 1.013 ms | 4.9× |
| *Rust with `command` hooks (competitor)* | *24.95 ms* | *fails* |

A 2.7× speedup on a number that is already 5× inside budget buys **0.36 ms per
tool call**. Over a 50-step agent run that is 18 ms, against LLM calls measured
in seconds. It is not perceptible and it is not the reason anyone would or would
not adopt this.

### Where Go genuinely wins, and it is not speed

1. **Distribution.** A stripped Go binary is 6.07 MB with no runtime dependency:
   `curl | sh` and it runs. Python needs ≥3.10 present. This is the real Go
   argument and it is a good one.
2. **Tail predictability.** Go's GC pauses are sub-millisecond by design.
   Python's p99.9 of 2.99 ms and 4.2 ms max are fine today but are the thing
   that would degrade first under load.
3. **It is already in hand.** Go is a language on this team; Rust is not. If a
   port ever happens, this is the target — D1 said so and the measurement
   confirms it.

### Where Python wins, decisively

1. **The calibration pipeline is the moat and it is statistical.**
   Clopper-Pearson bounds, FSM induction from traces, AUROC curves,
   anytime-valid sequential tests. `scipy` and `numpy` make these a day's work;
   in Go they are a subproject each. **This half is not a close call and will
   never move.**
2. **One language, one toolchain, one test suite.** A split repo costs two CI
   matrices, a JSON contract kept in sync across languages, and a higher
   contributor barrier — paid every week, to save 0.36 ms.
3. **Ship speed.** The single largest risk to this project is not shipping.

### The trigger for revisiting

Port `guard` to Go when **one** of these is observed, not anticipated:

- **Install friction shows up as real complaints.** Two or more users report
  that the Python requirement blocked them. This is the most likely trigger and
  the most legitimate one.
- **`guard` p99 exceeds 5 ms** on a real agent session and profiling blames the
  interpreter rather than a fixable hot spot.
- **A hosted tier needs real concurrency.** Single-session local governance is
  not a concurrency problem; a multi-tenant service is.

The hot path is roughly 150 lines — feature update, gate evaluation, response.
Porting it is a weekend, not a rewrite, and the JSON contract makes it a
drop-in. **Deferring the port costs almost nothing; doing it now costs weeks of
the only thing that is actually scarce.**

`measure`, the calibration pipeline and all analysis stay Python permanently,
in every future version.

---

## D3 · Final three-way verdict: Python, and the port target is now Rust, not Go

**Decision.** Python for v0.1 and for the calibration pipeline permanently. If
`guard` is ever ported, the target is **Rust** — this supersedes the Go
recommendation in D1 and D2, on evidence gathered after they were written.

### The complete measurement

D1 compared Python against Rust-with-process-spawn (architecture, not language).
D2 compared Python against Go (language, controlled client). Neither measured
Rust as an HTTP server, which left the three-way question open. This does: the
**same workload, same machine, same identical Python client**, three server
implementations, 3,000 requests after 300 warm-up.

| metric | Rust | Go | Python | Python ÷ Rust |
|---|---:|---:|---:|---:|
| p50 | **0.170 ms** | 0.215 ms | 0.607 ms | 3.6× |
| p90 | **0.212 ms** | 0.293 ms | 0.765 ms | 3.6× |
| p99 | **0.291 ms** | 0.526 ms | 1.061 ms | 3.7× |
| p99.9 | **0.630 ms** | 3.687 ms | 2.705 ms | 4.3× |
| max | **2.169 ms** | 10.611 ms | 3.151 ms | 1.5× |
| mean | **0.181 ms** | 0.240 ms | 0.631 ms | 3.5× |

Headroom against the p99 < 5 ms budget: **Rust 17.2×, Go 9.5×, Python 4.7×.**
All three pass.

Stripped artefact size: **Rust 0.32 MB**, Go 6.07 MB, Python wheel ~10 KB plus a
separately-installed runtime.

### Two findings that were not anticipated

**1. Rust's advantage over Python is 3.6×, not orders of magnitude.** The
folklore gap does not appear on this workload, because the workload is a small
JSON POST and a handful of hash-map updates. Loopback TCP and syscall overhead
dominate, and those are the same for everyone.

**2. Go showed no tail advantage — in this run its tail was the worst of the
three.** p99.9 of 3.687 ms and a 10.611 ms max, against Python's 2.705 ms and
3.151 ms. This directly contradicts the "Go for predictable sub-millisecond GC
pauses" reasoning in D1. One run is not enough to conclude Go's tail is
*worse*, and it may be scheduler noise — but it is enough to withdraw the claim
that Go's tail is *better*, which was asserted without measurement.

That removes Go's main technical argument. What remains for Go is "it is
already a known language", which is a real consideration but a weaker one than a
19× smaller binary and a strictly better tail.

### Why the answer is still Python

Latency does not discriminate. The spread between best and worst is **0.44 ms
per tool call** — roughly 22 ms across a 50-step run, against LLM calls measured
in seconds. No user will ever perceive it, and all three sit inside budget.

So the decision falls to the factors that do discriminate:

| | Python | Go | Rust |
|---|---|---|---|
| Statistical ecosystem (**the moat**) | `scipy`, `numpy`, `sklearn` | a subproject each | a subproject each |
| Time to ship | fastest | moderate | slowest |
| Language already known | yes | yes | **no** |
| Distribution artefact | needs runtime | 6.07 MB binary | **0.32 MB binary** |
| Hot-path latency | 1.06 ms p99 | 0.53 ms p99 | **0.29 ms p99** |
| Where the users and reviewers are | **75.2% of AI job postings** | infra niche | infra niche |

The calibration pipeline — Clopper-Pearson recall bounds, FSM induction from
traces, AUROC curves, anytime-valid sequential tests — is the defensible part of
this project, and it is `scipy` in Python versus original implementations
anywhere else. That half is not a close call and will not move in any version.

The single largest risk remains not shipping. Python removes weeks and costs
0.44 ms.

### Revised port trigger

Port `guard` to **Rust** when install friction appears as a real, repeated
complaint — two or more users reporting that the Python requirement blocked
them. The 0.32 MB static binary is the reason: `curl | sh`, nothing else
required, and it is 19× smaller than the Go equivalent.

Do not port for latency. The budget has 4.7× headroom and the CI benchmark
guards it.

The hot path is ~150 lines behind a stable JSON contract, so this remains a
weekend of work whenever it becomes justified. Learning Rust to do it is real
cost — but it is cost paid once, against an artefact that is better on both of
the axes that would motivate the port in the first place.

`measure`, the calibration pipeline and all analysis stay Python permanently.

---

## D4 · The three "ceiling" properties are not ceilings

An earlier assessment listed three of the competitor's properties as unbeatable:
zero `unsafe`, a perfect 481/0 fail-open record, and a pre-registration with
strong power analysis. That was wrong on all three. Each is a **floor with a
level above it**, and the level above is reachable.

---

### C1 · "Zero `unsafe`" — the claim is narrower than it sounds

**What is actually true of Velra:** zero `unsafe` blocks *in its own two
crates*. Verified by reading the source.

**What that claim does not cover:**

| | Velra | AMASE |
|---|---:|---:|
| Direct runtime dependencies | **12** | **0** |
| Packages in lockfile | **138** | **0** |
| C compiled into the artefact | **SQLite amalgamation, bundled** | none |
| No-unsafe enforcement | observed count | n/a — no unsafe construct exists |

`crates/velra-core/Cargo.toml` line 20 reads
`rusqlite = { version = "0.40.2", features = ["bundled"] }`. The `bundled`
feature compiles the SQLite amalgamation — a large, mature, and entirely
memory-unsafe C codebase — directly into the binary. Alongside it: `blake3`
(SIMD intrinsics), `regex`, `aho-corasick`, `serde_json`, and eight more.

So the honest statement of the property is: *"we wrote no unsafe Rust, and we
statically link a C database engine plus eleven other crates."* That is a good
property. It is not the same as "this binary contains no memory-unsafe code."

**Two levels above it, both available:**

1. **`#![forbid(unsafe_code)]`.** Velra does not use it — grep finds neither
   `forbid` nor `deny(unsafe_code)` in either crate root. Their zero is an
   observed count that a future commit can silently change; the attribute is a
   guarantee the compiler enforces. This is a one-line difference between
   "we checked" and "it cannot happen."
2. **Zero third-party code at all.** AMASE has **no runtime dependencies** —
   stdlib only, verifiable by anyone in one command. Supply-chain attack surface
   is not "small", it is empty. No transitive CVE can reach it, no dependency
   can be yanked or hijacked, and there is no C in the process.

**The beat:** do not argue about `unsafe` at all. Publish dependency counts.
0 versus 138 is a stronger and more legible claim than 0 versus 0.

---

### C2 · "481 invocations, 0 failures" — a sample, not a guarantee

Zero failures cannot be improved on. But **481 is a sample size**, and the rule
of three makes precise what it proves:

| Observed | 95% upper bound on failure rate | i.e. could fail as often as |
|---|---:|---:|
| 0 / 481 (Velra, in-session) | **0.62%** | 1 in 160 |
| 0 / 4,531 (Velra, incl. load tests) | 0.066% | 1 in 1,510 |
| 0 / 10,000 | 0.030% | 1 in 3,333 |
| 0 / 100,000 | **0.003%** | 1 in 33,333 |

Their headline figure supports "fails less than once in 160 invocations, with
95% confidence." That is a good number presented as a perfect one.

**Two levels above it:**

1. **More samples, which is cheap.** The fault-injection harness is automated;
   100,000 invocations is a longer CI job, not a research programme. It moves
   the provable bound by two orders of magnitude.
2. **Structural fail-open, which is the real win.** Velra's guarantee requires
   *their code to execute and exit 0*. Every one of the 481 invocations is
   evidence that their error handling works — but it is evidence about code
   that ran.

   AMASE's HTTP hook fails open **when the product is absent entirely**. Claude
   Code requires a 2xx response carrying decision fields in order to block a
   tool call. A dead server, a crashed process, a busy port, a firewall, an
   uninstalled binary — all produce no such response, and the tool proceeds.

   That is fail-open by *absence*, not by correct execution. It is a strictly
   stronger property, and it holds in exactly the scenarios where a
   correctness-dependent guarantee is most likely to fail.

**The beat:** state the rule-of-three bound honestly, run 100k, and point out
that the architecture fails open even when the process is not running.

---

### C3 · The pre-registration was well-powered and badly designed

This is the most important of the three, because it is where their project
actually came apart.

Their statistics are genuinely good: one-sided Fisher exact, with n ≥ 4 derived
from the arithmetic that a perfect split reaches p = 0.014 at n = 4. Better
reasoning than most funded teams, as stated.

**And three of four hypotheses still failed to produce a verdict.** H1 and H2
returned INCONCLUSIVE because their control found that compaction *was not
lossy on their fixture* — the phenomenon the intervention was designed to
prevent did not occur in the baseline. Their own threats section names it:
*"A defect requiring several coordinated edits would leave more room for the
arms to diverge."*

**Statistical power does not help when the effect being measured is absent from
the fixture.** That is a design failure, not an analysis failure, and no amount
of replicates fixes it.

**Four levels above it:**

1. **Positive control as a gate, run first.** Before spending money measuring an
   intervention, demonstrate the phenomenon exists in the fixture. Velra ran
   their control concurrently and it retroactively invalidated two hypotheses.
   Running it as a **blocking precondition** means you never spend a rupee on an
   experiment that cannot separate.
2. **Pre-specify a SESOI and use equivalence tests.** This is the big one. With
   a smallest-effect-size-of-interest declared in advance and a TOST equivalence
   test, a null result becomes a *conclusive* finding — "no effect larger than
   X" — instead of INCONCLUSIVE. **Their H1 and H2 would have been real results
   rather than shrugs.** Two verdicts recovered from a purely methodological
   change.
3. **Anytime-valid sequential testing.** Their fixed-n design cannot stop early
   when evidence is clear, nor extend when it is marginal, without invalidating
   the p-value. Anytime-valid tests permit data-dependent stopping at matched
   power — cheaper and more informative, for a student paying per rollout.
4. **External immutable timestamp.** Their pre-registration is a file in their
   own repository. Git history makes tampering visible, which is decent, but an
   OSF registration is timestamped by a third party and is the recognised
   standard. Also: with four hypotheses tested, report multiplicity control.

**The beat:** the target is not "more replicates." It is **3 of 4 hypotheses
resolved instead of 1**, achieved by a positive-control gate and pre-specified
equivalence bounds — both free, both design-level.

---

### The revised scoreboard rows

| Property | Velra | AMASE target |
|---|---|---|
| Memory/supply-chain safety | 0 `unsafe` own code; 12 deps, 138 packages, bundled C | **0 dependencies, 0 third-party code** |
| No-unsafe enforcement | observed count | n/a by construction |
| Fail-open evidence | 0/481 → <0.62% at 95% | **0/100,000 → <0.003%** |
| Fail-open mechanism | requires correct execution | **holds when the process is absent** |
| Hypotheses resolved | **1 of 4** | **3 of 4** via positive-control gate + SESOI |
| Null results | INCONCLUSIVE | **conclusive** via equivalence testing |
| Stopping rule | fixed n | anytime-valid |
| Pre-registration | in-repo file | external timestamped registry |

None of these require a breakthrough. They require noticing that "zero" and
"perfect" are descriptions of a measurement, not of a limit.

---

## D5 · Oracle AI Agent Memory is a measurement subject, not a dependency

**Decision.** AMASE does not depend on Oracle AI Agent Memory, or on any
memory substrate. It defines a `MemoryBackend` protocol with a stdlib-only
default, ships `OracleBackend` behind an optional extra that nothing in the
default install path imports, and treats Oracle as **arm A9** in the ablation
rig — a system under test, not a component.

### What Oracle actually is

A database-native memory substrate inside Oracle AI Database 23ai and later —
the version where the `VECTOR` type exists. Six-stage lifecycle: ingestion,
extraction, consolidation, retrieval, summarization, revision/removal. That
last stage is unusual and worth naming: most memory systems only accumulate.

Published results, read from the arXiv paper and the product docs rather than
a summary:

| Benchmark | Score |
|---|---|
| LongMemEval (v26.6) | 94.4% — 472/500 |
| LongMemEval (arXiv) | 93.8% |
| BEAM 100K | 68.5% |
| BEAM 1M | 65.6% |
| Token efficiency | 10.7× fewer than flat-history baselines |

It is **not a pip-installable package**. `oracle-ai-agent-memory` does not
exist on PyPI; I checked. It is reached through `langchain-oracledb` (7 deps),
`langgraph-oracledb` (3), `langchain-oci` (17) and `oracledb` (19), against a
running 23ai+ database. The free 23ai Docker container does run the documented
examples end to end, so the barrier is a container rather than a licence — but
a container is still a container.

### The comparison that decided it

The three systems answer different questions, and only one of them has
evidence that its answer helps.

| | Velra capsule | Oracle context card | AMASE |
|---|---|---|---|
| Question | what survives compaction? | what should the agent remember? | **when should this run stop?** |
| Construction | pure function of a SQLite snapshot | LLM extraction over history | deterministic gates over the trace |
| Determinism | byte-identical | non-deterministic by construction | byte-identical |
| Size | bounded, 745-token target | unbounded, retrieval-scored | n/a |
| Infrastructure | none | Oracle DB 23ai+ | **none** |
| **Efficacy evidence** | **H1, H2 INCONCLUSIVE** | **94.4%, 10.7×** | to be established |

That last row is the finding. Velra built the more elegant artefact and could
not demonstrate it helps, because their control found the baseline never
forgot anything. Oracle built the heavier, non-deterministic system and
published a measured result on a public benchmark.

**Rigour of construction is not evidence of benefit.** D4 argued AMASE should
beat Velra on construction. D5 adds the other half: construction alone is what
left Velra with one verdict out of four.

### Why not depend on it

D4 established the strongest structural advantage AMASE has: **0 runtime
dependencies against Velra's 12 direct and 138 locked**, including a bundled
SQLite C amalgamation. That claim is verifiable in one command and needs no
argument.

Requiring Oracle means `oracledb` (19) plus `langchain-oracledb` (7) plus a
database container. AMASE would go from 0 dependencies to more than the
competitor it is trying to beat on exactly this axis. That trade is not close.

Three further reasons, each sufficient on its own:

1. **Wrong user.** AMASE's user is one developer running Claude Code on a
   laptop. They will not provision an Oracle container to find out how many
   tokens they wasted.
2. **Wrong problem.** A doomed run is doomed regardless of what the agent
   remembers from last week. Cross-session memory does not touch the
   per-step reliability decay that AMASE exists to interrupt.
3. **LLM in the hot path.** Extraction is model-driven, so it is
   non-deterministic *and it consumes tokens* — the exact quantity AMASE
   exists to conserve. A token-saving tool that spends tokens to decide what
   to save is arguing with itself.

### What is implemented instead

`src/amase/memory.py`, stdlib only:

- `MemoryBackend` — a `typing.Protocol` with `append`, `load`, `search`,
  `runs`, `close`. Structural, so a backend needs no import of AMASE to
  satisfy it.
- `LocalBackend` — JSONL append log plus a `sqlite3` index, both stdlib. This
  is what `pip install amase` gives you and it stays that way permanently.
- `OracleBackend` — importable only via `pip install amase[oracle]`. The
  import of `oracledb` happens inside `__init__`, never at module scope, so
  the default install never touches it and `python -c "import amase"` pulls in
  nothing third-party.

The README line *"pluggable backends; Oracle AI Database supported for
enterprise deployments"* is now true, and `pip install amase` still installs
zero third-party packages. Enterprises get the checkbox; nobody else pays for
it.

### The sharper play — Oracle as arm A9

Oracle published on LongMemEval and BEAM. Both are **conversational** memory
benchmarks. Nobody has measured Oracle Agent Memory on long-horizon
**coding-agent** traces.

AMASE is the instrument that can. The horizon sweep already fits per-step
reliability `p` and produces cost-attributed decay curves; pointing it at a
memory substrate is the same experiment with one more arm.

> Deterministic bounded memory (Velra-style capsule) versus LLM-extracted
> memory (Oracle-style context card) versus no memory, on the same
> coding-agent task set, with per-step reliability, token cost and rupees
> attached.

Three consequences:

1. **It makes Velra and Oracle data points rather than rivals.** Same move as
   D4 recommended for Velra, applied to a vendor with a developer-relations
   team that notices measurement work.
2. **Oracle's 10.7× is testable.** If it replicates on coding traces, that is
   an independent confirmation of a vendor claim — publishable. If it does
   not, that is the more interesting result, and the methodology is already
   built to defend it.
3. **The rig gets a fourth arm for nothing.** A9 slots into the existing
   A1–A8 design with no new infrastructure beyond a Docker container that runs
   only during the sweep.

Note the constraint this puts on the measurement: an arm whose memory is
produced by an LLM costs tokens to run, and those tokens must be counted
*against* it in the cost column, not treated as overhead. Measuring a
token-spending memory system with a token-accounting instrument is the whole
point, and it only works if the accounting is honest about which side the
extraction cost falls on.

### What would overturn this

- **Oracle ships a standalone, database-free Python package with few
  dependencies.** Then `LocalBackend` could delegate to it and the calculus
  changes. Currently there is no such package.
- **A9 shows LLM-extracted memory materially lowers failure rate on coding
  traces** — large enough that a run-stopping tool should be offering memory
  repair instead of only abort. That would be a finding worth changing the
  product for, and it is precisely the finding A9 is designed to be able to
  produce.
- **Enterprise users actually arrive and ask for it.** Then `OracleBackend`
  graduates from stub to maintained. Not before; a maintained integration
  nobody uses is a liability.

### Guardrail

`tests/test_memory.py` asserts that importing `amase` and `amase.memory`
brings in no module outside the standard library. If someone adds a top-level
third-party import to the default path, the suite fails. The zero-dependency
claim is enforced by a test, not by discipline.
