# AMASE — Research Brief

**Deep research pass on the uploaded strategy report**
Date: 17 September 2026 · Author: research synthesis for Aryan Raj Singh

---

## 0. Summary in one paragraph

The uploaded report is well-written and its *direction* is right — agentic systems, evals, observability, context engineering are genuinely what 2026 hiring screens on. But its **centerpiece recommendation is a year out of date**, two of its four modules are built on assumptions the industry has already abandoned, and its salary figures are inflated by roughly 50–100% at the fresher level. This brief documents what changed, what to keep, and the single research finding that AMASE should be rebuilt around.

---

## 1. Four corrections to the uploaded report

### 1.1 "Build a multi-agent coding workspace" is no longer a differentiated project

The report proposes a Planner / Coder / Reviewer agent that fixes issues in a sandbox. That product category is now crowded and largely commoditised: OpenHands, SWE-agent, Aider, Cline, Cursor, Codex and Claude Code all occupy it, most of them open source.

More decisively, **the benchmark that category was built to win is saturated and contaminated**:

| Benchmark | Status, Sept 2026 |
|---|---|
| SWE-bench Verified | **Retired by OpenAI.** State of the art reported at **97.0%** (Claude Opus 5, Vals.ai measurement) and described by leaderboard maintainers as "mature and heavily exposed in public training data." See the box below. |
| HumanEval / MBPP | Saturated, no signal left. |
| SWE-bench Pro | Live frontier. 1,865 long-horizon tasks, 41 repos, 4 languages, avg 107 LOC across 4.1 files. Top standardised score **45.9%** (Claude Opus 4.5). |
| SWE-rebench | Contamination-resistant, refreshed on a rolling window. Current window: 111 problems / 65 repos. Top score **64.5%**. |
| Terminal-Bench 2.x | Docker-sandboxed, end-state-verified CLI tasks. Adapter-based — **you can plug your own agent in**. |

> **OpenAI's own retirement post is the strongest evidence here — and it is a primary source, so quote it.**
>
> Auditing 138 SWE-bench Verified problems, OpenAI found **59.4% contained material defects**: 35.5% had tests so strict they reject functionally correct submissions, 18.8% tested undocumented behaviour beyond the problem statement, 5.1% other issues.
>
> On contamination, every frontier model tested showed training exposure: **GPT-5.2 reproducing gold patches verbatim, Claude Opus 4.5 recalling inline comments from the original code, Gemini 3 Flash emitting the exact regex from the original fix.** OpenAI now recommends **SWE-bench Pro** and is building privately-authored benchmarks instead.
>
> The transferable lesson for AMASE: *if the frontier labs no longer trust public static benchmarks, a capstone that authors its own held-out set is aligned with where the field actually is.*

**Implication:** an interviewer's first question will be "how is this different from OpenHands?" The report gives no answer to that question. AMASE needs one.

### 1.2 Module 3 (AST chunking → vector DB → hybrid retrieval → cross-encoder, for *code*) is the wrong default

The report presents embedding-based code retrieval as the sophisticated choice. The industry moved the other way. Anthropic's Claude Code team dropped vector-DB RAG for codebases in favour of **agentic search** (grep, glob, file reads, AST tools driven by the model itself). Their stated reasons:

- **Staleness** — indexes decay as code changes daily; continuous re-embedding is real operational cost.
- **Reliability** — code needs *exact* symbol references and call sites, not semantically similar snippets.
- **Simplicity** — no index infrastructure means fewer failure modes.
- **Privacy** — local-first exploration avoids shipping source code to an embedding endpoint.

Agentic search has real weaknesses too — token costs balloon on large repos, and concept-level search is weak when you don't know the symbol names. So the honest 2026 answer is *hybrid, and empirically chosen per repo.*

**Implication:** building the embedding pipeline and calling it state of the art is an interview liability. Building it as **one measured arm of an experiment** — agentic search vs hybrid retrieval, same tasks, same model, published numbers — converts the same work into a defensible result.

### 1.3 The salary figures are inflated

