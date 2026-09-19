# Agent Reliability — Market & Technical Research Dossier

**Prepared for: Aryan Raj Singh · 17 September 2026**
Scope: what to build commercially in agent infrastructure, and what actually converts into a frontier-lab offer.

---

## 0. The one-paragraph finding

The entire agent-evaluation industry reports the wrong number. Every platform — Braintrust, LangSmith, Arize, Galileo, Maxim — reports **aggregate task completion rate**, a single pass@1-style score. But agent success decays *geometrically* with step count, and the decay constant is the thing that actually predicts production behaviour. A 2026 study of 10,664 trajectories found that **every model tested falls from near-perfect success to near-zero within sixteen steps** on agentic tasks, and that this is driven by step count, not context length. Nobody sells a tool that measures the decay constant. That gap is the product.

---

## 1. Compensation and hiring: what "ultra-high-paying" actually means

| Company / role | Reported median total compensation |
|---|---|
| OpenAI, Software Engineer | **$630K** |
| Anthropic (technical, overall) | **$545K** |
| Anthropic, Research Scientist | **$746K** |
| Meta, ML Engineer | **$469K** |
| Frontier-lab Research Scientist band | $500K–$800K+ |
| OpenAI Residency (career-changers) | $220K |

Acceptance rates at these labs are under 1%.

### What they screen for — in their own words

Anthropic's guidance is unusually explicit and it directly validates an open-source strategy:

> *"If you have done interesting independent research, written an insightful blog post, or made substantial contributions to open-source software, put that at the TOP of your resume."*

About half of Anthropic's technical staff do not hold PhDs. Meta's framing: *"Ship tools, not demos. Architect for scale, not just accuracy."*

### What gets rejected

- Tutorial-tier projects (MNIST, Titanic)
- Jupyter notebooks with no deployment evidence
- Cold applications without a relationship

### What actually converts

- Deployed production systems **with monitoring**
- A public technical artefact — paper, blog post, or widely-used OSS
- A GitHub presence that reads as professional work
- **Referrals or genuine relationships built 6–12 months before applying**

Process notes worth knowing: Anthropic runs a CodeSignal screen (~520+/600) plus a safety-focused behavioural round that expects real engagement with alignment thinking, not surface familiarity. OpenAI sends a research paper days ahead and asks you to identify its limitations and propose extensions live.

**Read-through:** the highest-leverage artefact is one deep, genuinely-used open-source tool with a published measurement behind it. That is precisely the shape of the product below.

---

## 2. Market structure: what is taken, what is open

| Category | State | Players |
|---|---|---|
| Durable execution / orchestration | **Heavily funded** | Temporal ($5B valuation, $300M raised Feb 2026), Inngest, Restate |
| Observability & evaluation | **Crowded, consolidating** | Braintrust ($800M valuation; Notion, Dropbox as customers), LangSmith, Arize, Galileo, Maxim. **Langfuse acquired by ClickHouse, Jan 2026** |
| Gateway / proxy | **Fragmented, weak monetisation** | LiteLLM (418M monthly PyPI downloads, struggling to monetise), Portkey, OpenRouter |
| Enterprise APM expansion | Encroaching | Datadog, Dynatrace, New Relic at $50–100K+/yr |
| **Agent testing / QA** | **Named underserved** | Fragmented |
| **Cost optimisation tooling** | **Named underserved** | Fragmented |

Demand-side signal: **57% of organisations now run agents in production, but only about one-third are satisfied with their observability.** That is a replacement market, not a greenfield one — which matters, because it means buyers exist and are unhappy, rather than needing to be created.

Market size projections: enterprise AI governance $2.2B (2025) → $9.5B (2035); broader autonomous agent market up to $199B by 2035.

### Named structural gaps

1. Multi-agent system observability — frameworks lack cross-agent debugging
2. Real-time online evaluation — 52% run offline evals, only **37% run production evals**
3. Human-in-the-loop at scale — annotation workflows mostly custom-built
4. Cross-platform governance — agent sprawl without coordination standards
5. Automated compliance — only 28% test for bias, 22% for interpretability

---

## 3. The production pain, quantified

### Failure rates

- Agents fail **70–95% of the time** in production depending on task complexity
- **88%** of enterprise agents that work in demos fail in real workflows
- **95%** of generative AI pilots deliver no measurable P&L impact (MIT)
- WebArena: best GPT-4 agent scored **14.41%** end-to-end vs **78.24%** human
- Carnegie Mellon: agents fail common office tasks roughly **70%** of the time
- **Consistency collapse: 60% success on single runs → 25% over 8 consecutive runs**
- Compounding: three agents at 70% each → **34%** end-to-end (0.7³)
- Only **23%** of enterprise agents deployed in 2026 reach production at all
- **88%** of organisations reported confirmed or suspected agent incidents in the past year; 47–53% had incidents where agents exceeded permissions

### The decay law — the single most important research finding

