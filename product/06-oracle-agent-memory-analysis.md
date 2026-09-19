# Oracle AI Agent Memory vs Velra — and what AMASE should do about it

18 September 2026 · from Oracle's docs, blogs, the arXiv paper, and PyPI metadata read directly

---

## 0. The finding that matters most

Oracle and Velra are solving **overlapping problems with opposite philosophies**, and one of them proved their solution works.

| | Velra continuation capsule | Oracle Agent Memory context card |
|---|---|---|
| How it is built | pure function of a SQLite snapshot | LLM extraction over conversation history |
| Determinism | byte-identical across platforms | non-deterministic by construction |
| Size | bounded, 745-token target, 800 ceiling | unbounded, retrieval-scored |
| Scope | one session, across one `/compact` | cross-session, cross-thread, cross-agent |
| Infrastructure | none — local SQLite file | Oracle AI Database 23ai+ |
| **Proven effective?** | **H1, H2 INCONCLUSIVE** | **LongMemEval 94.4%, 10.7× fewer tokens** |

That last row is the whole story. Velra built an elegant, deterministic, bounded memory artefact — and their own benchmark could not demonstrate it helps, because their control found the baseline never forgot anything. Oracle built a heavyweight, non-deterministic, database-backed memory system — and published a measured efficacy result on a public benchmark.

**Rigour of construction is not the same as evidence of benefit.** Velra has the former; Oracle has the latter. AMASE should aim to have both, and the route to that is in §5.

---

## 1. What Oracle AI Agent Memory actually is

A **database-native memory substrate** built into Oracle AI Database, aimed at long-horizon enterprise agents.

### Architecture

A layered design: an **active memory core** for immediate access, and a **passive memory-store interface** with explicit scope control across users, agents and threads.

The memory lifecycle has **six stages**: ingestion → extraction → consolidation → retrieval → summarization → revision/removal. That last stage matters — most memory systems only add; this one revises and forgets.

### Two pillars

- **Short-term** — thread context cards, conversation summaries, recent turns, task state, intermediate progress during an active session.
- **Long-term** — `add` and `search` workflows over user preferences, learned rules, and facts from earlier sessions.

### Published results

| Benchmark | Score |
|---|---|
| LongMemEval (v26.6) | **94.4%** — 472/500 correct |
| LongMemEval (arXiv paper) | 93.8% |
| BEAM 100K | 68.5% average accuracy over 100K-token histories |
| BEAM 1M | 65.6% average accuracy over 1M-token histories |
| Token efficiency | **10.7× fewer tokens than flat-history baselines** |

Evaluation combines task accuracy with memory-specific metrics: evidence retrieval, recall, latency, estimated token use. That is a more complete methodology than most memory products publish.

### Newer capabilities (26.6)

- **Custom extraction instructions** — domain guidance on what becomes durable memory, with `BACKGROUND` extraction mode and a configurable frequency.
- **Hybrid search** — semantic vector similarity combined with exact text matching, which is the same BM25-plus-dense argument that shows up in code retrieval.
- **TTL with per-record overrides**, metadata filtering with array operators, update APIs for threads/messages/records.
- **Context cards** — summaries plus retrieval topics plus relevant memories, assembled per thread.

### What it requires

This is the decisive practical fact. **Oracle AI Agent Memory is not a pip-installable library.** I checked PyPI: `oracle-ai-agent-memory` does not exist. It is a *database feature*, reached through SDKs:

| Package | Version | Deps |
|---|---|---|
| `langchain-oracledb` | 1.5.0 | 7 |
| `langgraph-oracledb` | 1.0.1 | 3 |
| `langchain-oci` | 0.3.2 | 17 |
| `oracledb` | 26.0.0 | 19 |

Classes: `OracleSemanticCache`, `OracleChatMessageHistory`, `OracleSaver` / `AsyncOracleSaver` (LangGraph checkpointing per `thread_id`), `OracleStore` / `AsyncOracleStore`, `OracleDBMemoryStore`.