| Claim in report | What the current data actually says |
|---|---|
| Fresher with strong AI portfolio: **₹12–18 LPA** | Freshers (0–2 yrs) **₹6–10 LPA** typical; **₹8–12 LPA** at product companies and AI-first startups; **₹15 LPA+** at FAANG India, largely IIT/NIT pipeline |
| LLM/Agentic AI Engineer entry: **₹15–20+ LPA** | This band exists, but it is the *top decile* of fresher outcomes, not a baseline |
| IT services / GCC | **₹12–28 LPA** is the **mid-level** band at TCS/Infosys/Wipro/HCL/Cognizant/Accenture, not entry |

Skill premiums *are* real and roughly as described: GenAI/LLM **+25–40%**, MLOps **+20–35%**, vector DBs **+15–25%**, cloud **+15–25%**, Docker/K8s **+10–20%**.

**Implication:** treat ₹8–14 LPA as the realistic target band for a strong non-IIT fresher portfolio in NCR/Bengaluru, with ₹18 LPA+ as the stretch outcome that a genuinely differentiated project unlocks. Do not anchor a negotiation on the report's numbers.

### 1.4 Some citations don't hold up

The report's "6.1 lakh active AI roles," "42% YoY growth," "2.3 lakh NASSCOM talent gap" and "67% YoY growth in RAG-listing roles" circulate widely across SEO career-advice sites but I could not trace them to a primary NASSCOM publication. Citation 16 (`arXiv:2606.14502`) did not resolve to a readable paper.

Directionally the scarcity story is supported — one industry source puts AI-engineer demand growth at ~40% YoY against a talent pool growing 15–20%, and projects ~4M AI jobs in India by 2030. **But do not quote the specific lakh-figures in an interview or a README.** Quote the tier-and-premium data instead; it survives scrutiny.

---

## 2. The finding AMASE should be built on

This is the most valuable thing in the entire research pass.

> **LangChain took a fixed model (GPT-5.2-Codex) from 52.8 → 66.5 on Terminal-Bench 2.0 — +13.7 points, from outside the top 30 to top 5 — by changing only the harness.**
>
> **OpenHands, independently: scaffold choice is "the single biggest factor in overall performance, worth 11 to 15 points for recent top models."**

Two independent sources converge on the same number. The harness — the tooling, prompting, context management and control flow *around* the model — is worth more than a full model generation, and it is the part a student can actually build, control and measure.

The specific mechanisms LangChain credited:

1. **Build–verify loop** — explicit plan → build → verify → fix prompting, plus a pre-completion checklist middleware that forces the agent to check its work against the spec before declaring done.
2. **Environment context injection** — map the directory tree and discover available tooling at startup; tell the agent its time budget explicitly.
3. **Doom-loop detection** — track per-file edit counts, intervene when the agent keeps editing the same file.
4. **Reasoning budget allocation** — a "reasoning sandwich": extra-high reasoning for planning, high for implementation, extra-high for final verification. Not max effort everywhere.

And from Anthropic's context-engineering work, four more mechanisms with the same character:

5. **Compaction** — summarise history at the context limit, preserving architectural decisions, open bugs and implementation details while discarding redundant tool output.
6. **Structured note-taking** — external memory files (`NOTES.md`, todo lists) that survive outside the context window.
7. **Sub-agent delegation** — specialist agents with clean context windows returning 1,000–2,000 token summaries to a coordinator. Reported as a substantial gain over single-agent on complex tasks.
8. **Just-in-time retrieval** — keep lightweight identifiers (paths, queries) and load content at runtime rather than pre-loading.

**Nobody is publishing rigorous, cost-annotated ablations of these eight mechanisms.** That gap is the project.

---

## 3. Stack reality check