From an empirical study of 10,664 trajectories ([arXiv:2609.01660](https://arxiv.org/abs/2609.01660)):

- Task success follows a **geometric pattern governed by one per-step reliability factor**
- That factor rises with model scale but **saturates well below 1 even for the strongest models, guaranteeing eventual collapse at sufficiently long horizons**
- On agentic tasks, **every model tested falls from near-perfect to near-zero within sixteen steps**
- **Degradation is driven by step count, not context length.** Counterintuitively, *restricting* the context window made decline worse (logit slope −0.69 vs −0.44)
- Reliability drops to **0.42 at GAIA-length horizons and 0.24 at hundred-step production scenarios**
- The authors' own recommendation: *"horizon-aware evaluation and reliability budgeting in place of aggregate pass-rate metrics"*

That sentence is an unbuilt product specification.

### Where agents break — failure taxonomy

From 3,100+ trajectories across four domains ([arXiv:2604.11978](https://arxiv.org/html/2604.11978v1)), human-judge agreement κ=0.84:

Seven failure categories — environment error, instruction error, false assumption, planning error, and three that are **long-horizon-specific**: catastrophic forgetting, history error accumulation, memory limitation.

- Performance degrades **non-linearly**, with sharp cliffs rather than gradual decline
- As horizon grows, **planning and memory failures become dominant**
- **72.5% of failures are process-level**, 27.5% design-level
- Domains break at different horizons: web earliest, embodied steepest, OS and database hold longest
- Conclusion: scaling the base model cannot fix this; architecture must

### The consistency signal — the second key finding

From 1,140 traces across 6 models and 19 tasks ([arXiv:2605.28840](https://arxiv.org/html/2605.28840)):

- **Tool Sequence Similarity (TSS)** — normalised Levenshtein distance over the tool-call sequence. Mean **0.87**
- **Argument Consistency (AC)** — Jaccard similarity over argument key-value pairs. Mean **0.69**
- The gap is significant (Cohen's d=0.75, p<10⁻¹³): agents pick the same *procedure* but vary the *parameters* — "structural consistency, parametric variance"
- **High-TSS runs achieved 90.2% correctness; low-TSS runs achieved 61.2%** (d=0.81, p<0.001)
- Argument variance showed **no** meaningful correlation with success (r=0.12, n.s.)
- **60% of behavioural divergence originates in the first two pipeline steps**
- Ambiguous task specifications cut argument consistency ~28%; task clarity mattered more than model choice

**Read-through:** tool-sequence divergence measured in the *first two steps* is a leading indicator of run failure. That makes a cheap early-abort signal possible — kill a doomed run at step 2 instead of paying for 50 steps.

### Cost

- A 5-step agent loop costs **3.2×** a single chat call; 50 steps exceeds **30×**; 200-step autonomous debugging reaches **100×**
- One developer burned **$4,200 in a weekend** on an autonomous refactoring session
- Median **$480/developer/month**, p90 **$1,650**
- Spend breakdown: **62% re-sent context**, 14% tool definitions, 11% actual reasoning, 8% system prompts, 5% wasted retries
- Controls that work: prompt caching (−88% on system prompts), tier routing (−60–80%), per-user daily caps ($50–100), context pruning (~$0.65/loop)

### The reproducibility problem

When an agent fails in production, teams cannot recreate it. The failure refuses to manifest again.

Bitwise determinism is unattainable with hosted models — floating-point non-associativity, batch-invariance (your output depends on which requests were batched alongside yours), and mixture-of-experts routing contention all break it. Beyond the model, at least eight other things drift between runs: interpolated dates, retrieval results from live indexes, tool responses, model version drift, accumulated history.

The correct framing is **replayability, not determinism**: record the full envelope at the orchestration boundary and replay the frozen run. Critically, some non-determinism is *valuable* — self-consistency sampling improves accuracy 6–17% — so forcing determinism at generation time would destroy quality. Determinism belongs at replay time only.

### Practitioner testimony (LangChain Interrupt 2026)

- Simulated evals scoring **90% offline did not transfer** — real users are not polite, patient simulations
- Adding **20+ tools per domain** caused reasoning degradation and cost explosions (Monday.com, Rippling); the fix was fewer, more generic tools with progressive discovery
- Evals moved from release checkpoints to **continuous gates**
- **Cost became a first-class engineering discipline**, determining profitability
- Consensus: *"how do we operate reliable, observable, governable agent systems at scale?"* replaced building as the core problem

---

## 4. Competitive analysis — why the gap is real

Braintrust is the strongest competitor ($800M valuation). Their own 2026 article on agent reliability tools:

- Reports **task completion rate** as *the* core metric
- Uses the 0.99¹⁰⁰ ≈ 37% example to illustrate compounding failure
- **Does not mention** per-step reliability breakdowns, horizon-aware evaluation, variance across repeated runs, or any probabilistic success model beyond that one example

So the leading vendor has publicly identified the compounding-failure phenomenon and has not productised measuring it.

Independent survey of the space confirms the gap:

> *"No standardized monitoring instrumentation for production meltdown detection beyond research prototypes… Silent failure detection lacks commercially packaged solutions… The tooling exists in fragments; integrated production systems remain custom-built."*

Reliability Decay Curves, graceful-degradation scoring and semantic validation exist as **research concepts**, not shipped products.

### What exists in the adjacent space

| Capability | Who has it | Limitation |
|---|---|---|
| Trace + eval platform | Braintrust, LangSmith, Arize, Galileo | Aggregate metrics only |
| Deterministic replay | Braintrust (Playground), Phoenix, Agenta (partial) | Replay for debugging, not for measuring reliability decay |
| Trace → regression test | Braintrust (automated), Arthur (workflow described) | Still largely manual elsewhere |
| Record/replay OSS | `langchain-replay`, `Agent-Tape`, `vcr-langchain`, `codesweep-ai/vcr` | Small, single-purpose, no measurement layer |
| Reliability SLOs | Future AGI (six SLOs, Apache-2.0 SDK) | SLO definitions, not decay modelling |

The scattered OSS record/replay projects are a **positive** signal: demand is validated, no one owns it, and none couples replay to a measurement theory.

---

## 5. Why not the other two candidates

**MCP security** was the widest-open space and I recommend against it as the primary build, though the data is striking: ~9,400 public MCP servers (38% growth in H1 2026), 30+ CVEs in a 60-day window, 82% path-traversal prevalence across 2,614 deployments, 33% of 1,000 scanned servers with critical vulnerabilities, only 8.5% using OAuth, 492 servers publicly exposed with zero auth (later ~1,467), CoSAI's audit of 17 servers averaging 34/100. Existing scanners run **~78% false positives** because pattern matching cannot distinguish "You MUST call this function first" (normal MCP dependency documentation) from adversarial injection.

The reason to decline: security credibility is reputational and adversarial. A student publishing vulnerability findings against named projects invites conflict, demands disclosure discipline, and is judged by a community that weighs track record heavily. It is a superb *second* project once you have standing.

**Cost control** is genuine (the 62%/100×/$4,200 numbers are real) but LiteLLM has 418M monthly downloads and owns the integration point. You would be building a feature into someone else's distribution.

---

## 6. Sources

- [How Fast Do Agents Rot? (arXiv:2609.01660)](https://arxiv.org/abs/2609.01660)
- [The Long-Horizon Task Mirage? (arXiv:2604.11978)](https://arxiv.org/html/2604.11978v1)
- [How Consistent Are LLM Agents? (arXiv:2605.28840)](https://arxiv.org/html/2605.28840)
- [AI Agent Failure Rate — Fiddler](https://www.fiddler.ai/blog/ai-agent-failure-rate)
- [AI Agent Observability & Governance: 2026 Market Reality](https://guptadeepak.com/ai-agent-observability-evaluation-governance-the-2026-market-reality-check/)
- [AI Agent Infrastructure Startups Landscape 2026 — Presenc](https://presenc.ai/research/ai-agent-infrastructure-startups-2026)
- [Best AI agent reliability tools 2026 — Braintrust](https://www.braintrust.dev/articles/best-ai-agent-reliability-tools-2026)
- [Best AI agent debugging tools 2026 — Braintrust](https://www.braintrust.dev/articles/best-ai-agent-debugging-tools-2026)
- [Long-Horizon Agent Reliability Science: Beyond pass@1 — Zylos](https://zylos.ai/research/2026-06-22-long-horizon-agent-reliability-science/)
- [AI Agent Reliability Metrics 2026: Six SLOs — Future AGI](https://futureagi.com/blog/ai-agent-reliability-metrics-2026/)
- [Your Agent Failed in Prod. Good Luck Reproducing It.](https://dev.to/tisha/your-agent-failed-in-prod-good-luck-reproducing-it-56ci)
- [Deterministic Replay as a Missing Primitive — Sakura Sky](https://www.sakurasky.com/blog/missing-primitives-for-trustworthy-ai-part-8/)
- [Regression Test Datasets from Production Failures — Arthur](https://www.arthur.ai/column/regression-test-datasets-ai-agents-production-failures)
- [Production is the New Prototype — LangChain Interrupt 2026 notes](https://8thlight.com/insights/production-is-the-new-prototype-notes-from-langchain-interrupt-2026)
- [Agentic AI Cost Runaway — LeanOps](https://leanopstech.com/blog/agentic-ai-cost-runaway-token-budget-2026/)
- [Breaking Into AI in 2026: What Anthropic, OpenAI and Meta Actually Hire For](https://dataexec.io/p/breaking-into-ai-in-2026-what-anthropic-openai-and-meta-actually-hire-for)
- [How to Get Hired at OpenAI, Anthropic & DeepMind in 2026 — Sundeep Teki](https://www.sundeepteki.org/advice/how-to-get-hired-at-openai-anthropic-and-google-deepmind-in-2026)
- [MCP Security Statistics 2026 — Practical DevSecOps](https://www.practical-devsecops.com/mcp-security-statistics-2026-report/)
- [MCP Server Security Audit 2026 — AppSec Santa](https://appsecsanta.com/research/mcp-server-security-audit-2026)
- [MCP Ecosystem H1 2026 Retrospective — Digital Applied](https://www.digitalapplied.com/blog/mcp-ecosystem-h1-2026-retrospective-adoption-data-points)