Requires **Oracle AI Database 23ai or later** — the version where the `VECTOR` type exists. Importantly, **the free 23ai Docker container supports the documented examples end to end**, so the barrier is a container, not a licence negotiation. Agent Memory proper is documented against 26.4/26.6 and was announced as "expected CY2026."

---

## 2. Where AMASE sits — a different axis entirely

It is worth being precise, because it is easy to assume three products in "agent infrastructure" compete.

| Product | Question it answers |
|---|---|
| Velra | *What survives the compaction boundary?* |
| Oracle Agent Memory | *What should the agent remember, across sessions?* |
| **AMASE** | ***When should this run stop?*** |

Velra and Oracle are both **memory** systems — they differ in scope and philosophy but answer the same class of question. AMASE is a **control** system. It does not decide what to remember; it decides whether to continue.

That orthogonality is good news. It means Oracle is not a competitor, and it means integration is a genuine option rather than a capitulation.

---

## 3. Should AMASE depend on Oracle Agent Memory?

**No. Not as a required dependency, and the reasoning is the same argument that beats Velra.**

D4 established AMASE's strongest structural advantage: **zero runtime dependencies** against Velra's 12 direct, 138 in the lockfile, including a bundled SQLite C amalgamation. "0 versus 138" is legible, verifiable in one command, and unarguable.

Adding Oracle would mean `oracledb` (19 deps) plus `langchain-oracledb` (7) plus a database container. AMASE would go from **0 dependencies to more than Velra has**, discarding the one advantage that is both structural and easy to explain.

Three further reasons:

1. **Wrong user.** AMASE's user is an individual developer running Claude Code on a laptop. They will not provision an Oracle container to find out how many tokens they wasted.
2. **Wrong problem.** Cross-session memory is not the bottleneck AMASE addresses. A doomed run is doomed regardless of what the agent remembers from last week.
3. **LLM in the loop.** Extraction is model-driven, which is non-deterministic and *consumes tokens* — the exact quantity AMASE exists to conserve. Putting it in the hot path would contradict the design.

---

## 4. What to do instead — optional backend behind a protocol

The correct integration is one that costs nothing when unused.

```python
# amase/memory.py — stdlib only, no imports beyond typing
from typing import Protocol, Iterable

class MemoryBackend(Protocol):
    """Where AMASE persists traces and calibration state."""
    def append(self, run_id: str, event: dict) -> None: ...
    def load(self, run_id: str) -> Iterable[dict]: ...
    def search(self, query: str, k: int) -> list[dict]: ...
```

- **Default: `LocalBackend`** — JSONL plus stdlib `sqlite3`. Zero third-party code. This is what `pip install amase` gives you, and it stays that way permanently.
- **Optional: `OracleBackend`** — shipped as an extra, `pip install amase[oracle]`, importing `oracledb` and `langchain-oracledb` only when selected.

This preserves the claim exactly as stated — *AMASE has zero required dependencies* — while allowing the README line *"pluggable backends; Oracle AI Database supported for enterprise deployments."* Enterprises get their checkbox; individual users never pay for it.

`OracleSaver` from `langgraph-oracledb` is genuinely useful here, since it provides per-`thread_id` graph checkpointing, which `amase measure` needs for deterministic replay in the ablation rig. But `sqlite3` provides the same thing locally, so it remains an option rather than a requirement.

---

## 5. The sharper play — Oracle as a measurement target

This is the part worth acting on, and it is where the three projects connect.

**Oracle published their numbers on LongMemEval and BEAM — conversational memory benchmarks. Nobody has measured Oracle Agent Memory on long-horizon coding-agent traces.**

AMASE is the instrument that can. The horizon sweep already fits per-step reliability `p` and produces cost-attributed decay curves. Pointing it at a memory substrate is the same experiment with one more arm.

The result nobody currently has:

