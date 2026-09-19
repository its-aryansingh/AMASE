"""Observable failure signals.

Everything here is computed from the trace alone -- no model internals, no
extra LLM call. That constraint is the point: the strongest published early-abort
method reads residual-stream hidden states, which no frontier API exposes, so a
method that only needs the transcript is the one that can actually be deployed.

The six signals follow the failure modes reported in the multi-agent
wasted-computation literature: tool errors, repeated action loops, low
information gain, evidence/grounding failures, execution failures, budget waste.
This module implements the four that a Claude Code transcript can support
without guessing.

A signal is *not* a verdict. It marks the first point at which a run looked like
it was in trouble. Measuring how many tokens are spent after that point is the
whole question this tool exists to answer.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Literal

from .trace import Entry

SignalKind = Literal[
    "tool_error",
    "repeat_call",
    "edit_thrash",
    "read_churn",
]

#: How many identical calls before it counts as a loop. Two is a retry; three is
#: a pattern. Set deliberately high enough that an ordinary retry-after-fix does
#: not trip it.
REPEAT_THRESHOLD = 3

#: How many edits to one file before it counts as thrashing. Coding agents
#: legitimately edit a file two or three times; five is a different behaviour.
EDIT_THRESHOLD = 5

#: How many reads of one file before it counts as churn.
READ_THRESHOLD = 4

#: Consecutive erroring tool results before it counts. A single failing command
#: is normal -- that is how an agent learns the state of the world.
ERROR_RUN_THRESHOLD = 2

_EDIT_TOOLS = {"Edit", "Write", "NotebookEdit", "MultiEdit", "str_replace_editor"}
_READ_TOOLS = {"Read", "NotebookRead"}


@dataclass(frozen=True)
class Signal:
    """The first point at which a run looked like it was failing."""

    kind: SignalKind
    entry_index: int
    detail: str

    def describe(self) -> str:
        labels = {
            "tool_error": "consecutive tool errors",
            "repeat_call": "repeated identical tool call",
            "edit_thrash": "repeated edits to one file",
            "read_churn": "repeated reads of one file",
        }
        return f"{labels[self.kind]} — {self.detail}"


def first_signal(entries: list[Entry]) -> Signal | None:
    """Return the earliest warning signal in the trace, or None.

    Ties are broken by entry order, which is what "first" has to mean for the
    post-signal token fraction to be well defined.
    """
    seen_calls: dict[str, int] = {}
    edits: dict[str, int] = {}
    reads: dict[str, int] = {}
    error_run = 0

    for entry in entries:
        for result in entry.tool_results:
            if result.is_error:
                error_run += 1
                if error_run >= ERROR_RUN_THRESHOLD:
                    snippet = result.text.strip().splitlines()
                    head = snippet[0][:120] if snippet else "(no output)"
                    return Signal(
                        kind="tool_error",
                        entry_index=entry.index,
                        detail=f"{error_run} in a row, latest: {head}",
                    )
            else:
                error_run = 0

        for call in entry.tool_calls:
            sig = call.signature
            seen_calls[sig] = seen_calls.get(sig, 0) + 1
            if seen_calls[sig] >= REPEAT_THRESHOLD:
                where = call.target or "(no target)"
                return Signal(
                    kind="repeat_call",
                    entry_index=entry.index,
                    detail=f"{call.name} x{seen_calls[sig]} on {where[:120]}",
                )

            target = call.target
            if not target:
                continue

            if call.name in _EDIT_TOOLS:
                edits[target] = edits.get(target, 0) + 1
                if edits[target] >= EDIT_THRESHOLD:
                    return Signal(
                        kind="edit_thrash",
                        entry_index=entry.index,
                        detail=f"{edits[target]} edits to {target[:120]}",
                    )
            elif call.name in _READ_TOOLS:
                reads[target] = reads.get(target, 0) + 1
                if reads[target] >= READ_THRESHOLD:
                    return Signal(
                        kind="read_churn",
                        entry_index=entry.index,
                        detail=f"{reads[target]} reads of {target[:120]}",
                    )

    return None
