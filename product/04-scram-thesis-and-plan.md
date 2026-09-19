# SCRAM — The 1000× Thesis, From Scratch

**A runtime reliability governor for black-box agents.**
17 September 2026 · written after a full teardown of Velra and a fresh literature pass

*(Name proposal: SCRAM is the reactor-engineering term for an emergency shutdown of a runaway reaction. Short, memorable, and it describes exactly what the product does. Verify the npm/PyPI/GitHub namespace before committing.)*

---

## 1. The 1000× is real, but only on one axis

You cannot be 1000× better than Velra at writing Rust. Velra's code is excellent — zero `unsafe`, compile-time assertions guarding measured constants, a chaos suite for its fail-open property. You cannot be 1000× better at methodology either; its pre-registration and Fisher-exact power analysis are better than most funded teams'.

**You can be 1000× better at the thing that actually matters: how much money each intervention saves.**

That is not marketing. Here is the arithmetic, using Velra's own published measurements and September 2026 Anthropic list prices.

### Velra's value per intervention

Velra's four-run measurement of Claude Code's native summary: **670, 802, 4,410 and 6,937 tokens**. Median ≈ 2,606. Velra's capsule: **718 tokens**.

| Case | Tokens saved | Sonnet 5 ($2/M in) | Opus 5 ($5/M in) |
|---|---:|---:|---:|
| Typical (median native) | 1,888 | **$0.0038** | $0.0094 |
| Best observed | 6,219 | $0.0124 | **$0.031** |

**Velra saves roughly half a cent to three cents**, once per compaction event. Compaction fires once or twice in a long session.

To be fair to it: Velra does not claim to save money. It claims boundedness and determinism, and it delivers those. But if you are asking how to be 1000× better, the axis has to be economic, and this is the number on the other side of the comparison.

### The value of aborting a doomed run

