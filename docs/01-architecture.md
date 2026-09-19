# AMASE — Architecture Specification

**Autonomous Multi-Agent Software Engineering**
v0.1 · 17 September 2026

---

## 1. Thesis

> Everyone is building coding agents. Almost nobody is measuring **which parts of the agent actually matter**.
>
> AMASE is a minimal, fully-instrumented coding-agent harness plus an ablation rig that measures the marginal contribution — in resolve rate, tokens, rupees and wall-clock — of each harness mechanism, on contamination-resistant tasks, with variance reported.

The deliverable is not a product. **The deliverable is a table**: eight harness mechanisms, each with a measured effect size and a cost. The harness exists to produce that table.

This is defensible for exactly the reason the crowded alternative is not. "I built a multi-agent coding workspace" invites *"how is it different from OpenHands?"*. "I measured what makes coding agents work and here is the data" invites *"walk me through your methodology"* — which is the conversation you want.

### Why this framing is well-founded

Two independent 2026 sources put harness/scaffold choice at **+11 to +15 points** on agent benchmarks with the model held fixed — larger than a model generation. LangChain demonstrated +13.7 on Terminal-Bench 2.0 by changing only the harness. That is a large, under-measured effect in a domain students can actually run experiments in.

---

## 2. Non-goals

Explicitly out of scope. Say so in the README; scope discipline is itself a signal.

- Not a product or a Cursor/OpenHands competitor. No IDE plugin, no web workspace, no chat UI beyond a results dashboard.
- Not chasing SWE-bench Verified SOTA. That benchmark is saturated and contaminated.
- Not a generative UI, Slack workspace, or multi-platform bot.
- Not training or fine-tuning a model. The model is a controlled constant.

---

## 3. System design

```
                         ┌─────────────────────────────┐
                         │  EXPERIMENT RUNNER          │
                         │  config matrix · n≥3 reps   │
                         │  seeds · variance capture   │
                         └──────────────┬──────────────┘
                                        │
         ┌──────────────────────────────▼──────────────────────────────┐
         │  AMASE CORE HARNESS  (deliberately minimal ReAct loop)      │
         │                                                             │
         │   ┌────────────────── MIDDLEWARE STACK ──────────────────┐  │
         │   │  every layer is independently switchable = one arm   │  │
         │   │                                                      │  │
         │   │  A1 env-context injection     A5 sub-agent delegate  │  │
         │   │  A2 pre-completion checklist  A6 retrieval mode      │  │
         │   │  A3 doom-loop detector        A7 model router        │  │
         │   │  A4 compaction strategy       A8 reasoning budget    │  │
         │   └──────────────────────────────────────────────────────┘  │
         │                                                             │
         │   tools: bash · read · write · edit · grep · glob · test    │
         └───────┬──────────────────────────────────────┬──────────────┘
                 │                                      │
     ┌───────────▼───────────┐            ┌─────────────▼─────────────┐
     │  EXECUTION SANDBOX    │            │  TELEMETRY (OTel GenAI)   │
     │  Docker, per-task     │            │  → Langfuse (self-hosted) │
     │  ephemeral, no net    │            │  tokens · USD · latency   │
     │  stdout/stderr capture│            │  turns · tool errors      │
     └───────────┬───────────┘            └─────────────┬─────────────┘
                 │                                      │
     ┌───────────▼──────────────────────────────────────▼─────────────┐
     │  EVALUATION                                                    │
     │  Terminal-Bench 2.x adapter  ·  AMASE-Held-Out (self-authored) │
     │  end-state verification (pytest)  ·  DeepEval CI gate          │
     └───────────┬────────────────────────────────────────────────────┘
                 │
     ┌───────────▼───────────┐
     │  RESULTS              │
     │  ablation table       │
     │  cost/quality curves  │
     │  static dashboard     │
     └───────────────────────┘
```

### 3.1 Core harness — keep it boring

A single ReAct-style agent: `bash`, `read_file`, `write_file`, `edit_file`, `grep`, `glob`, `run_tests`. No multi-agent structure by default — **multi-agent is ablation A5, not an assumption.** That is the honest design: the report asserted multi-agent is better; AMASE tests it.

