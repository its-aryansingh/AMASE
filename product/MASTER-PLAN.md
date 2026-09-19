# AMASE — The Master Plan

**One name. One codebase. One scoreboard.**
17 September 2026 · supersedes the Halflife and SCRAM drafts

---

## 0. First, a consolidation

Across this research I proposed three names — AMASE, Halflife, SCRAM. That was two too many. Collapsing them:

- **Halflife** is taken on PyPI and crates.io. **SCRAM** is taken on npm, PyPI *and* crates.io.
- **AMASE is free on all three.** It is also already the title on your submitted synopsis, and it is the name you want.

So AMASE is the product. Not a research project that feeds a differently-named product — **the thing itself.**

And the submitted synopsis needs no rework. Its title is *"A Harness-Ablation Framework for Measuring Cost–Quality Trade-offs in Autonomous Software Engineering Agents."* Aborting a doomed run early **is** a cost–quality trade-off, measured. The capstone and the product are the same sentence.

---

## 1. What AMASE is, in one line

> **Your agent burns 58% of its tokens after it is already doomed. AMASE stops it — and tells you exactly how often it will be wrong.**

Two commands, one binary, one codebase:

| Command | What it does | Serves |
|---|---|---|
| `amase measure` | runs your agent at several horizons, fits per-step reliability, publishes the decay curve and cost table | the capstone, the paper, the credibility |
| `amase guard` | watches a live run, predicts failure from behaviour alone, blocks the doomed ones at a certified recall bound | the product, the money, the users |

`measure` produces the calibration that `guard` needs. `guard` produces the traces that improve `measure`. They are the same system read in two directions — which is why this is one project and not two.

---

## 2. The three numbers this rests on

Everything below is downstream of these, and each is a published measurement, not an assertion.

**1. The waste is real and it is enormous.** Across 165 GAIA traces, among failed runs that emitted a warning signal, the mean post-warning token fraction is **0.581**, median **0.611**. A small intervention pilot in the same study cut it to **0.304**. Agents burn most of a failed run's budget after the failure is already detectable.

**2. Stopping them works, and the science is settled.** A recall-controlled probe cascade saved **60.2% of tokens on TextCraft and 54.9% on WebShop** at a 90% global recall target, beating the best single-gate baseline in all 6 cells. It uses Clopper-Pearson bounds, so the recall guarantee is distribution-free — you can promise "95% of runs that would have succeeded survive" and mean it.

**3. It can be done without model internals.** That cascade reads residual-stream hidden states, which **no frontier API exposes** — its own limitations section says online deployment "needs serving-stack modifications." But the FSM-from-traces work reaches **held-out AUROC up to 0.94** on failure prediction from observable traces alone, with topology "shaped more by the deployment framework than the underlying LLM itself." Model-agnostic. And the web-agent monitor finds observable signals "competitive with internal-signal baselines."

**The gap in one sentence: the best method needs internals nobody can get; the methods that work black-box exist only as papers; and the hook that can actually block a tool call is sitting unused in Claude Code.**

---

## 3. Why this beats Velra — the short version

Velra is genuinely good. Zero `unsafe`, 162 tests, compile-time assertions guarding measured constants, a pre-registration file and a published FAILED verdict. Respect it, and say so publicly — more on why that is tactical, not polite, in §7.

You beat it on **where you stand**, not on craft:

| | Velra | AMASE |
|---|---|---|
| Fires | at compaction — 1–2× a session | every tool call |
| Does | freezes and re-injects context | warns, nudges, blocks, halts |
| Saves | **$0.004–$0.031** per event | **$2–$15** typical, $1,000+ tail |
| Guarantees | byte-determinism | **certified recall bound** |
| Gets better with use | no — pure function | yes — calibration corpus compounds |

**~1,200× on value per intervention, computed from both projects' own published numbers** at September 2026 Anthropic pricing.

And the one architectural fact that decides the performance row: Velra's own spawn control — the same binary exiting immediately, doing nothing — costs **4.27 ms p50 / 24.95 ms p99**. That is OS process creation, paid before any of their code runs, and it is why their worst marginal p99 hit **163.307 ms** and H4 failed. **Claude Code has supported HTTP hooks since February 2026.** A resident server has no fork, no exec, no per-call database open. Their 163 ms tail is not something you optimise — it is a category you decline to enter.

---

## 4. Architecture

```
Claude Code ──HTTP POST──► amase guard (resident, Rust)
   │  (same JSON as command hooks)         │
   │                                        ├─► ring buffer (lock-free, zero-alloc)
   │  ◄──2xx + decision fields──────────────┤        │
   │     allow / deny+reason / context      │        └─► background drain → SQLite
   │                                        │
   └── command-binary fallback              ├─► PREDICTOR (LLM-free, <200µs)
       when server is down                  │   loop distance · tool error rate
                                            │   information gain · FSM state
                                            │   budget burn · probe divergence
                                            │
                                            ├─► GOVERNOR (calibrated gate cascade)
                                            │   L1 warn · L2 nudge · L3 block · L4 halt
                                            │
                                            └─► LEDGER (tokens + ₹ saved, false aborts)
```

