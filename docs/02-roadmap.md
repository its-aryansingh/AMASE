# AMASE — 14-Week Build Roadmap

Final-year capstone pacing, alongside coursework and placement season.
Start: late September 2026 · Target ship: early January 2027

---

## Shape of the plan

Five phases. Each has a **hard exit criterion** — a thing that either works or doesn't. Do not start the next phase until the current one's criterion is met, even if that means cutting later phases.

**The cut-line is after Phase 1.** A working instrumented harness with a CI gate and one honest baseline number is already a stronger portfolio than most capstones. Everything after Phase 1 increases the ceiling, not the floor.

---

## Phase 0 — Skeleton and baseline · Weeks 1–2

Prove the loop closes end to end before building anything clever.

- Repo scaffold, `uv`/Poetry, pre-commit, typed config.
- Docker sandbox: spawn per task, mount workspace, capture stdout/stderr, enforce turn/time/USD caps, tear down.
- Minimal ReAct agent on LangGraph. Seven tools: `bash`, `read_file`, `write_file`, `edit_file`, `grep`, `glob`, `run_tests`. Zero middleware.
- Terminal-Bench adapter. `uv tool install terminal-bench`, wire AMASE as the agent.
- Run 5 tasks by hand. Watch the traces. Learn where it fails.

**Exit:** baseline harness resolves at least one Terminal-Bench task end to end, unattended, with a clean teardown.

> If the adapter fights you, timebox it to 10 days and fall back to running your own tasks in the same format. Do not let integration plumbing eat Phase 1.

---

## Phase 1 — The measurement spine · Weeks 3–5

**This is the phase that makes the project credible.** Everything else is optional relative to this.

- OpenTelemetry GenAI semantic-convention instrumentation on every model call and tool call.
- Self-hosted Langfuse (docker-compose). Traces flowing, searchable.
- Cost accounting: per-model token pricing table, USD attributed per task and per run.
- Metrics schema frozen: resolve, tokens in/out, USD, wall-clock p50/p95, turns, tool-error rate, doom-loop incidence.
- Experiment runner: config matrix × n repetitions, results to `results/*.json`, mean and spread computed.
- **DeepEval CI gate.** GitHub Actions, 8–10 cheap tasks on every PR, fails on regression beyond tolerance, posts a delta table as a PR comment.
- Baseline established: 20 tasks × n=3, published in `results/baseline.json` and in the README.

**Exit:** a PR that degrades the agent gets automatically blocked by CI, with a diff table in the comment. Screenshot that. It is the single best artifact in the project.

---

## Phase 2 — Cheap, high-yield ablations · Weeks 6–9

The mechanisms with the best effort-to-signal ratio. Expect most of the total gain to land here.

- **A1** environment context injection — directory map, tool discovery, explicit turn/time budget at startup.
- **A2** pre-completion checklist — block completion until the agent verifies against spec and runs tests. *Hypothesis: largest single effect.*
- **A3** doom-loop detection — per-file edit counters, forced re-plan on threshold.
- **A8** reasoning budget — flat vs "sandwich" (high plan / mid build / high verify).
- **A4** compaction — `truncate` vs `summarize` vs `notes` file.
- Pre-register each hypothesis in `EXPERIMENTS.md` *before* running.
- Run each arm at n=3 against baseline. Then a small factorial over the top 3 to check interactions.

**Exit:** a five-row ablation table with effect sizes, token deltas and rupee deltas, committed to `results/` and rendered in the README.

> **Write the first blog post here**, not at the end. "I measured what makes coding agents work — part 1" with a real table lands harder in October than a finished repo lands in January.

---

## Phase 3 — The expensive arms · Weeks 10–12

The two mechanisms the original report treated as foundational. Here they are hypotheses.

- **A6 retrieval** — implement hybrid: BM25 sparse index + dense embeddings + cross-encoder rerank over AST-aware chunks. Compare against agentic grep/glob on identical tasks. Track **time-to-first-relevant-file** and **tokens-per-task**, not just resolve rate.
- **A7 routing** — static rules first (token count, task-class keywords, turn phase: planning vs mechanical edit). Frontier model for plan and debug, local 8B via Ollama or vLLM for mechanical edits. Plot the full cost/quality curve; identify the knee. Only build a learned classifier if static rules visibly plateau.
- If you run vLLM, record throughput and KV-cache behaviour. That gives you first-hand answers on memory-bound inference and PagedAttention.