Built on **LangGraph** for checkpointing, interruption and state persistence. LangGraph earns its place here because ablations need deterministic replay from a checkpoint, not because multi-agent frameworks are fashionable.

Every middleware is a wrapper with a uniform interface, toggled by config:

```python
@dataclass(frozen=True)
class HarnessConfig:
    env_context:      bool  = False   # A1
    pre_check:        bool  = False   # A2
    loop_detect:      bool  = False   # A3
    compaction:       Literal["truncate","summarize","notes"] = "truncate"  # A4
    subagent_review:  bool  = False   # A5
    retrieval:        Literal["agentic","hybrid","both"] = "agentic"        # A6
    routing:          Literal["off","static","learned"]  = "off"            # A7
    reasoning_budget: Literal["flat","sandwich"]         = "flat"           # A8
```

Baseline = all defaults off. Every result is reported as a delta from that baseline.

### 3.2 The eight ablation arms

| # | Mechanism | What it does | Hypothesis | Cost to build |
|---|---|---|---|---|
| **A1** | Environment context injection | Map directory tree + discover installed tooling + state the time/turn budget, injected at startup | +points, small token cost | Low |
| **A2** | Pre-completion checklist | Middleware blocks "done" until the agent verifies output against the spec and runs tests | Largest single gain | Low |
| **A3** | Doom-loop detection | Track per-file edit counts; on threshold, inject a re-plan prompt | Cuts catastrophic runs, improves p95 | Low |
| **A4** | Compaction strategy | `truncate` vs `summarize` vs `notes` (external `NOTES.md`) | `notes` wins on long-horizon tasks | Medium |
| **A5** | Sub-agent delegation | Reviewer runs in a clean context, returns a 1–2k token summary | Wins on tasks >N turns, loses on short ones | Medium |
| **A6** | Retrieval mode | Agentic grep/glob vs BM25 + dense + cross-encoder rerank vs both | Agentic wins on exact-symbol; hybrid wins on concept-level and huge repos | **High** |
| **A7** | Model router | Static rules → frontier for plan/debug, local SLM for mechanical edits | 30–60% cost cut at small quality delta | **High** |
| **A8** | Reasoning budget | Flat effort vs "sandwich" (high-plan / mid-build / high-verify) | Better points-per-rupee than flat max | Low |

A1–A3 and A8 are cheap and high-yield — build them first. A6 and A7 are the expensive, interesting ones and are where the RAG and router modules from the original report survive, reframed from *assumptions* into *measured hypotheses*.

### 3.3 Sandbox

**Plain Docker, per-task, ephemeral, network-disabled by default.** Not E2B, not Daytona, for the capstone:

- E2B (Firecracker microVMs, dedicated kernel) is the right answer for untrusted multi-tenant code in production — and that is the *correct interview answer* — but it is a paid managed service and you are running hundreds of rollouts.
- Daytona went closed-source in June 2026.
- Terminal-Bench tasks are Docker-native anyway, so Docker is the path of least resistance.

Ship an `E2BSandbox` class implementing the same interface, wired but off by default. You then get to say: *"Docker for the experiments because of rollout economics; E2B behind the same interface for the multi-tenant case, because container escape via shared kernel is the threat model that matters when the code is model-generated."* That answer is worth more than actually paying for E2B.

Hard guardrails on every rollout: max turns, max wall-clock, max USD. A runaway agent is a budget event.

### 3.4 Telemetry

Instrument to the **OpenTelemetry GenAI semantic conventions** — the 2026 standard — and export to a **self-hosted Langfuse** (MIT core). Vendor-neutral instrumentation is a stronger claim than naming a SaaS.

Captured per rollout, non-negotiable:

| Metric | Why |
|---|---|
| resolve / pass (bool) | the outcome |
| input + output tokens, per model | cost attribution |
| USD cost, per model, per task | the money argument |
| wall-clock, p50 / p95 | latency honesty |
| turns to completion | efficiency |
| tool-call error rate | harness quality signal |
| doom-loop incidence | A3's effect |
| tokens-per-task and time-to-first-relevant-file | A6's effect |

