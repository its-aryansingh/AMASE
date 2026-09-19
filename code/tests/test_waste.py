"""The waste measurement and the CLI contract."""

from __future__ import annotations

import json

from conftest import assistant, tool_result

from amase.cli import main
from amase.pricing import resolve
from amase.trace import read_entries
from amase.waste import analyse, summarise


def test_no_signal_means_no_fraction(tmp_transcript):
    rows = []
    for i in range(4):
        rows.append(assistant(i, tool="Read", tool_input={"file_path": f"f{i}.py"}))
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    report = analyse(path, resolve("sonnet"))
    assert report.post_signal_fraction is None
    assert report.post_signal_tokens == 0
    assert report.total_tokens > 0


def test_fraction_counts_the_signal_turn_and_everything_after(tmp_transcript):
    """Three clean turns, then a loop that burns three more turns of the same size.

    The signal fires on the third identical call, so four of six assistant turns
    sit at or after the cut. Hand-computed, not read off the implementation.
    """
    rows = []
    for i in range(3):
        rows.append(
            assistant(
                i,
                tool="Read",
                tool_input={"file_path": f"f{i}.py"},
                input_tokens=1_000,
                output_tokens=0,
            )
        )
        rows.append(tool_result(i))
    for i in range(3, 6):
        rows.append(
            assistant(
                i,
                tool="Bash",
                tool_input={"command": "pytest -q"},
                input_tokens=1_000,
                output_tokens=0,
            )
        )
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    report = analyse(path, resolve("sonnet"))
    assert report.signal_kind == "repeat_call"
    # 6 assistant turns x 1000 tokens
    assert report.total_tokens == 6_000
    # the loop's third call is the signal; that turn and the rest are post-signal
    assert report.post_signal_tokens == 1_000
    assert report.post_signal_fraction == round(1_000 / 6_000, 4)


def test_cost_uses_every_token_class(tmp_transcript):
    rows = [
        assistant(
            0,
            tool="Read",
            tool_input={"file_path": "a.py"},
            input_tokens=1_000_000,
            output_tokens=1_000_000,
            cache_read=1_000_000,
            cache_write=1_000_000,
        ),
        tool_result(0),
    ]
    path = tmp_transcript(rows)

    report = analyse(path, resolve("sonnet"))
    # Sonnet 5: 2 in, 10 out, 0.20 cache read, 2.50 cache write, per million
    assert report.total_usd == round(2.0 + 10.0 + 0.20 + 2.50, 6)


def test_malformed_lines_are_skipped_not_fatal(tmp_path):
    path = tmp_path / "broken.jsonl"
    good = json.dumps(assistant(0, tool="Read", tool_input={"file_path": "a.py"}))
    path.write_text(good + "\n{not json\n\n" + good + "\n", encoding="utf-8")

    entries = read_entries(path)
    assert len(entries) == 2


def test_truncated_final_line_is_tolerated(tmp_path):
    """A live session has no final line. Refusing to read it would be useless."""
    path = tmp_path / "live.jsonl"
    good = json.dumps(assistant(0, tool="Read", tool_input={"file_path": "a.py"}))
    path.write_text(good + '\n{"type":"assistant","message":{"con', encoding="utf-8")

    assert len(read_entries(path)) == 1


def test_summary_mean_and_median(tmp_transcript):
    def looping(name: str, clean: int, loop: int):
        rows = []
        for i in range(clean):
            rows.append(
                assistant(
                    i,
                    tool="Read",
                    tool_input={"file_path": f"{name}{i}.py"},
                    input_tokens=1_000,
                    output_tokens=0,
                )
            )
            rows.append(tool_result(i))
        for i in range(clean, clean + loop):
            rows.append(
                assistant(
                    i,
                    tool="Bash",
                    tool_input={"command": f"run {name}"},
                    input_tokens=1_000,
                    output_tokens=0,
                )
            )
            rows.append(tool_result(i))
        return tmp_transcript(rows, name=f"{name}.jsonl")

    price = resolve("sonnet")
    reports = [analyse(looping("a", 1, 5), price), analyse(looping("b", 5, 5), price)]
    summary = summarise(reports)

    assert summary.analysed == 2
    assert summary.with_signal == 2
    assert summary.mean_fraction is not None
    assert 0.0 < summary.mean_fraction < 1.0


def test_cli_json_output_and_exit_codes(tmp_transcript, capsys):
    rows = []
    for i in range(3):
        rows.append(
            assistant(
                i,
                tool="Bash",
                tool_input={"command": "pytest"},
                input_tokens=1_000,
                output_tokens=0,
            )
        )
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    assert main(["waste", str(path), "--json"]) == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["summary"]["analysed"] == 1
    assert payload["reports"][0]["signal_kind"] == "repeat_call"

    # threshold below the measured fraction must fail, for CI use
    assert main(["waste", str(path), "--json", "--threshold", "0.01"]) == 1
    # threshold above it must pass
    assert main(["waste", str(path), "--json", "--threshold", "0.99"]) == 0


def test_cli_missing_path_is_an_error_not_a_traceback(capsys):
    assert main(["waste", "/nonexistent/path/x.jsonl"]) == 2
    assert "amase:" in capsys.readouterr().err


def test_cli_unknown_model_is_an_error(tmp_transcript, capsys):
    path = tmp_transcript([assistant(0, tool="Read", tool_input={"file_path": "a.py"})])
    assert main(["waste", str(path), "--model", "gpt-9"]) == 2
    assert "unknown model" in capsys.readouterr().err
