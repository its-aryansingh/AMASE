# amase

**Measure how much of an agent run was spent after it was already failing.**

```
amase waste  ·  1 transcript(s)  ·  priced as Claude Sonnet 5

  doomed_session.jsonl
    ███████████████████·········   68.4% after first signal
    219.2k of 320.4k tokens  ·  $0.1226 of $0.1815  ·  ₹10.79 wasted
    signal at entry 12: repeated identical tool call — Bash x3 on pytest tests/test_billing.py -q
```

Two-thirds of that session's money was spent after the agent had already started
repeating itself.

---

## Why

When an agent run fails, most of its cost is incurred *after* the failure was
first detectable. Across 165 GAIA traces, among failed runs that emitted a
warning signal, the mean post-warning token fraction was **0.581** and the
median **0.611**. An intervention pilot in the same study brought it down to
**0.304**.

That is the argument for stopping doomed runs early. `amase waste` is the
measurement that tells you whether it applies to *your* runs, on your own
traces, before you change anything.

## Install

```bash
pip install amase
```

No dependencies outside the standard library.

## Use

```bash
# every Claude Code session on this machine
amase waste

# one transcript, with the signal explained
amase waste ~/.claude/projects/my-repo/session.jsonl -v

# the last 20 sessions, priced as Opus
amase waste -n 20 --model opus

# machine-readable
amase waste --json

# fail CI if the average run wastes more than 40% after its first warning
amase waste --threshold 0.40
```

Exit codes: `0` under threshold, `1` over it, `2` on error.

## What counts as a warning signal

All four are computed from the transcript alone — no model internals, no extra
LLM call, no network.

| Signal | Fires when |
|---|---|
| `tool_error` | 2+ consecutive tool results that errored |
| `repeat_call` | the same tool called with the same arguments 3 times |
| `edit_thrash` | 5+ edits to one file |
| `read_churn` | 4+ reads of one file |

The earliest one wins, and everything from that entry onward counts as
post-signal. A single failing command never fires — that is how an agent learns
the state of the world, not a sign of trouble.

## On the token numbers

`usage.input_tokens` on an assistant turn is the size of the **whole context**
sent for that call, not an increment. Summing it across turns is therefore
correct for billing — each API call really is charged for everything it sent —
but wrong if read as "how much new text appeared". This tool only ever uses it
for billing, and counts cache reads and cache writes at their own rates.

Prices are Anthropic list, September 2026. They change; the report always names
the table it used.

## What this is not

It does not decide whether a run failed. It marks the first point at which the
run *looked* like it was in trouble, and measures what was spent after that.
Whether the run then recovered is a separate question, and one this tool
deliberately does not guess at.

## Development

```bash
git clone https://github.com/its-aryansingh/amase && cd amase
pip install -e ".[dev]"
pytest
```

## Licence

Apache-2.0.
