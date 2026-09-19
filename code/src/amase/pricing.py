"""Token to money.

Prices are Anthropic list, September 2026, US dollars per million tokens. They
change; `amase waste --price-input/--price-output` overrides them, and the
report always states which table it used so a stale number is visible rather
than silent.
"""

from __future__ import annotations

from dataclasses import dataclass

#: Rupees per US dollar. Indicative only -- used to print a second, more legible
#: figure alongside the dollar one, never to compute anything downstream.
INR_PER_USD = 88.0


@dataclass(frozen=True)
class Price:
    """Per-million-token rates for one model."""

    name: str
    input: float
    output: float
    cache_read: float
    cache_write: float

    def cost_usd(
        self,
        input_tokens: int,
        output_tokens: int,
        cache_read: int = 0,
        cache_write: int = 0,
    ) -> float:
        return (
            input_tokens * self.input
            + output_tokens * self.output
            + cache_read * self.cache_read
            + cache_write * self.cache_write
        ) / 1_000_000


TABLE: dict[str, Price] = {
    "opus": Price("Claude Opus 5", input=5.0, output=25.0, cache_read=0.50, cache_write=6.25),
    "sonnet": Price("Claude Sonnet 5", input=2.0, output=10.0, cache_read=0.20, cache_write=2.50),
    "haiku": Price("Claude Haiku 4.5", input=1.0, output=5.0, cache_read=0.10, cache_write=1.25),
    "fable": Price("Claude Fable 5.1", input=10.0, output=50.0, cache_read=0.25, cache_write=12.50),
}

DEFAULT_MODEL = "sonnet"


def resolve(name: str) -> Price:
    key = name.strip().lower()
    if key in TABLE:
        return TABLE[key]
    raise KeyError(f"unknown model {name!r}; known: {', '.join(sorted(TABLE))}")


def inr(usd: float) -> float:
    return usd * INR_PER_USD
