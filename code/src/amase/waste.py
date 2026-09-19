"""The measurement: how much of a run was spent after it was already in trouble.

Published finding this replicates, on your own traces rather than a benchmark:
across 165 GAIA traces, among failed runs that emitted a warning signal, the
mean post-warning token fraction was 0.581 and the median 0.611. An intervention
pilot in the same study cut it to 0.304.

If your own number lands near 0.58, the case for stopping doomed runs is made
with your data. If it lands far away, that is a finding too, and it should be
published rather than buried.
"""

from __future__ import annotations

from dataclasses import dataclass, asdict
from pathlib import Path
from typing import Any

from .pricing import Price, inr
from .signals import Signal, first_signal
from .trace import Entry, Usage, read_entries


@dataclass(frozen=True)
class Report:
    """The result of analysing one transcript."""

    path: str
    entries: int
    assistant_turns: int
    tool_calls: int

    total_tokens: int
    post_signal_tokens: int
    post_signal_fraction: float | None

    total_usd: float
    post_signal_usd: float

    signal_kind: str | None
    signal_index: int | None
    signal_detail: str | None

    model: str

    @property
    def wasted_inr(self) -> float:
        return inr(self.post_signal_usd)

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        d["post_signal_inr"] = round(self.wasted_inr, 4)
        return d


def _usage_cost(usage: Usage, price: Price) -> float:
    return price.cost_usd(
        input_tokens=usage.input_tokens,
        output_tokens=usage.output_tokens,
        cache_read=usage.cache_read_input_tokens,
        cache_write=usage.cache_creation_input_tokens,
    )


def analyse(path: Path, price: Price) -> Report:
    entries: list[Entry] = read_entries(path)
    signal: Signal | None = first_signal(entries)

    total_tokens = 0
    post_tokens = 0
    total_usd = 0.0
    post_usd = 0.0
    assistant_turns = 0
    tool_calls = 0

    cut = signal.entry_index if signal else None

    for entry in entries:
        if entry.type == "assistant":
            assistant_turns += 1
        tool_calls += len(entry.tool_calls)

        tokens = entry.billed_tokens
        cost = _usage_cost(entry.usage, price)
        total_tokens += tokens
        total_usd += cost

        if cut is not None and entry.index >= cut:
            post_tokens += tokens
            post_usd += cost

    fraction = (post_tokens / total_tokens) if (cut is not None and total_tokens) else None

    return Report(
        path=str(path),
        entries=len(entries),
        assistant_turns=assistant_turns,
        tool_calls=tool_calls,
        total_tokens=total_tokens,
        post_signal_tokens=post_tokens,
        post_signal_fraction=round(fraction, 4) if fraction is not None else None,
        total_usd=round(total_usd, 6),
        post_signal_usd=round(post_usd, 6),
        signal_kind=signal.kind if signal else None,
        signal_index=signal.entry_index if signal else None,
        signal_detail=signal.describe() if signal else None,
        model=price.name,
    )


@dataclass(frozen=True)
class Summary:
    """Aggregate across many transcripts."""

    analysed: int
    with_signal: int
    total_tokens: int
    post_signal_tokens: int
    total_usd: float
    post_signal_usd: float
    mean_fraction: float | None
    median_fraction: float | None

    def to_dict(self) -> dict[str, Any]:
        d = asdict(self)
        d["post_signal_inr"] = round(inr(self.post_signal_usd), 2)
        return d


def summarise(reports: list[Report]) -> Summary:
    flagged = [r for r in reports if r.post_signal_fraction is not None]
    fractions = sorted(r.post_signal_fraction for r in flagged)  # type: ignore[misc]

    mean = sum(fractions) / len(fractions) if fractions else None
    if fractions:
        mid = len(fractions) // 2
        median = (
            fractions[mid]
            if len(fractions) % 2
            else (fractions[mid - 1] + fractions[mid]) / 2
        )
    else:
        median = None

    return Summary(
        analysed=len(reports),
        with_signal=len(flagged),
        total_tokens=sum(r.total_tokens for r in reports),
        post_signal_tokens=sum(r.post_signal_tokens for r in reports),
        total_usd=round(sum(r.total_usd for r in reports), 6),
        post_signal_usd=round(sum(r.post_signal_usd for r in reports), 6),
        mean_fraction=round(mean, 4) if mean is not None else None,
        median_fraction=round(median, 4) if median is not None else None,
    )
