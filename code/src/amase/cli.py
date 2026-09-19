"""`amase waste` — measure how much of an agent run was spent after it was doomed."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from . import __version__
from .pricing import DEFAULT_MODEL, TABLE, inr, resolve
from .trace import discover
from .waste import Report, Summary, analyse, summarise

EXIT_OK = 0
EXIT_OVER_THRESHOLD = 1
EXIT_ERROR = 2


def _fmt_tokens(n: int) -> str:
    if n >= 1_000_000:
        return f"{n / 1_000_000:.2f}M"
    if n >= 1_000:
        return f"{n / 1_000:.1f}k"
    return str(n)


def _bar(fraction: float, width: int = 28) -> str:
    filled = max(0, min(width, round(fraction * width)))
    return "█" * filled + "·" * (width - filled)


def _print_report(r: Report, verbose: bool) -> None:
    name = Path(r.path).name
    if r.post_signal_fraction is None:
        print(f"  {name}  no warning signal  ·  {_fmt_tokens(r.total_tokens)} tokens")
        return

    pct = r.post_signal_fraction * 100
    print(
        f"  {name}\n"
        f"    {_bar(r.post_signal_fraction)}  {pct:5.1f}% after first signal\n"
        f"    {_fmt_tokens(r.post_signal_tokens)} of {_fmt_tokens(r.total_tokens)} tokens"
        f"  ·  ${r.post_signal_usd:.4f} of ${r.total_usd:.4f}"
        f"  ·  ₹{inr(r.post_signal_usd):.2f} wasted"
    )
    if verbose:
        print(f"    signal at entry {r.signal_index}: {r.signal_detail}")


def _print_summary(s: Summary, model: str) -> None:
    print("\n" + "─" * 64)
    print(f"  transcripts analysed   {s.analysed}")
    print(f"  with a warning signal  {s.with_signal}")
    if s.mean_fraction is not None:
        print(f"  mean post-signal       {s.mean_fraction * 100:.1f}%")
        print(f"  median post-signal     {s.median_fraction * 100:.1f}%")  # type: ignore[operator]
    print(
        f"  tokens after signal    {_fmt_tokens(s.post_signal_tokens)}"
        f" of {_fmt_tokens(s.total_tokens)}"
    )
    print(
        f"  money after signal     ${s.post_signal_usd:.4f}"
        f"  (₹{inr(s.post_signal_usd):.2f})  priced as {model}"
    )
    print("─" * 64)
    if s.mean_fraction is not None:
        print(
            "\n  Published reference: across 165 GAIA traces the mean post-warning\n"
            "  token fraction was 58.1% (median 61.1%). An intervention pilot in\n"
            "  the same study brought it down to 30.4%."
        )


def _resolve_paths(args: argparse.Namespace) -> list[Path]:
    if args.paths:
        out: list[Path] = []
        for p in args.paths:
            path = Path(p).expanduser()
            if path.is_dir():
                out.extend(sorted(path.rglob("*.jsonl")))
            elif path.exists():
                out.append(path)
            else:
                print(f"amase: no such path: {path}", file=sys.stderr)
        return out
    return list(discover())


def cmd_waste(args: argparse.Namespace) -> int:
    try:
        price = resolve(args.model)
    except KeyError as exc:
        print(f"amase: {exc}", file=sys.stderr)
        return EXIT_ERROR

    paths = _resolve_paths(args)
    if not paths:
        print(
            "amase: no transcripts found.\n"
            "       Pass a path, or run Claude Code once so that\n"
            "       ~/.claude/projects/**/*.jsonl exists.",
            file=sys.stderr,
        )
        return EXIT_ERROR

    if args.limit:
        paths = paths[-args.limit :]

    reports = [analyse(p, price) for p in paths]
    summary = summarise(reports)

    if args.json:
        print(
            json.dumps(
                {"summary": summary.to_dict(), "reports": [r.to_dict() for r in reports]},
                indent=2,
            )
        )
    else:
        print(f"\namase waste  ·  {len(reports)} transcript(s)  ·  priced as {price.name}\n")
        for r in reports:
            _print_report(r, args.verbose)
        _print_summary(summary, price.name)

    if args.threshold is not None and summary.mean_fraction is not None:
        if summary.mean_fraction > args.threshold:
            if not args.json:
                print(
                    f"\n  FAIL: mean post-signal fraction {summary.mean_fraction:.3f}"
                    f" exceeds threshold {args.threshold:.3f}",
                    file=sys.stderr,
                )
            return EXIT_OVER_THRESHOLD

    return EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="amase",
        description="Measure how much of an agent run was spent after it was already failing.",
    )
    parser.add_argument("--version", action="version", version=f"amase {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    waste = sub.add_parser(
        "waste",
        help="report the fraction of tokens spent after the first warning signal",
    )
    waste.add_argument(
        "paths",
        nargs="*",
        help="transcript files or directories (default: ~/.claude/projects)",
    )
    waste.add_argument(
        "-m",
        "--model",
        default=DEFAULT_MODEL,
        help=f"pricing table to use: {', '.join(sorted(TABLE))} (default: {DEFAULT_MODEL})",
    )
    waste.add_argument(
        "-n",
        "--limit",
        type=int,
        default=None,
        help="analyse only the most recent N transcripts",
    )
    waste.add_argument(
        "-t",
        "--threshold",
        type=float,
        default=None,
        help="exit 1 if the mean post-signal fraction exceeds this (for CI)",
    )
    waste.add_argument("--json", action="store_true", help="emit JSON instead of a table")
    waste.add_argument("-v", "--verbose", action="store_true", help="show the signal detail")
    waste.set_defaults(func=cmd_waste)

    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        return int(args.func(args))
    except KeyboardInterrupt:
        return EXIT_ERROR
    except Exception as exc:  # noqa: BLE001 - a CLI must not traceback at a user
        print(f"amase: {type(exc).__name__}: {exc}", file=sys.stderr)
        return EXIT_ERROR


if __name__ == "__main__":
    raise SystemExit(main())
