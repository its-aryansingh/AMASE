# HALFLIFE — Product Specification

**Reliability budgeting for long-horizon agents.**
v0.1 · 17 September 2026

*(Name is a proposal — it captures decay, it is short, memorable and unclaimed in this space. Alternatives: Attrition, Horizon, Decayline. Check the npm/PyPI/GitHub namespace before committing.)*

---

## 1. Positioning

> **Everyone reports whether the agent passed. Nobody reports how fast it dies.**
>
> Halflife measures your agent's per-step reliability, projects the horizon at which it collapses, and catches divergent runs at step two instead of step fifty.

One-line pitch for a README:

> *Your agent scores 90% on your evals and fails in production. That is not a bug in your evals — it is arithmetic. Halflife measures the decay constant they are hiding.*

### The wedge, stated precisely

Agent success is not a single probability. It is `P(success) ≈ p^n`, where `p` is per-step reliability and `n` is the horizon. Published measurement across 10,664 trajectories confirms this geometric form and shows `p` saturates below 1 for every model — so **every agent has a horizon beyond which it reliably fails**, and nobody currently knows where theirs is.

Existing platforms report the left-hand side on one value of `n`. Halflife estimates `p` and hands you the curve.

---

## 2. The three primitives

Each is grounded in a specific published result, and each is independently useful — which matters, because it means the tool is adoptable in pieces.

### P1 · Reliability decay measurement

Run a task at several horizon lengths with repetition, fit the decay curve, report:

- **`p` (per-step reliability)** with a confidence interval
- **`n₅₀`** — the horizon at which expected success falls below 50%
- **`n_target`** — the horizon at which it falls below the user's stated SLO
- **Reliability Decay Curve** — success vs step count, with the cliff marked

The headline artefact is a single sentence a platform team can act on:

> *"At p = 0.963 (95% CI 0.951–0.974), your agent drops below 80% success at 6 steps and below 50% at 19. Your median production task is 24 steps."*

No existing product outputs that sentence.

### P2 · Early divergence detection (the cheap win)

Run the same task `k` times (k = 3–5), compute **Tool Sequence Similarity** over the first `m` steps, and use it as a live failure predictor.

Grounded in: high-TSS runs achieve **90.2%** correctness vs **61.2%** for low-TSS; **60% of behavioural divergence originates in the first two steps**; argument-level variance is *not* predictive (r=0.12, n.s.) — so measure sequence, not arguments.

Two modes:

- **Offline** — a divergence score per task in CI, flagging tasks whose specification is ambiguous. (Ambiguous specs cut consistency ~28%; task clarity dominates model choice.)
- **Online** — a runtime guard that forks `k` cheap probes at step 1–2, compares tool sequences, and aborts or re-plans when they disagree. At 100× cost multipliers for long runs, killing a doomed run at step 2 is the single highest-ROI control available.

This is the feature that makes the tool pay for itself, and it is the one nobody has shipped.

### P3 · Deterministic replay and failure capture

Record the full envelope at the orchestration boundary — assembled prompt, parameters, sampled completion, tool inputs and outputs, retrieved chunks, model version, timestamps — into an append-only, hash-chained trace.

Design rules, from the record/replay literature:

- **Replayability, not determinism.** Bitwise determinism is unattainable (float non-associativity, batch invariance, MoE routing contention) and *undesirable* — self-consistency sampling improves accuracy 6–17%. Keep variation at generation time; require determinism only at replay time.
- **Record at the graph boundary**, not the socket — survives streaming and concurrency.
- **Canonicalise before hashing** — strip run ids, timestamps, trace ids, or every fixture key misses.
- **Deterministic stubs** must never call a live model, hit an API, read a database, or touch the system clock.
- **Same agent code in both modes** via dependency injection.

Then: one command turns any recorded failure into a frozen regression test.

---

## 3. What this is not

Stating the non-goals is what keeps the project shippable and is itself a credibility signal.

- **Not another observability platform.** Halflife emits OpenTelemetry GenAI spans and exports to whatever the user already runs — Langfuse, Phoenix, LangSmith, Datadog. Competing with Braintrust on trace UI is unwinnable and unnecessary.
- **Not an agent framework.** It wraps LangGraph, the OpenAI Agents SDK, Pydantic AI, or a bare loop.
- **Not a model evaluator.** It measures *your system*, not the frontier.
- **Not a guardrails/security product.** That is the second project, not this one.

The strategic point: Halflife is **complementary to the incumbents**, which means adoption does not require displacing anyone. It is a measurement layer that makes their traces more useful.

---