**Four rules that are not negotiable:**

1. **Zero LLM in the hot path.** The LLM-supervisor alternative achieves 29.68% token reduction but costs 15.45% of tokens for itself plus ~1.5 minutes per task. That is the ceiling you refuse.
2. **Fail-open, always.** Every path returns allow on any internal error. Velra's record is 481 invocations, 0 non-zero exits, 0 stderr bytes. Match it, with a wider fault matrix.
3. **Shadow mode by default.** Predict and log; do not act until the user opts in and has seen their own numbers.
4. **Never abort without a certified floor.** The recall bound is the product.

---

## 5. The scoreboard you will publish

Condensed from the full 31-parameter analysis. Every row must be a measurement you can reproduce.

| Parameter | Velra 0.1.1 | AMASE target |
|---|---|---|
| Value per intervention | $0.004–$0.031 | $2–$15, measured |
| Intervention frequency | 1–2 / session | every tool call |
| Hook transport | command (spawn) | HTTP + command fallback |
| Marginal p50 | 3.455–10.635 ms | **< 0.2 ms** |
| Worst marginal p99 | **163.307 ms** | **< 1 ms** |
| Latency verdict | **FAILED** | PASSED, gated in CI |
| `unsafe` | 0 | 0 |
| `.unwrap()` in src | 71 | **0**, lint-enforced |
| Formal verification | — | **Kani harnesses** |
| Fuzzing | — | cargo-fuzz on parsers |
| Fail-open | 481 / 0 | matched, wider matrix |
| Reproducible build | not claimed | **verified in CI** |
| Binary budget | 8 MB | **< 3 MB** |
| Statistics | fixed-n Fisher | **anytime-valid** + Fisher |
| Guarantee | byte-determinism | **certified recall** |
| Hypotheses passed | **1 of 4** | **3 of 4** |
| Hosts | 1 | 3 |

**The row that matters most is the last-but-one.** Velra passed 1 of 4 because their control found compaction was not lossy on their task — the baseline never failed, so the arms could not separate. Their own threats section says a harder defect "would leave more room for the arms to diverge."

The fix is one line in your protocol: **run a positive control first.** Prove the phenomenon exists on your task set before spending money measuring an intervention against it. That single procedural addition is what turns INCONCLUSIVE into a verdict, and you learned it free from their published failure.

---

## 6. Sixteen weeks

Compressed deliberately. Velra went nothing → installable → benchmarked in six days. That is the bar for what one person can do.

### Weeks 1–2 · Exist

- `amase waste` — reads a trace, reports the fraction of tokens burned after the first warning signal. Forty lines of logic. **Ship it standalone in week one.**
- One-command install, three platforms, SHA-256 verified. Not a later phase.
- Fail-open from the first commit; chaos suite alongside.
- `#![deny(clippy::unwrap_used, expect_used, panic)]` at crate root from commit one — free, permanent, and it is a row on the scoreboard.

**Exit: a stranger runs `amase waste` on their own trace and gets a number.**

### Weeks 3–5 · Observe and predict

- HTTP hook server + command fallback. Criterion benchmark in CI failing the build above 1 ms p99.
- Six black-box signals. FSM built from the collected corpus.
- **Pre-registration committed before any evaluation run.**
- Report per-round AUROC honestly against the published hidden-state figures.

**Exit: an AUROC-by-round curve from black-box features on real traces.**

> **This is the go/no-go.** If AUROC at rounds 1–2 is near chance on coding agents, move the gates later — savings shrink, the product survives. Decide this before building the cascade, not after.

### Weeks 6–9 · Govern

- Gate cascade, Clopper-Pearson calibration, global recall budget search.
- Four escalation levels wired to real hook outputs.
- The ledger: tokens and rupees saved, false aborts counted.
- Anytime-valid sequential testing alongside fixed-n Fisher, so the comparison is direct.

**Exit: certified recall on a frozen split, with the achieved-vs-target table.**

### Weeks 10–12 · Prove

- Positive control, then the full evaluation.
- Decay curves for 3+ frameworks — this is the capstone's ablation table.
- **Publish at least one negative result.** It is the highest-credibility-per-word content that exists, and Velra proved it works.
- Kani harnesses, starting with `redaction_never_leaks`. Velra tests redaction against a corpus; proving it for all inputs within a bound is a categorically stronger claim.

### Weeks 13–16 · Launch and compound

- Reproducible-build verification, SLSA L3 provenance, binary under 3 MB.
- Second host adapter — breaks the single-host coupling Velra cannot escape.
- The post, the preprint, the comparison table.
- Capstone final report and paper, from the same measurements.

---

## 7. The launch — and why hostility loses

You want to win publicly. The evidence on how says: **do it by being scrupulously fair.**