> *Deterministic bounded memory (Velra-style capsule) versus LLM-extracted memory (Oracle-style context card) versus no memory, measured on the same coding-agent task set, with per-step reliability, token cost and rupees attached.*

That is a genuinely novel comparison. It is directly on-thesis for the capstone — *"measuring cost–quality trade-offs"* is the submitted title, and memory strategy is exactly such a trade-off. And it does not compromise the architecture, because the systems under test are subjects, not dependencies.

Three practical consequences:

1. **It makes both Velra and Oracle data points in your paper**, rather than competitors to argue with. The same move recommended for Velra earlier, applied to a vendor with a developer-relations team that notices measurement work.
2. **Oracle's 10.7× claim is testable.** If it replicates on coding traces, you have confirmed a vendor claim independently, which is a credible thing to publish. If it does not, that is a more interesting result and you have the methodology to defend it.
3. **It gives the ablation rig a fourth arm for free** — A9, memory strategy — slotting into the existing A1–A8 design with no new infrastructure.

---

## 6. What this does to the Velra scoreboard

One row changes, and it is the row that matters most.

| Parameter | Velra | Oracle | AMASE target |
|---|---|---|---|
| Memory determinism | **byte-identical** | non-deterministic | measured, both arms |
| Memory scope | one compaction boundary | cross-session, cross-agent | out of scope — control, not memory |
| Efficacy evidence | **INCONCLUSIVE ×2** | 94.4% LongMemEval, 10.7× tokens | **3 of 4 hypotheses resolved** |
| Infrastructure required | none | Oracle DB 23ai+ | **none** |
| Dependencies | 12 direct / 138 locked | 19 + 7 + container | **0 required** |

Velra wins determinism and beats Oracle on infrastructure. Oracle wins on proven benefit by a wide margin. **AMASE can take the two columns neither holds together: zero infrastructure *and* demonstrated efficacy** — which is exactly the gap D4 identified, arrived at from a different direction.

---

## 7. Recommendation, in one paragraph

Do not depend on Oracle AI Agent Memory. Do define a `MemoryBackend` protocol now, while the codebase is 1,146 lines and it costs an hour, with a stdlib-only default and `amase[oracle]` as an unused extra — so the enterprise line exists in the README without a single required dependency being added. Then treat Oracle as arm **A9** in the ablation rig and publish the first measurement of LLM-extracted versus deterministic-bounded memory on coding agents. That single experiment answers the question Velra could not, tests a vendor claim nobody has independently checked, and produces a result that belongs in the capstone paper and on Hacker News in the same week.

---

## Sources

- [Oracle Agent Memory as an Enterprise Memory Substrate for Long-Horizon AI Agents (arXiv:2607.13157)](https://arxiv.org/abs/2607.13157)
- [Oracle AI Agent Memory — product page](https://www.oracle.com/database/ai-agent-memory/)
- [About Agent Memory — Oracle docs 26.4](https://docs.oracle.com/en/database/oracle/agent-memory/26.4/agmea/about.html)
- [Introducing Oracle AI Agent Memory — Oracle blog](https://blogs.oracle.com/database/introducing-oracle-ai-agent-memory-a-unified-memory-core-for-enterprise-ai-systems)
- [What's New in Oracle AI Agent Memory: Custom Extraction, Hybrid Search — Oracle blog](https://blogs.oracle.com/developers/whats-new-in-oracle-ai-agent-memory-custom-extraction-hybrid-search-and-more-control)
- [One Database for the Whole LangChain Ecosystem — Oracle blog](https://blogs.oracle.com/developers/one-database-for-the-whole-langchain-ecosystem-memory-persistence-and-deep-agents-on-oracle-ai-database)
- PyPI metadata for `langchain-oracledb`, `langgraph-oracledb`, `langchain-oci`, `oracledb`, read directly
- Velra repository, `BENCHMARK_REPORT.md` and `Cargo.lock`, read from a clone
