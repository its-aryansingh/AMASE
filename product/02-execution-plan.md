# Halflife — 12-Month Execution Plan

Optimised for one outcome: **an offer from a frontier lab or top-tier AI startup.**
Secondary: real OSS users. Tertiary: revenue.

---

## The strategy in four sentences

Anthropic tells you exactly what to do: *put independent research, an insightful blog post, or substantial open-source contributions at the top of your resume.* So build one tool that is genuinely used, publish one measurement nobody else has made, and spend six months being visibly useful to the people who would refer you. Referrals built 6–12 months ahead are repeatedly cited as the difference between an offer and a silent rejection at sub-1% acceptance rates. Everything below serves those three things, in that order.

---

## Phase structure

| Phase | Weeks | Outcome | The one thing that must be true at the end |
|---|---|---|---|
| 0 · Foundations | 1–4 | Harness + recorder | You can record and replay one agent run byte-identically |
| 1 · The metric | 5–10 | Decay measurement works | You can state your own agent's `p` and `n₅₀` with a CI |
| 2 · First result | 11–16 | Published measurement | A blog post with decay curves for 3 frameworks is live |
| 3 · Open source | 17–26 | `pip install halflife` | Ten people you did not recruit have used it |
| 4 · Compounding | 27–40 | Paper + adoption | A preprint is on arXiv and the repo has real issues from strangers |
| 5 · Conversion | 41–52 | Applications | You apply with referrals already in place |

The capstone deadline sits inside Phase 2. That is deliberate — the academic write-up and the launch post are the same research, formatted twice.

---

## Phase 0 — Foundations · Weeks 1–4

The unglamorous half of the project. Do it properly; everything else sits on it.

- Repo, Apache-2.0, `uv`, typed config, pre-commit, CI from commit one
- **Adapter interface** — `run(task, config) -> Trace`. Implement LangGraph first, raw-callable second
- **Recorder** — envelope capture at the graph boundary: assembled prompt, params, completion, tool I/O, retrieved chunks, model version, timestamps. Append-only JSONL, hash-chained
- **Canonicalisation** — strip run ids, timestamps, trace ids before hashing, or every fixture key will miss
- **Replay engine** — per-event-type cursors, exhaustion detection, integrity checks, deterministic stubs that never touch network or clock
- Hard caps on turns, wall-clock and spend at the boundary. Non-negotiable: one runaway loop is a ₹4,000 weekend

**Exit test:** record a 20-step run, replay it offline with zero network calls, assert byte-identical tool sequence. Write that as a passing test in CI.

**Why this first:** replay is the hardest thing to retrofit and the thing most competitors do partially. Getting it right early is the technical moat.

---

## Phase 1 — The metric · Weeks 5–10

- **Horizon runner** — sweep `n ∈ {5, 10, 20, 40}`, repeat `k ≥ 5` per point, control seeds where the provider allows, record what is uncontrollable
- **TSS / AC implementation** — Levenshtein over tool sequences, Jaccard over argument pairs. Validate against the published means (TSS ≈ 0.87, AC ≈ 0.69) as a sanity check on your implementation
- **Decay fit** — weighted least squares on `log(success) vs n`, bootstrap CI for `p`
- **Cost model** — per-model token pricing table, USD attributed per step, per task, per horizon
- **Report** — decay curve, the budget statement sentence, cost-normalised reliability

**Exit test:** run it against your own agent and produce the sentence *"At p = X (95% CI …), success drops below 80% at n = Y and below 50% at n = Z."*

**Risk:** your fit may not be clean geometric. If it is not, that is a finding, not a failure — report the deviation and what shape it actually takes. A documented contradiction of a published result is *more* valuable than a replication.

---

## Phase 2 — First result · Weeks 11–16

This is the phase that generates the hiring artefact.

- Measure decay curves for **3+ agent frameworks** on one shared task set — LangGraph, OpenAI Agents SDK, and one more
- Replicate (or contradict) the step-count-not-context-length finding on your own tasks
- Implement the **early-abort guard** and measure it: wasted spend saved vs false-abort rate, as a curve
- Write the post

### The post

Title shape: *"We measured how fast coding agents die. Here is the decay constant."*

Structure:

1. The hook — your evals say 90%, production says 30%, and that gap is arithmetic, not a bug
2. The maths — `p^n`, why one number hides it
3. The measurement — method, n, variance, what you could not control
4. The curves — three frameworks, side by side
5. The surprise — whichever of your findings contradicts intuition
6. The practical payoff — early abort saved X% of spend
7. The tool — `pip install halflife`, ten lines to reproduce every figure