Hacker News, where this lands or dies, is explicit — *"Don't use superlatives (fastest, biggest, first, best). Modest language is stronger."* And: *"Don't sell to this audience. If you try, they will close the tab."* The Fly.io launch that worked had its founder answering 53 comments; booster comments from friends are the recognised way to get flagged.

So the comparison table ships **with Velra credited by name, and with the rows where it wins stated plainly** — zero `unsafe`, a perfect fail-open record, a pre-registration file better than most funded teams'. A table that admits the competitor beat you somewhere is believed. One claiming total superiority reads as marketing and gets dismantled in the comments by someone who cloned both repos.

The strongest thing you can do to Velra is not attack it. It is to publish a scoreboard so honest that the 163 ms row and the 1-of-4 row speak for themselves.

### The launch sequence

| Order | Channel | Expectation |
|---|---|---|
| 1 | Show HN, with the post already published | 500–2,000 signups in 24h when it lands |
| 2 | GitHub + awesome-* list submissions | 50–200/month sustained |
| 3 | Dev.to / technical syndication | 10k+ views per strong post |
| 4 | Build-in-public weekly updates | reported 4× more early adopters |

**Title shape:** `Show HN: AMASE – stop AI agent runs that are already doomed`. Plain, no superlative, says what it does.

**The post:** hook with the 58% number, the maths of why one score hides it, your measurement, the curves, the surprise, the money saved, then `pip install amase`. Ship the tool the same day or the attention is wasted.

Open-source core with a free tier is the growth lever: open-source dev tools reportedly grow ~8× faster in the first 12 months, and 73% of paid conversions come from free-tier users.

---

## 8. What actually kills this

| Risk | Honest assessment | Response |
|---|---|---|
| **Black-box signals too weak on *coding* agents** | The sharpest risk. Coding agents legitimately re-read the same file and re-run tests — behaviour that looks exactly like a loop. The published results are on TextCraft and WebShop, not code. | Week-5 go/no-go. Measure AUROC before building the cascade. Move gates later if needed. |
| **False aborts** | Killing a good run is far worse than wasting tokens. One bad abort loses a user permanently. | Recall guarantee is the design. Shadow mode default. Never abort without a certified floor. |
| **Corpus collection is slow** | 299 successful runs to certify 0.99; 114 for 0.974. Fixed arithmetic, independent of predictor quality. | `amase waste` is the funnel. Start at 0.95 and tighten. |
| **Anthropic changes the hook API** | The same existential risk Velra carries. | Adapter layer from day one. Predictor and governor are host-independent; only the observer couples. |
| **Someone ships first** | The papers are public. The ideas are in the air. | Weeks, not months. Week-one `amase waste` plants the flag. |
| **You never ship** | The actual most likely failure. Five planning documents now exist and zero lines of product code. | Item 1 is two days. Do it before the next planning session. |

---

## 9. The next 72 hours

1. Claim `amase` on npm, PyPI, crates.io and GitHub. Free today; may not be next month.
2. Create the repo. Apache-2.0. `deny(unwrap_used)` in the first commit. CI on three platforms in the second.
3. Write `amase waste` — read a trace, find the first warning signal, report the post-warning token fraction, exit non-zero over a threshold so it works in CI.
4. Run it on your own Claude Code traces. Whatever number comes out is your first piece of original evidence.
5. Post that number. One paragraph, one chart, nothing else.

That is the whole of week one, and it converts five documents into a project.

---

## Sources

- Velra repository — source, `BENCHMARK_REPORT.md`, `DECISIONS.md`, read from a clone
- [Early Diagnosis of Wasted Computation in Multi-Agent LLM Systems (arXiv:2606.01365)](https://arxiv.org/abs/2606.01365)
- [Doomed from the Start: Early Abort of LLM Agent Episodes (arXiv:2607.06503)](https://arxiv.org/html/2607.06503)
- [Automata from Agent Traces: Failure and Next-Step Prediction (arXiv:2608.23670)](https://arxiv.org/abs/2608.23670)
- [Monitoring Web Agents Without Internal Signals (arXiv:2609.02057)](https://arxiv.org/abs/2609.02057)
- [Stop Wasting Your Tokens: SupervisorAgent, ICLR 2026 (arXiv:2510.26585)](https://arxiv.org/html/2510.26585v2)
- [Kani: A Model Checker for Rust (arXiv:2607.01504)](https://arxiv.org/html/2607.01504v1)
- [Anytime validity is free: inducing sequential tests (arXiv:2501.03982)](https://arxiv.org/html/2501.03982)
- [Claude Code hooks reference](https://code.claude.com/docs/en/hooks)
- [Claude Code hooks: all lifecycle events and handler types](https://claudefa.st/blog/tools/hooks/hooks-guide)
- [How to launch a dev tool on Hacker News](https://www.markepear.dev/blog/dev-tool-hacker-news-launch)
- [Developer tool first 1000 users strategy](https://revenuefast.in/grow/developer-tool-first-1000-users)
- [Anthropic API pricing, September 2026](https://benchlm.ai/anthropic/api-pricing)
