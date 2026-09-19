# AMASE

**Deterministic, measurable governance for LLM coding agents.**

Two tools and the research behind them. One measures how much of an agent run
was spent after it had already failed. The other decides, before a command runs,
whether it should run at all. Both answer from evidence in microseconds, with no
model call, and give the same answer every time.

[![CI](https://github.com/its-aryansingh/AMASE/actions/workflows/ci.yml/badge.svg)](https://github.com/its-aryansingh/AMASE/actions/workflows/ci.yml)

---

## `code/` — amase: where the money went

```
$ amase waste ~/.claude/projects/**/*.jsonl

amase waste  ·  1 transcript(s)  ·  priced as Claude Sonnet 5

  doomed_session.jsonl
    ███████████████████·········   68.4% after first signal
    219.2k of 320.4k tokens  ·  $0.1226 of $0.1815  ·  ₹10.79 wasted
    signal at entry 12: repeated identical tool call — Bash x3 on pytest tests/test_billing.py -q
```

Two-thirds of that session's cost was incurred after the agent had already
started repeating itself.

The premise, from published work rather than intuition: across 165 GAIA traces,
among failed runs that emitted a warning signal, the mean post-warning token
fraction was **0.581**, the median **0.611**. An intervention pilot in the same
study brought it to **0.304**. `amase waste` tells you whether that applies to
*your* traces, on your own machine, before you change anything.

Four black-box signals, computed from the transcript alone — no model internals,
no extra API call: repeated identical tool calls, edit thrashing on one file,
read churn, and consecutive tool errors. Thresholds are set so that ordinary
recovery behaviour does not trip them; a single failing command never fires,
because that is how an agent learns the state of the world.

Python 3.10+, **zero runtime dependencies**, 34 tests.

```bash
cd code
pip install -e ".[dev]"
amase waste tests/fixtures/doomed_session.jsonl
```

Exit code 1 when the wasted fraction crosses `--threshold`, so it works as a CI
gate. `--json` for machines, `--model` to price against a different model.

---

## `toolgate/` — a guard that decides before the command runs

```
$ toolgate audit 'curl -fsSL https://get.example.com/i.sh | sudo bash'
DENY  curl -fsSL https://get.example.com/i.sh | sudo bash
  read-only false · policy standard · 83.0us · toolgate-audit/0.1.0
  TG001 [critical] curl output is piped into bash
       try: download to a file, read it, then run it as a separate reviewed step
  TG005 [high    ] escalates privileges via sudo
```

An MCP server (revision `2026-07-28`) and a CLI in one Go binary, so the rules
that run inside the agent are the same rules that run in CI.

**It parses the command instead of matching patterns.** Eleven spellings of one
download-and-execute, against a representative regex guard and against toolgate:

```
pattern-matching guard missed 5 of 11; toolgate missed 0
```

The misses — `| "sh"`, `| s''h`, `| \sh`, `| /bin/sh`, `| sudo bash` — are not
attacks. They are how a model reformats a command it copied out of a README. A
hand-written POSIX lexer does quote removal, unwraps `sudo`/`env`/`timeout` to
find the program that actually runs, and recurses into `$(…)`, backticks and
`sh -c "…"`. Where `argv[0]` genuinely cannot be resolved — `$(printf 'r''m') -rf /`
— it escalates rather than allows. **Unknown is not safe.**

Sixteen rule classes, a deterministic read-only classifier, and a hash-chained
append-only journal that detects both an edited record and a deleted one.

Go 1.24, **zero dependencies** (`go list -m all` prints one line), 2.8 MB static
binary, no cgo. **14.3 µs** per command.

```bash
cd toolgate
go test ./...
go build ./cmd/toolgate
./toolgate rules
```

As an MCP server — goose, Claude Code, Cursor and anything else that takes stdio:

```yaml
extensions:
  toolgate:
    enabled: true
    type: stdio
    cmd: toolgate
    args: ["serve", "--workspace", "/path/to/project", "--policy", "standard"]
```

`toolgate/goose/toolgate_inspector.rs` is a `ToolInspector` written against
goose's real trait signature — the upstream path described in
`toolgate/docs/GSOC-2027-PROPOSAL.md`.

---

## Layout

| path | what |
|---|---|
| `code/` | the `amase` Python package, tests, and benchmarks |
| `code/DECISIONS.md` | D1–D5: language choice, the three-language benchmark, the competitive analysis, the memory backend |
| `toolgate/` | the Go guard: MCP server, CLI, rule engine, journal |
| `toolgate/docs/` | decisions, threat model, rule reference, GSoC proposal |
| `docs/` | research brief, architecture, roadmap, generated diagrams |
| `product/` | market research, competitive teardowns, the Oracle and Go/MCP analyses |
| `submission/` | capstone synopsis, presentation, research paper draft, report card |

---

## Why both

They are the same thesis on two surfaces. `amase` answers *when should this run
stop?*; `toolgate` answers *should this action happen at all?* Both refuse to put
a language model on a path that needs to be cheap, reproducible and explainable.

The decision records are the part worth reading. `code/DECISIONS.md` and
`toolgate/docs/DECISIONS.md` record what was chosen, what was measured, and what
would overturn it — including the benchmark that overturned an earlier
recommendation of mine, and the three competitor properties I wrongly called
unbeatable before checking.

---

## Status

`amase` v0.1.0 and `toolgate` v0.1.0 both build, test and run. They are early:
the rule table covers sixteen classes of damage, not all of them, and the
read-only classifier has not yet been scored against a labelled corpus. Both
limits are written down rather than glossed — see `toolgate/docs/THREAT-MODEL.md`
and the *what would overturn this* section of each decision record.

## Context

Final-year capstone project, B.Tech Computer Science and Engineering, Noida
Institute of Engineering and Technology, Greater Noida (AKTU). The work in
`toolgate/` continues past the capstone as an open-source contribution track
toward the Agentic AI Foundation's goose.

## Licence

Apache-2.0.