Every number in the final table traces to a stored trace. That traceability *is* the observability story.

### 3.5 Evaluation

**Two task sets, on purpose.**

1. **Terminal-Bench 2.x** — public, comparable, adapter-based (`uv tool install terminal-bench`, write an adapter, plug in AMASE). Gives an externally-legible number.
2. **AMASE-Held-Out** — 25–40 tasks you author yourself in the Terminal-Bench format (`task.yaml`, `Dockerfile`, `solution.sh`, `tests/test_outputs.py`), from repos with no plausible presence in training data: your own projects, recent small Indian OSS repos, freshly-written codebases.

The second set is the scientific point. **Contamination-free by construction**, because you built it. Stating *"public benchmarks are contaminated, so I authored a held-out set and report both"* is a genuinely senior-level move and it directly pre-empts the strongest objection to any benchmark result.

Scoring is **end-state verification** — after the agent stops, does the machine satisfy the tests? Not transcript grading, not LLM-as-judge on the diff. LLM-as-judge is reserved for one narrow, honest use: grading *explanation quality* of the agent's summary, where no deterministic check exists, with human-agreement calibration reported.

### 3.6 CI gate

**DeepEval** (pytest-style assertions) in GitHub Actions. On every PR touching prompts, middleware or config:

- run a fast subset (8–10 cheap tasks) against a cheap model
- fail the PR if resolve rate drops below `baseline − tolerance`
- post the delta table as a PR comment

Nightly: the full matrix on the paid model, results committed to `results/`.

This is the piece the original report correctly identified as ~70% of real production AI work and almost entirely absent from student portfolios. Build it early — **Phase 1, not last** — because it protects every subsequent experiment.

---

## 4. Experimental protocol

The methodology is the differentiator. Treat it as seriously as the code.

1. **n ≥ 3 runs per configuration.** Single-run agent benchmark numbers are noise. Report mean and spread; reporting variance at all puts you ahead of most published agent results.
2. **One variable at a time**, then a small factorial over the top-3 mechanisms to check for interaction effects (does A2 still help once A5 is on?).
3. **Model held fixed** within an experiment. Run the full matrix on one cheap model, and the top few configs on one frontier model.
4. **Fixed seeds and temperature** where the provider allows; record what is not controllable and say so.
5. **Report cost, always.** Every point of resolve rate has a rupee price. A mechanism that adds 2 points for 3× the tokens is a *negative* result and should be published as one.
6. **Publish negative results.** If hybrid retrieval loses to grep on your task set, that is the most interesting finding in the project. Do not bury it.
7. **Pre-register hypotheses** in `EXPERIMENTS.md` before running. Cheap to do, and it makes the work legible as research.

---

## 5. Repository layout

```
amase/
├── README.md                    # thesis, headline table, how to reproduce
├── EXPERIMENTS.md               # pre-registered hypotheses + results log
├── amase/
│   ├── harness/
│   │   ├── graph.py             # LangGraph ReAct core
│   │   ├── tools.py             # bash, read, write, edit, grep, glob, test
│   │   └── middleware/          # A1–A8, one module each
│   ├── sandbox/
│   │   ├── base.py              # Sandbox interface
│   │   ├── docker.py            # default
│   │   └── e2b.py               # wired, off by default
│   ├── routing/
│   │   ├── rules.py             # static — build first
│   │   └── learned.py           # classifier — only if rules plateau
│   ├── retrieval/
│   │   ├── agentic.py           # grep/glob/read
│   │   └── hybrid.py            # BM25 + dense + cross-encoder rerank
│   └── telemetry/otel.py        # GenAI semconv → Langfuse
├── eval/
│   ├── adapters/terminal_bench.py
│   ├── tasks/held_out/          # 25–40 self-authored tasks
│   ├── runner.py                # config matrix, n reps, variance
│   └── ci/                      # DeepEval suite for the PR gate
├── results/                     # committed JSON + generated tables
├── dashboard/                   # static site over results/
└── docs/                        # this spec, research brief, roadmap
```