**Exit:** a cost/quality curve with the knee marked, and a documented finding on retrieval — *including if the finding is that grep wins*. Especially then.

---

## Phase 4 — Held-out set, write-up, ship · Weeks 13–14

- **AMASE-Held-Out**: 25–40 self-authored tasks in Terminal-Bench format across three difficulty tiers, sourced from your own repos and recent small OSS projects with no plausible training-data presence. Discard any task the baseline solves 3/3 or 0/3.
- Re-run the top configurations on the held-out set. Compare public vs held-out deltas — if a mechanism helps on public tasks but not held-out ones, that is a contamination signal and a genuinely publishable observation.
- Static dashboard over `results/`: ablation table, cost/quality curves, trace links.
- README rewrite: thesis, headline table, reproduction instructions, honest limitations section.
- Long-form write-up. Target an arXiv preprint or a substantial technical blog post. The report's own claim that a publication adds ₹3–5L to an offer is the one monetary claim in it worth acting on.
- Two-minute demo video: agent running, trace opening in Langfuse, CI gate blocking a bad PR.

**Exit:** a stranger can clone the repo, run `make reproduce`, and get your numbers.

---

## Weekly cadence

| Week | Focus | Artifact |
|---|---|---|
| 1 | Scaffold + Docker sandbox | sandbox spawns, executes, tears down |
| 2 | ReAct core + TB adapter | one task resolved end to end |
| 3 | OTel + Langfuse | traces visible and searchable |
| 4 | Cost accounting + runner | `results/*.json` with n reps |
| 5 | **CI gate** | PR blocked on regression — screenshot it |
| 6 | A1 + A2 | two arms, n=3 |
| 7 | A3 + A8 | two arms, n=3 |
| 8 | A4 | three compaction modes |
| 9 | Factorial + **blog post 1** | five-row ablation table |
| 10 | A6 hybrid retrieval build | index + rerank working |
| 11 | A6 comparison run | retrieval finding |
| 12 | A7 routing | cost/quality curve with knee |
| 13 | Held-out task authoring | 25–40 tasks, tiered |
| 14 | Dashboard + write-up + video | shipped |

---

## Budget

Treat API spend as a first-class constraint; it is the most common reason capstones like this stall.

- Full ablation matrix runs on a **cheap or local model**. Frontier models only for the top-3 configs on the held-out set.
- Hard per-rollout caps: max turns, max wall-clock, max USD. Non-negotiable.
- Track cumulative spend in the same telemetry that tracks everything else — then your cost discipline is itself demonstrable.
- Apply for student and startup credits in **week 1**, not week 10. Approval takes time.
- Rough working target: keep total spend under ₹8–10k. If a phase threatens that, cut task count before cutting rigour — 12 tasks at n=3 beats 40 tasks at n=1.

---

## Placement-season overlay

You are building this while interviewing. Sequence the artifacts so they are usable early.

- **By end of week 5**, you have a story: instrumented agent harness, CI-gated evals, baseline numbers. That is already interview-ready. Put it on the resume then, not in January.
- **By end of week 9**, you have the ablation table and a blog post. That is the thing you send in a cold email to an AI startup.
- **By week 14**, you have the write-up and dashboard. That is the thing that gets you past a senior screen.

Resume line, from week 5 onward — concrete, no adjectives:

> **AMASE** — instrumented coding-agent harness and ablation rig. Measured the marginal contribution of 8 harness mechanisms on resolve rate, token cost and p95 latency across Terminal-Bench and a self-authored contamination-free task set. OTel GenAI tracing → Langfuse; DeepEval CI gate blocking regressions on every PR. *[repo] [write-up]*

---

## What to cut, in order, if time runs short

1. Learned router (A7 learned) — static rules capture most of the value
2. The factorial interaction study — single-arm results still stand
3. A5 sub-agent delegation — interesting but expensive to measure
4. A6 hybrid retrieval — the most expensive arm; the agentic-search default is defensible on its own
5. The dashboard — a markdown table in the README is enough

**Never cut:** telemetry, the CI gate, n≥3, or the held-out set. Those four are the entire reason the project is differentiated.