From 165 GAIA traces ([arXiv:2606.01365](https://arxiv.org/abs/2606.01365)): among failed runs that emitted a warning signal, the **mean post-warning token fraction is 0.581, median 0.611** — 54.5%, 63.8% and 46.8% across difficulty levels 1–3. A small intervention pilot in the same study cut it to **0.304**.

Put plainly: **a failed agent run burns about 58% of its tokens after the first detectable sign that it is failing.**

Combine with measured agent economics: a 5-step loop costs 3.2× a single chat call, 50 steps exceeds 30×, a 200-step debugging session reaches **100×**. Median developer spend is **$480/month**, p90 **$1,650**, with a documented **$4,200 single weekend**.

| Scenario | Run cost | Saving at 58% post-warning |
|---|---:|---:|
| Typical substantial run (~$8) | $8 | **$4.64** |
| Heavy user run (p90, ~$27) | $27 | **$15.70** |
| The documented runaway weekend | $4,200 | **~$2,400** |

### The ratio

$4.64 ÷ $0.0038 = **1,220×** on the typical case.

Even the most conservative framing — Velra's best observed saving at Opus pricing against a modest $2 abort — gives **65×**. The typical case is three orders of magnitude.

**That is the honest 1000×.** Not better software. A better place to stand in the agent lifecycle.

---

## 2. What the science already proves

This matters enormously, because it means the hard question is settled and only the engineering is open.

### Early abort works, and works well

**"Doomed from the Start: Early Abort of LLM Agent Episodes via a Recall-Controlled Probe Cascade"** ([arXiv:2607.06503](https://arxiv.org/html/2607.06503)) places gates at rounds 1–6 and aborts episodes predicted to fail:

- **60.2% of tokens saved on TextCraft, 54.9% on WebShop** at a 90% global recall target
- At a conservative 95% recall: 45.0% and 41.5%
- Beat the best single-gate baseline in **all 6 cells** (2 environments × 3 models), by 1.5–8.8×
- Achieved recall stayed within one standard deviation of target in **all 24 configurations**
- Uses **Clopper-Pearson confidence bounds** for distribution-free guarantees — you can promise "at least 95% of runs that would have succeeded will survive the cascade" and mean it

That last property is what makes this deployable. Nobody adopts a tool that silently kills good runs. A certified recall floor turns it from a gamble into an engineering control.

### Black-box signals are competitive with model internals

- **"Automata from Agent Traces"** ([arXiv:2608.23670](https://arxiv.org/abs/2608.23670)) collapses a trace corpus into a compact finite-state machine — **7 to 43 states, built in milliseconds** — and reaches **held-out AUROC up to 0.94** for failure prediction, with an online monitor that ranks failing runs above passing ones from a *partial* trace. Critically: the FSM topology "appears shaped more by the deployment framework than the underlying LLM itself." **Model-agnostic.**
- **"Monitoring Web Agents Without Internal Signals"** ([arXiv:2609.02057](https://arxiv.org/abs/2609.02057)) finds observable trajectory signals "competitive with internal-signal baselines" across two benchmarks and five open and closed backbones, and supports "early intervention under fixed false-cut budgets."
- The GAIA study's six observable failure modes: tool instability and error rates, repeated action loops, low information gain from searches, evidence/grounding failures, execution failures, budget waste.

### The honest counterweight

The runtime-supervisor approach (**SupervisorAgent**, ICLR 2026) achieves a more modest **29.68% token reduction on GAIA** — and costs **15.45% of total tokens** for the supervisor itself plus **~1.5 minutes of added latency per task**. That is the realistic ceiling if you use an LLM to supervise. It is an argument for making the governor LLM-free.

---

## 3. The gap — stated precisely

Three facts, together:

1. **The strongest early-abort method needs model internals.** "Doomed from the Start" reads the residual-stream hidden state. Its own limitations section says the method "assumes access to internal hidden states (requires instrumented serving infrastructure)" and that "experiments use offline replay to extract activations; online deployment needs serving-stack modifications." **You cannot get hidden states from the Anthropic, OpenAI or Google APIs.** So the best method in the literature is unavailable to the overwhelming majority of production agents.

2. **The black-box methods that work are research artefacts.** The FSM paper and the web-agent monitor both demonstrate competitive prediction from observable traces. Neither ships as something a developer installs.

3. **The interception point already exists and nobody is using it for this.** Claude Code's `PreToolUse` hook **can block a tool call** — via exit code 2 or `permissionDecision: "deny"` with a reason — and can inject `additionalContext`. `Stop` can force continuation. `PermissionRequest` can deny. That is a complete intervention surface, available today, requiring **zero serving-stack modification**.

**Velra uses this surface to observe and to freeze context. It does not use it to intervene.**

So the gap is: *a black-box, LLM-free, statistically-certified runtime governor that predicts failure from observable trace signals and intervenes through the host's own hook API.*

The science says it should work. The API says it can be built. Nobody has built it.

---

## 4. The product

> **SCRAM watches your agent's trace in real time, predicts failure from behaviour alone, and stops the run before it burns the other 58% of your money — with a certified guarantee about how many good runs it will let through.**

### Four components

**C1 · The observer.** Hooks into the host at every tool boundary. On Claude Code that is `PreToolUse` / `PostToolUse` / `PostToolUseFailure` / `Stop`. Records the full envelope to a local append-only log. This is the piece Velra has already proven is buildable — and the piece you should design to be host-pluggable from day one.

**C2 · The predictor.** LLM-free, sub-millisecond, computed from observable signals only:

- repeated-action loop detection (edit-distance over the recent tool sequence)
- tool error rate over a sliding window
- information gain per step (are new files/symbols being touched, or the same ones?)
- FSM state occupancy — build the automaton from your own trace corpus, flag states with high historical failure rates
- budget burn rate against the declared horizon
- tool-sequence divergence across `k` cheap probe forks at early steps

Deliberately **no LLM in the hot path.** SupervisorAgent's 15.45% token overhead and 1.5-minute latency penalty is the cost of putting one there.

**C3 · The governor.** A cascade of calibrated gates at early rounds, exactly as the probe-cascade paper specifies, but over black-box features. Each gate has a threshold calibrated on held-out data so that the **global** recall of successful runs meets a user-set target. Escalating interventions, weakest first:

| Level | Action | Mechanism on Claude Code |
|---|---|---|
| 1 | Warn | `systemMessage` in hook output |
| 2 | Nudge | inject `additionalContext` telling the agent to re-plan |
| 3 | Block | `permissionDecision: "deny"` with a reason on the specific tool call |
| 4 | Halt | deny + escalate to the human with the trace |

**C4 · The ledger.** Every intervention is recorded with tokens and rupees saved or lost, so the tool proves its own worth. This is what converts "interesting" into "renewed".

### The contract that makes it adoptable

```
$ scram status
  recall target:      0.95 (certified, Clopper-Pearson, n=163 successful runs)
  achieved recall:    0.962  [0.941, 0.978]
  runs governed:      1,284
  aborted:            211  (16.4%)
  false aborts:       8    (0.62%)
  tokens saved:       14.2M
  money saved:        ₹9,840
  governor overhead:  0.4 ms p99, 0 tokens
```

**Zero tokens of overhead** is the line that wins the argument against every LLM-supervisor alternative.

---

## 5. Why Velra structurally cannot follow you here

Not a slight — a genuine architectural fact.

| | Velra | SCRAM |
|---|---|---|
| Intervention point | `PreCompact` — one boundary, occasionally | every tool call — continuously |
| Action | freeze and re-inject context | warn / nudge / block / halt |
| Value per event | $0.004–$0.031 | $2–$15 typical, $1,000+ tail |
| Failure mode if wrong | a slightly worse capsule | a killed good run — hence the recall guarantee |
| Required science | none, it's a pure function | calibrated prediction with distribution-free bounds |
| Data moat | none | grows with every trace calibrated |

Velra's design centre is *determinism*: its capsule is a pure function of a snapshot, which is why it can promise byte-identical output. SCRAM's design centre is *calibrated prediction*, which is inherently statistical. To build SCRAM, Velra would have to add a statistics layer, a calibration corpus and a recall-certification pipeline — that is not a feature, it is a different product with a different core competence.

And the moat compounds: every trace you calibrate on makes the gates tighter. Velra's capsule renderer does not get better with usage.

---

## 6. The claim you will be able to make — and how you prove it

Target headline, stated as a falsifiable claim:

> *On N real agent runs, SCRAM saved X% of tokens on failed runs at a certified ≥95% recall of successful runs, with zero LLM overhead and sub-millisecond p99 latency.*

### Proof obligations, in order

1. **Corpus.** Collect ≥300 real agent runs with known outcomes. The certification arithmetic is fixed and unforgiving: `n ≈ ln(α)/ln(ρ*)` — **114 successful episodes certify up to 0.974 recall, 149 certify 0.98, 299 certify 0.99.** This is a hard data requirement, independent of how good your predictor is. Start collecting on day one.
2. **Baseline.** Reproduce the GAIA post-warning fraction on your own corpus. If you do not find ~58%, say so — that is a finding.
3. **Predictor.** Black-box features only. Report AUROC per round against the published hidden-state numbers (AUC 0.81–0.86 at round 1 on TextCraft; up to 0.94 for FSM-based).
4. **Cascade.** Calibrate gates, certify recall on a frozen held-out split, report achieved-vs-target across configurations.
5. **The money table.** Tokens and rupees saved, false-abort rate, overhead. This is the artefact.
6. **A negative result, published.** Where the predictor is near-chance — the WebShop/Llama cell in the original paper was — say so and abstain rather than abort.

---

## 7. Build plan — compressed, because Velra proved six days is possible

Velra went from nothing to installable-with-benchmarks in six days. That is the new baseline for what one person can do. This plan is aggressive on purpose.

### Week 1–2 · Observe and ship something

- Hook observer for Claude Code: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `Stop`. Append-only log.
- **Fail-open from the first commit.** Every hook exits 0, always. Chaos suite for locked / corrupt / read-only / missing DB. Copy this discipline from Velra wholesale — it is non-negotiable for anything in the critical path.
- Ship `waste-probe`: an offline tool that reads a trace and reports what fraction of tokens were burned after the first warning signal. **40 lines of logic, ships in week one, immediately useful to strangers, and it is your corpus-collection funnel.**
- One-command install. Not a later phase.

**Exit:** a stranger can run `scram waste` on their own trace and get a number.

### Week 3–5 · Predict

- Implement the six observable signals. No LLM anywhere in the path.
- Build the FSM from the collected corpus; measure per-state failure rates.
- Report per-round AUROC. Compare honestly against the published hidden-state figures.
- Pre-registration file committed **before** any evaluation run.

**Exit:** an AUROC curve by round, from black-box features, on real traces.

### Week 6–8 · Govern

- Gate cascade with Clopper-Pearson calibration and a global recall budget search.
- Four escalation levels wired to the real hook outputs.
- The ledger — tokens and money saved, false aborts counted.
- Shadow mode by default: predict and log, do not act, until the user opts in.

**Exit:** certified recall on a frozen split, with the achieved-vs-target table.

### Week 9–12 · Prove and publish

- Run the full evaluation. Publish the money table and at least one negative result.
- Blog post: *"A failed agent run burns 58% of its tokens after it is already doomed. We measured it, then stopped it."*
- arXiv preprint: black-box recall-controlled abort, positioned as the deployable counterpart to the hidden-state cascade.
- Second host adapter to prove portability.

**Exit:** a stranger can install it, point it at their agent, and see money saved.

---

## 8. What will actually go wrong

| Risk | Reality | Response |
|---|---|---|
| **False aborts** | The product risk. Killing a good run is far worse than wasting tokens. | Recall guarantee is the whole design. Shadow mode by default. Never ship an abort without a certified floor. |
| **Black-box features may be much weaker than hidden states** | Genuinely possible. The GAIA study's observable signals were only correlational (r=0.55 at best). The web-agent paper claims competitiveness but publishes no headline numbers in its abstract. | Test this in week 5, before building the cascade. If AUROC is too low at rounds 1–2, move the gates later — savings shrink but the product survives. |
| **Corpus collection is slow** | 299 successful runs for 0.99 certification is a lot for one person. | `waste-probe` is the funnel — it gives people a reason to send you traces. Start at a 0.95 target (needs far fewer) and tighten. |
| **Anthropic changes the hook API** | Same existential risk Velra carries. | Adapter layer from day one; the predictor and governor are host-independent. Only the observer is coupled. |
| **Someone ships this first** | The papers are public; the ideas are in the air. | Your advantage is the deployable black-box framing plus the hook surface. Move in weeks, not months. |
| **Latency in the hot path** | `PreToolUse` runs on every tool call. Velra's own p99 tail failed its budget at 163 ms. | Sub-millisecond budget, enforced by a benchmark in CI that fails the build. Rust for the hot path is worth serious consideration for exactly this reason. |

---

## 9. The honest summary

The 1000× is not in the code. It is in the choice of where to stand.

Velra intervenes at a boundary that fires occasionally and saves cents. SCRAM intervenes at a boundary that fires constantly and saves dollars — and the difference is about three orders of magnitude, computed from both projects' own published numbers.

The science that makes it possible is already published and strong. The interception surface already exists and can block. The one thing standing between you and the product is that nobody has connected a black-box predictor to a hook that says `deny` — with a number attached saying how often it will be wrong.

That is a twelve-week build, not a moonshot.

---

## Sources

- [Doomed from the Start: Early Abort of LLM Agent Episodes (arXiv:2607.06503)](https://arxiv.org/html/2607.06503)
- [Early Diagnosis of Wasted Computation in Multi-Agent LLM Systems (arXiv:2606.01365)](https://arxiv.org/abs/2606.01365)
- [Automata from Agent Traces: Failure and Next-Step Prediction (arXiv:2608.23670)](https://arxiv.org/abs/2608.23670)
- [Monitoring Web Agents Without Internal Signals (arXiv:2609.02057)](https://arxiv.org/abs/2609.02057)
- [Stop Wasting Your Tokens: SupervisorAgent, ICLR 2026 (arXiv:2510.26585)](https://arxiv.org/html/2510.26585v2)
- [AgentSpec: Customizable Runtime Enforcement for Safe and Reliable LLM Agents, ICSE 2026](https://cposkitt.github.io/files/publications/agentspec_llm_enforcement_icse26.pdf)
- [Deterministic Governance Kernels for Agent Runtimes — Zylos](https://zylos.ai/research/2026-03-11-deterministic-governance-kernels-agent-runtimes/)
- [Claude Code Hooks reference](https://code.claude.com/docs/en/hooks)
- [Claude Code hook events and blocking — Morph](https://www.morphllm.com/claude-code-hooks)
- [Anthropic API pricing, September 2026 — BenchLM](https://benchlm.ai/anthropic/api-pricing)
- [Agentic AI cost runaway and token budgets — LeanOps](https://leanopstech.com/blog/agentic-ai-cost-runaway-token-budget-2026/)
- [Your Failed Agent Run Burns Most of Its Tokens AFTER It Fails](https://dev.to/alex_spinov/your-failed-agent-run-burns-most-of-its-tokens-after-it-fails-measure-it-in-40-lines-4ef)
- Velra repository, source and `BENCHMARK_REPORT.md`, read directly