## 4. Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  halflife  CLI / SDK                                             │
│  $ halflife measure --task fix_auth --horizons 5,10,20,40 -k 5   │
│  $ halflife replay run_8f2a --assert tool-sequence               │
│  $ halflife guard --probe-steps 2 --abort-on-divergence          │
└───────────────────────────┬──────────────────────────────────────┘
                            │
      ┌─────────────────────▼─────────────────────┐
      │  ADAPTER LAYER                            │
      │  LangGraph · OpenAI Agents SDK ·          │
      │  Pydantic AI · raw callable               │
      │  (one interface: run(task, cfg) -> trace) │
      └─────────────────────┬─────────────────────┘
                            │
   ┌────────────────────────┼────────────────────────┐
   │                        │                        │
┌──▼───────────┐  ┌─────────▼──────────┐  ┌──────────▼─────────┐
│ RECORDER     │  │ HORIZON RUNNER     │  │ RUNTIME GUARD      │
│ envelope     │  │ sweeps n, repeats  │  │ k probes at step   │
│ capture,     │  │ k times, seeds,    │  │ 1–2, TSS compare,  │
│ hash-chained │  │ hard cost/turn cap │  │ abort / re-plan    │
│ tape         │  │                    │  │                    │
└──┬───────────┘  └─────────┬──────────┘  └──────────┬─────────┘
   │                        │                        │
   │              ┌─────────▼──────────┐             │
   │              │ METRICS ENGINE     │             │
   │              │ TSS · AC · fit p   │◄────────────┘
   │              │ n₅₀ · decay curve  │
   │              │ cost per step      │
   │              └─────────┬──────────┘
   │                        │
┌──▼────────────┐  ┌────────▼─────────┐  ┌───────────────────┐
│ REPLAY ENGINE │  │ REPORT           │  │ EXPORT            │
│ stubs, cursors│  │ decay curve,     │  │ OTel GenAI spans  │
│ integrity     │  │ budget statement,│  │ → Langfuse /      │
│ checks        │  │ regression diff  │  │   Phoenix / DD    │
└───────────────┘  └──────────────────┘  └───────────────────┘
```

### Key design decisions

| Decision | Choice | Why |
|---|---|---|
| Language | Python (TS SDK later) | Where agents are built |
| Integration surface | Adapter per framework, one interface | Avoids framework lock-in; grows adoption |
| Telemetry | OTel GenAI semantic conventions | Standard-conformant; never compete on storage |
| Storage (OSS) | Local SQLite + JSONL tape | Zero-infra install; `pip install halflife` and go |
| Storage (cloud) | Postgres + object store for tapes | Later |
| Sandboxing | Reuse the user's; provide Docker helper | Not our differentiator |
| Licence | Apache-2.0 | Permissive attracts enterprise use; matches Future AGI, Phoenix |

---

## 5. Metric definitions (write these precisely — they are the moat)

```python
# Tool Sequence Similarity between two runs, over the first m steps
TSS(a, b, m) = 1 - levenshtein(tools(a)[:m], tools(b)[:m]) / max(len_a, len_b)

# Divergence score across k runs of one task
DIV(task, k, m) = 1 - mean(TSS(ri, rj, m) for all pairs i<j)

# Per-step reliability, fitted across horizon sweep
# success(n) = p^n  ->  log(success) = n * log(p)
p_hat = exp(weighted_least_squares(log(success_rates), horizons))

# Horizon at which success falls below threshold t
n_t = log(t) / log(p_hat)

# Cost-normalised reliability: reliability per rupee at horizon n
CNR(n) = p_hat^n / cost_per_task(n)
```

`CNR` is the number a platform team actually optimises and is, as far as this research found, unnamed anywhere in the literature or in any product. Naming a metric that people adopt is one of the highest-leverage things an individual engineer can do.

---

## 6. Open source / commercial split

**Apache-2.0 core** — everything above. CLI, SDK, adapters, recorder, replay, metrics, local reports, OTel export. This is the artefact that gets you hired; it must be genuinely complete, not crippled.

**Hosted tier (later, only if usage appears)** — the things that are painful to self-host and only matter at team scale:

- Hosted tape storage with retention, PII scrubbing and sharing
- Historical decay tracking across releases — "your p dropped 0.014 this sprint"
- CI integration that comments the decay delta on a pull request
- Cross-team benchmark: how your `p` compares to anonymised peers at the same horizon
- Alerting on production decay drift

Pricing shape when it comes: usage-based on recorded runs with a free tier, which is the norm in this category. Do not design billing now.

---

## 7. Proof obligations

The product's claims must be backed by a measurement you ran yourself. This is the bridge to the capstone: **AMASE is the experimental engine that produces Halflife's evidence.** Same harness, two outputs — one academic, one commercial.

Required results before launch:

1. Decay curves for 3+ public agent frameworks on a shared task set, with `p` and `n₅₀` for each
2. Replication of the step-count-not-context-length finding on your own tasks — or a documented contradiction, which is more interesting
3. Demonstrated early-abort saving: "TSS-based abort at step 2 cut wasted spend X% at Y% false-abort rate"
4. A reproducibility claim: `halflife replay` reproduces N recorded failures with byte-identical tool sequences

Item 3 is the commercial headline. Item 4 is the engineering credibility.
