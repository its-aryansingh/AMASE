"""Signal detection. Each test states the behaviour being asserted, not the code path."""

from __future__ import annotations

from conftest import assistant, tool_result

from amase.signals import EDIT_THRESHOLD, READ_THRESHOLD, REPEAT_THRESHOLD, first_signal
from amase.trace import read_entries


def test_clean_run_has_no_signal(tmp_transcript):
    rows = []
    for i in range(6):
        rows.append(assistant(i, tool="Read", tool_input={"file_path": f"src/mod{i}.py"}))
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    assert first_signal(read_entries(path)) is None


def test_identical_call_repeated_is_a_loop(tmp_transcript):
    rows = []
    for i in range(REPEAT_THRESHOLD):
        rows.append(assistant(i, tool="Bash", tool_input={"command": "pytest -q"}))
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    signal = first_signal(read_entries(path))
    assert signal is not None
    assert signal.kind == "repeat_call"
    assert "pytest -q" in signal.detail


def test_argument_order_does_not_defeat_loop_detection(tmp_transcript):
    """Same call, keys written in a different order, is still the same call."""
    rows = []
    variants = [
        {"file_path": "a.py", "old_string": "x", "new_string": "y"},
        {"new_string": "y", "file_path": "a.py", "old_string": "x"},
        {"old_string": "x", "new_string": "y", "file_path": "a.py"},
    ]
    for i, v in enumerate(variants):
        rows.append(assistant(i, tool="Edit", tool_input=v))
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    signal = first_signal(read_entries(path))
    assert signal is not None
    assert signal.kind == "repeat_call"


def test_one_failing_command_is_not_a_signal(tmp_transcript):
    """A single failure is how an agent learns the state of the world."""
    rows = [
        assistant(0, tool="Bash", tool_input={"command": "pytest"}),
        tool_result(0, text="FAILED tests/test_a.py", is_error=True),
        assistant(1, tool="Edit", tool_input={"file_path": "a.py"}),
        tool_result(1, text="ok"),
        assistant(2, tool="Bash", tool_input={"command": "pytest -x"}),
        tool_result(2, text="passed"),
    ]
    path = tmp_transcript(rows)

    assert first_signal(read_entries(path)) is None


def test_consecutive_errors_are_a_signal(tmp_transcript):
    rows = [
        assistant(0, tool="Bash", tool_input={"command": "make"}),
        tool_result(0, text="error: linker failed", is_error=True),
        assistant(1, tool="Bash", tool_input={"command": "make clean"}),
        tool_result(1, text="error: no rule to make target", is_error=True),
    ]
    path = tmp_transcript(rows)

    signal = first_signal(read_entries(path))
    assert signal is not None
    assert signal.kind == "tool_error"


def test_error_run_resets_on_success(tmp_transcript):
    """An error, a fix, then another error is progress, not a doom loop."""
    rows = [
        assistant(0, tool="Bash", tool_input={"command": "a"}),
        tool_result(0, text="error: one", is_error=True),
        assistant(1, tool="Bash", tool_input={"command": "b"}),
        tool_result(1, text="fine"),
        assistant(2, tool="Bash", tool_input={"command": "c"}),
        tool_result(2, text="error: two", is_error=True),
    ]
    path = tmp_transcript(rows)

    assert first_signal(read_entries(path)) is None


def test_error_detected_from_text_without_is_error_flag(tmp_transcript):
    rows = [
        assistant(0, tool="Bash", tool_input={"command": "a"}),
        tool_result(0, text="Traceback (most recent call last):\n  File ..."),
        assistant(1, tool="Bash", tool_input={"command": "b"}),
        tool_result(1, text="Permission denied"),
    ]
    path = tmp_transcript(rows)

    signal = first_signal(read_entries(path))
    assert signal is not None
    assert signal.kind == "tool_error"


def test_edit_thrash_on_one_file(tmp_transcript):
    rows = []
    for i in range(EDIT_THRESHOLD):
        rows.append(
            assistant(i, tool="Edit", tool_input={"file_path": "core.py", "old_string": f"v{i}"})
        )
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    signal = first_signal(read_entries(path))
    assert signal is not None
    assert signal.kind == "edit_thrash"
    assert "core.py" in signal.detail


def test_read_churn_on_one_file(tmp_transcript):
    rows = []
    for i in range(READ_THRESHOLD):
        rows.append(
            assistant(i, tool="Read", tool_input={"file_path": "big.py", "offset": i * 100})
        )
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    signal = first_signal(read_entries(path))
    assert signal is not None
    assert signal.kind == "read_churn"


def test_first_signal_wins_when_several_would_fire(tmp_transcript):
    """The earliest signal defines the cut point; later ones must not override it."""
    rows = [
        assistant(0, tool="Bash", tool_input={"command": "x"}),
        tool_result(0, text="error: a", is_error=True),
        assistant(1, tool="Bash", tool_input={"command": "y"}),
        tool_result(1, text="error: b", is_error=True),
    ]
    for i in range(2, 2 + EDIT_THRESHOLD):
        rows.append(assistant(i, tool="Edit", tool_input={"file_path": "z.py", "n": i}))
        rows.append(tool_result(i))
    path = tmp_transcript(rows)

    signal = first_signal(read_entries(path))
    assert signal is not None
    assert signal.kind == "tool_error"
    assert signal.entry_index <= 3