---

## 6. Technology decisions, with the rejected alternative

Stating what you rejected and why is how you demonstrate judgement in an interview.

| Decision | Chosen | Rejected | Reason |
|---|---|---|---|
| Orchestration | LangGraph | CrewAI, MS Agent Framework, OpenAI Agents SDK | Need checkpointing + deterministic replay for ablations; CrewAI is prototyping-shaped |
| Sandbox | Docker (E2B interface ready) | E2B managed, Daytona | Rollout economics; Daytona closed-source June 2026 |
| Tracing | OTel GenAI semconv → Langfuse | Vendor SDK lock-in | Standard-conformant, self-hostable, MIT core |
| CI evals | DeepEval | Ragas, promptfoo | Ragas is RAG-metric-shaped; DeepEval is pytest-native so the gate is just a test |
| Live evals | Langfuse | LangSmith, Braintrust | Same platform as tracing; self-hosted; no cost floor |
| Routing | Static rules first | Learned router first | Static captures 60–70% of savings at zero training cost; learned adds 15–25% on top |
| Retrieval default | Agentic search | Vector-DB-first | Staleness + exact-symbol reliability; hybrid is an ablation arm, not the default |
| Benchmark | Terminal-Bench + self-authored held-out | SWE-bench Verified | Verified is saturated (~97%) and contaminated; OpenAI stopped reporting it |

---

## 7. How each interview question gets an evidence-backed answer

| Question you will be asked | What AMASE lets you say |
|---|---|
| "How do you reduce token cost at scale?" | "Measured it. Static routing of mechanical edits to a local 8B model cut cost X% for a Y-point resolve-rate delta. Here's the cost/quality curve and where the knee is." |
| "How do you handle context window limits?" | "A/B'd three compaction strategies over n=3. Structured note-taking beat summarisation by N points at M% fewer tokens on tasks over T turns." |
| "Is multi-agent better than single-agent?" | "Conditionally. Sub-agent review won on long-horizon tasks and lost on short ones — the crossover was around K turns. Here's the data." |
| "How do you stop infinite planning loops?" | "Edit-count doom-loop detector with a forced re-plan. Incidence went from A% to B%, and p95 wall-clock dropped by C%." |
| "How do you evaluate?" | "CI gate on every PR, nightly full matrix, n≥3 with variance, end-state verification, and a self-authored held-out set because the public benchmarks are contaminated." |
| "RAG or fine-tune?" | "Neither by default for code. I measured agentic search against hybrid retrieval — here's what won and on which task classes. RAG is for dynamic knowledge; fine-tuning is for behaviour and output format, and risks catastrophic forgetting." |
| "Why is LLM inference memory-bound?" | KV cache growth in long-context generation; PagedAttention virtualises the blocks. If you run a local model under vLLM for A7, you will have measured throughput yourself. |

---

## 8. Known risks

| Risk | Mitigation |
|---|---|
| **API spend** — hundreds of rollouts on a frontier model is real money | Full matrix on a cheap/local model; frontier runs only on top-3 configs and only on the held-out set. Hard per-rollout USD cap. Track spend in the same telemetry. Apply for student/startup credits early. |
| **Scope creep back toward "a product"** | Non-goals section is load-bearing. Every new feature must answer: "which ablation arm is this?" |
| **Ablations are noisy, effects don't separate** | n≥3 minimum, raise to 5 on the final table. Report honest confidence. A null result, well-measured, is still a result. |
| **Terminal-Bench adapter fights you** | Phase 0 exit criterion is a working adapter on 5 tasks. If it resists after two weeks, fall back to running only the self-authored set and say so. |
| **Held-out tasks are too easy or too hard** | Author tasks in three difficulty tiers; discard any task the baseline solves 3/3 or 0/3 — they carry no signal. |
| **Time, against placements and coursework** | Phase 1 is the hard floor. A working harness + telemetry + CI gate + baseline number is already a better portfolio than 95% of capstones, even with zero ablations shipped. |