| Layer | Report says | 2026 reality |
|---|---|---|
| Orchestration | LangGraph / Pydantic AI | LangGraph still the standard for stateful, cyclic, persisted graphs. Microsoft folded AutoGen + Semantic Kernel into **Agent Framework**. CrewAI is prototyping-oriented. OpenAI Agents SDK is minimal-abstraction. Choice is defensible; LangGraph is fine. |
| Sandbox | E2B / Docker | **E2B** = Firecracker microVMs, dedicated kernel per session, session-scoped — right for untrusted LLM code. **Daytona** = Docker containers, persistent, faster cold start — but **went closed-source June 2026**. Managed pricing at scale is non-trivial (~$16.8k for 200 concurrent sandboxes). **Plain Docker locally is the correct choice for a capstone.** |
| Observability | Langfuse / Arize Phoenix / Datadog | Correct, and now underpinned by **OpenTelemetry GenAI semantic conventions**, which standardised LLM/agent tracing. Instrument to OTel conventions and export to Langfuse (MIT core, self-hostable). This is a stronger answer than naming a vendor. |
| Evals | Ragas / Inspect AI | Split the job: **code-first frameworks in CI** (DeepEval — pytest-style assertions; or promptfoo — declarative YAML) answer *"is this version better?"*; **platforms** (Langfuse, Phoenix, LangSmith, Braintrust) answer *"what's happening live?"* Ragas is RAG-metric-specific and only partly applicable here. Pair one of each. |
| Routing | Custom semantic router | Well-grounded. RouteLLM: **~95% of GPT-4 quality at 26% of cost** on MT-Bench; 30–85% savings depending on traffic mix. Critically: **static rules capture 60–70% of available savings at zero training cost**; learned routing adds 15–25% on top. Build rules first, learn second. |

---

## 4. What to keep from the original report

Genuinely correct and worth preserving:

- Evals and observability as the senior-level signal, and their absence as a red flag.
- Context engineering as attention management, and the "big context window = noise channel" framing.
- Cost/latency/quality as the dominant system-design interview axis.
- The RAG-vs-fine-tuning judgement call (RAG for knowledge, fine-tuning for behaviour/format/tone; catastrophic forgetting as the reason).
- KV-cache memory-bound inference, PagedAttention, GQA as required theory.
- Sandboxing LLM-generated code as non-negotiable.
- The interview matrix as a checklist of what the project must produce evidence for.

What to drop: the generative-UI platform, the Slack workspace, the multi-platform bot. They add surface area, not depth.

---

## Sources

- [Why we no longer evaluate SWE-bench Verified — OpenAI](https://openai.com/index/why-we-no-longer-evaluate-swe-bench-verified/) *(primary source)*
- [SWE-bench Verified Leaderboard 2026 — Steel.dev](https://leaderboard.steel.dev/leaderboards/swe-bench-verified/)
- [AI Coding Benchmarks Explained — OpenHands](https://www.openhands.dev/blog/ai-coding-benchmarks-explained)
- [SWE-bench Pro Leaderboard — Morph](https://www.morphllm.com/swe-bench-pro)
- [SWE-rebench Leaderboard](https://swe-rebench.com/)
- [Improving Deep Agents with harness engineering — LangChain](https://www.langchain.com/blog/improving-deep-agents-with-harness-engineering)
- [Effective context engineering for AI agents — Anthropic](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- [Why Claude Code dropped vector-DB RAG — SmartScope](https://smartscope.blog/en/ai-development/practices/rag-debate-agentic-search-code-exploration/)
- [Terminal-Bench guide 2026 — QASkills](https://qaskills.sh/blog/terminal-bench-agent-benchmark-guide-2026)
- [Daytona vs E2B sandboxes — Northflank](https://northflank.com/blog/daytona-vs-e2b-ai-code-execution-sandboxes)
- [LLM Router 2026: RouteLLM benchmarks — Klymentiev](https://klymentiev.com/blog/llm-router)
- [Best LLM & RAG evaluation tools 2026 — AgentsCamp](https://agentscamp.com/guides/evaluation/best-llm-eval-tools-2026)
- [The best AI agent frameworks in 2026 — LangChain](https://www.langchain.com/resources/ai-agent-frameworks)
- [AI Engineer Salary in India 2026 — Taggd](https://taggd.in/blogs/ai-engineer-salary/)
- [Top companies hiring AI engineers in India 2026 — OwnYourCareer](https://www.ownyourcareer.in/blog/top-companies-hiring-ai-engineers-india-2026)
