"""Tests for the pluggable backend, including the one that guards the headline.

``test_default_path_imports_nothing_third_party`` is the load-bearing one. The
zero-dependency claim is the project's strongest structural advantage over the
competitor, and a claim maintained by discipline decays the first time someone is
in a hurry. Here it fails the suite instead.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

import pytest

from amase.memory import (
    LocalBackend,
    MemoryBackend,
    NullBackend,
    OracleBackend,
    SearchHit,
    default_root,
    open_backend,
)

SRC = str(Path(__file__).resolve().parents[1] / "src")


def test_default_path_imports_nothing_third_party() -> None:
    code = (
        f"import sys; sys.path.insert(0, {SRC!r})\n"
        "import amase, amase.memory\n"
        "stdlib = set(sys.stdlib_module_names)\n"
        # sitecustomize/usercustomize are injected by the interpreter's startup,
        # not by anything amase imports, so they are not evidence either way.
        "injected = {'sitecustomize', 'usercustomize'}\n"
        "extra = sorted(\n"
        "    n for n in sys.modules\n"
        "    if not n.startswith(('amase', '_'))\n"
        "    and n.split('.')[0] not in stdlib\n"
        "    and n.split('.')[0] not in injected\n"
        ")\n"
        "print(','.join(extra))\n"
    )
    proc = subprocess.run(
        [sys.executable, "-I", "-c", code],
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, proc.stderr
    assert proc.stdout.strip() == "", (
        f"importing amase pulled in third-party modules: {proc.stdout.strip()}. "
        "See D5 in DECISIONS.md -- the default install has no dependencies."
    )


def test_local_backend_satisfies_the_protocol(tmp_path: Path) -> None:
    with LocalBackend(tmp_path) as backend:
        assert isinstance(backend, MemoryBackend)
        assert isinstance(NullBackend(), MemoryBackend)


def test_append_and_load_preserve_order(tmp_path: Path) -> None:
    with LocalBackend(tmp_path) as backend:
        for i in range(5):
            assert backend.append("run-1", {"seq": i, "tool": "Bash"}) == i
        rows = list(backend.load("run-1"))
    assert [r["seq"] for r in rows] == [0, 1, 2, 3, 4]


def test_runs_are_isolated(tmp_path: Path) -> None:
    with LocalBackend(tmp_path) as backend:
        backend.append("a", {"x": 1})
        backend.append("b", {"x": 2})
        assert backend.runs() == ["a", "b"]
        assert [r["x"] for r in backend.load("a")] == [1]


def test_search_finds_events(tmp_path: Path) -> None:
    with LocalBackend(tmp_path) as backend:
        backend.append("r", {"tool": "Bash", "command": "pytest tests/ -q"})
        backend.append("r", {"tool": "Read", "path": "README.md"})
        hits = backend.search("pytest", k=5)
    assert hits and isinstance(hits[0], SearchHit)
    assert hits[0].event["command"].startswith("pytest")


def test_search_handles_fts_metacharacters(tmp_path: Path) -> None:
    """A path or an error message is full of FTS5 syntax; it must not blow up."""
    with LocalBackend(tmp_path) as backend:
        backend.append("r", {"path": "src/amase/cli.py", "error": "no such option: --verbose"})
        for query in ["src/amase/cli.py", "--verbose", 'no "such" option', "a AND b", "x*"]:
            backend.search(query, k=3)  # must not raise


def test_run_ids_with_separators_do_not_collide(tmp_path: Path) -> None:
    with LocalBackend(tmp_path) as backend:
        backend.append("a/b", {"which": "slash"})
        backend.append("a_b", {"which": "underscore"})
        assert [r["which"] for r in backend.load("a/b")] == ["slash"]
        assert [r["which"] for r in backend.load("a_b")] == ["underscore"]


def test_traversal_in_a_run_id_stays_inside_the_root(tmp_path: Path) -> None:
    with LocalBackend(tmp_path) as backend:
        backend.append("../../escape", {"x": 1})
    written = list((tmp_path / "runs").glob("*.jsonl"))
    assert len(written) == 1
    assert written[0].parent == tmp_path / "runs"


def test_jsonl_is_the_record_of_truth(tmp_path: Path) -> None:
    """Deleting the index must cost a rebuild, not the data."""
    with LocalBackend(tmp_path) as backend:
        for i in range(3):
            backend.append("run-1", {"run_id": "run-1", "seq": i})

    (tmp_path / "index.db").unlink()
    with LocalBackend(tmp_path) as rebuilt:
        assert [r["seq"] for r in rebuilt.load("run-1")] == [0, 1, 2]
        assert rebuilt.search("run-1", k=5) == []
        assert rebuilt.reindex() == 3
        assert rebuilt.runs() == ["run-1"]


def test_load_of_unknown_run_is_empty_not_an_error(tmp_path: Path) -> None:
    with LocalBackend(tmp_path) as backend:
        assert list(backend.load("never-happened")) == []


def test_null_backend_stores_nothing_but_counts(tmp_path: Path) -> None:
    backend = NullBackend()
    assert backend.append("r", {"x": 1}) == 0
    assert backend.append("r", {"x": 2}) == 1
    assert list(backend.load("r")) == []
    assert backend.search("x", k=5) == []
    assert backend.runs() == ["r"]


def test_open_backend_resolves_specs(tmp_path: Path) -> None:
    assert isinstance(open_backend("null"), NullBackend)
    local = open_backend(f"local:{tmp_path}")
    assert isinstance(local, LocalBackend)
    assert local.root == tmp_path
    local.close()

    with pytest.raises(ValueError):
        open_backend("redis://localhost")
    with pytest.raises(ValueError):
        open_backend("oracle://malformed")


def test_oracle_backend_needs_the_extra() -> None:
    """The class is importable without the driver; only constructing it needs one."""
    assert OracleBackend.__name__ == "OracleBackend"
    with pytest.raises(ModuleNotFoundError, match=r"amase\[oracle\]"):
        OracleBackend("dsn", user="u", password="p")


def test_default_root_respects_amase_home(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    monkeypatch.setenv("AMASE_HOME", str(tmp_path / "custom"))
    assert default_root() == tmp_path / "custom"


def test_closed_backend_refuses_writes(tmp_path: Path) -> None:
    backend = LocalBackend(tmp_path)
    backend.close()
    backend.close()  # idempotent
    with pytest.raises(RuntimeError):
        backend.append("r", {"x": 1})