Publish on your own domain, cross-post to Hacker News and the relevant Discords. **Ship the tool in the same week**, or the attention is wasted.

**Capstone overlap:** the same measurements, the same figures, reformatted into the department's synopsis and IEEE paper. Do the research once.

---

## Phase 3 — Open source · Weeks 17–26

Adoption is a distribution problem, not a quality problem. Quality is table stakes.

- **Five-minute quickstart.** If someone cannot get a decay curve in five minutes, they leave. This is the single highest-leverage thing in the phase
- README that leads with the *output* — show the curve and the budget sentence above the fold, not the install instructions
- Adapters for the top three frameworks, each with a worked example
- **Integrate, don't compete** — ship the Langfuse/Phoenix exporters and say so loudly. "Works with your existing stack" removes the main objection
- GitHub Action that posts the decay delta as a PR comment
- Answer every issue within 24 hours for the first six months. This is what converts a repo into a project

**Distribution channels, in order of value:** the launch post's own audience → HN → the framework Discords where your adapters help → a conference talk (submit to anything agent-adjacent) → dev.to/Medium syndication last.

**Exit test:** ten people you did not personally recruit have run it, and at least three opened issues.

---

## Phase 4 — Compounding · Weeks 27–40

- **arXiv preprint** — the measurement from Phase 2, extended with more frameworks and horizons. A preprint is cheap and is the artefact Anthropic's advice explicitly names
- **Second post** — whatever you learned from users. "What 40 teams' decay curves look like" if you get data; otherwise a deeper technical piece on replay
- Harden: multi-framework support, better CI story, docs site
- **Begin the relationship work.** See below — this is not optional and it starts now, not in month eleven

---

## Phase 5 — Conversion · Weeks 41–52

- Targeted applications with referrals already in place
- Company-specific preparation. Generic preparation is near-worthless at <1% acceptance:
  - **Anthropic** — CodeSignal screen (~520+/600), plus a safety-focused behavioural round that expects genuine engagement with alignment thinking, not surface familiarity. Your reliability work is directly on-thesis: measuring when autonomous systems fail is a safety argument, and you should make it explicitly
  - **OpenAI** — they send a paper days ahead and ask you to find its limitations and propose extensions. Practise this on the three papers underpinning Halflife
  - **DeepMind** — first-principles maths, JAX, hiring committee
- Resume line, once Phase 2 ships:

> **Halflife** — open-source reliability measurement for long-horizon LLM agents. Measures per-step reliability and projects collapse horizons; TSS-based early abort cut wasted agent spend X% at Y% false-abort rate. Apache-2.0, N stars, used by [teams]. *[repo] [post] [preprint]*

---

## The relationship track — run this in parallel from week 20

The research is unambiguous that referrals matter more than applications, and that they need 6–12 months. Concretely:

- Identify 20–30 people who work on agent infrastructure at target companies
- Be *useful* to them publicly: thoughtful issues on their repos, a bug report with a reproduction, a benchmark they can use
- Your tool is the introduction. "I measured your framework's decay curve, here is what I found" is a legitimate, welcome first contact. A cold "can you refer me" is not
- Contribute to one adjacent OSS project meaningfully — one substantial merged PR beats fifty drive-by fixes

This is the highest-variance, highest-return activity in the plan and the one most likely to be skipped. Put it in the calendar.

---

## Budget

| Item | Estimate |
|---|---|
| Inference for decay sweeps | ₹15,000–25,000 over the year |
| Domain + docs hosting | ₹3,000 |
| Compute (local Docker, local models) | ₹0 |

Control: full sweeps on a cheap or local model; frontier models only for final confirmatory curves. Hard per-run caps. Apply for student/startup credits in week 1 — approval is slow.

---

## What would make me change this plan

Honest failure conditions, so you can tell early:

- **Braintrust ships per-step reliability.** Likely within 12 months given they already cite the compounding problem. Response: you will have shipped first and can go deeper on the runtime guard, which is a harder engineering problem and further from their trace-platform centre of gravity
- **Your decay fit isn't geometric.** Not fatal — publish the real shape
- **Nobody adopts it.** Then the paper and the engineering artefact still stand on their own, which is why the plan front-loads the measurement over the growth
- **The early-abort guard doesn't save money.** This is the real product risk. Test it by week 14, before you build the polished OSS release. If the false-abort rate is too high, the tool is a measurement library rather than a runtime control — still publishable, smaller product

---

## The single sentence to keep in view

> Build one tool that measures something real, publish the measurement, be useful to the people who would hire you, and let the artefact do the arguing.
