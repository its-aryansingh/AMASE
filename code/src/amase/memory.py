"""Where AMASE persists traces and calibration state.

The only thing this module is really deciding is *what AMASE is allowed to
require of a user's machine*. The answer is: nothing. ``pip install amase``
installs zero third-party packages, and the default backend here is JSONL plus
the standard library's ``sqlite3``. That is not asceticism for its own sake --
the competitor this project is measured against pulls 138 packages including a
bundled SQLite C amalgamation, and "0 versus 138" is a claim a reader can check
in one command. A dependency added here is paid for by every user forever, so
it has to earn that.

The escape hatch is the protocol. :class:`MemoryBackend` is a
``typing.Protocol``, so it is structural: a backend satisfies it by having the
right methods, without importing anything from AMASE. Heavier substrates --
Oracle AI Agent Memory being the one actually evaluated, see D5 in
``DECISIONS.md`` -- live behind extras and are imported lazily, inside
``__init__`` rather than at module scope, so that the default install path
never touches them even transitively.

What this module deliberately does *not* do: semantic retrieval. ``search`` is
lexical. A vector index would mean numpy at minimum, and the retrieval quality
question is one AMASE intends to *measure* (arm A9) rather than to answer by
picking a favourite. Backends that do semantic retrieval are welcome; they just
are not the default.
"""

from __future__ import annotations

import json
import os
import re
import sqlite3
import threading
from collections.abc import Iterable, Iterator, Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Protocol, runtime_checkable

__all__ = [
    "LocalBackend",
    "MemoryBackend",
    "NullBackend",
    "OracleBackend",
    "SearchHit",
    "default_root",
    "open_backend",
]


# ------------------------------------------------------------------ the shape


@dataclass(frozen=True, slots=True)
class SearchHit:
    """One retrieved event.

    ``score`` is backend-defined and only comparable within a single result
    list. Do not average it across backends; that is exactly the mistake arm A9
    exists to avoid making.
    """

    run_id: str
    seq: int
    score: float
    event: dict[str, Any]


@runtime_checkable
class MemoryBackend(Protocol):
    """Append-only event storage with retrieval.

    Runs are identified by an opaque string. Events are JSON-serialisable
    mappings. Sequence numbers are assigned by the backend, start at 0 within a
    run, and are dense -- ``load`` yields them in order and nothing renumbers.
    """

    def append(self, run_id: str, event: Mapping[str, Any]) -> int:
        """Store one event; return its sequence number within the run."""
        ...

    def load(self, run_id: str) -> Iterator[dict[str, Any]]:
        """Yield every event of a run, in the order it was appended."""
        ...

    def search(self, query: str, k: int = 10) -> list[SearchHit]:
        """Return at most ``k`` events matching ``query``, best first."""
        ...

    def runs(self) -> list[str]:
        """Every run id this backend holds."""
        ...

    def close(self) -> None:
        """Release handles. Calling twice is not an error."""
        ...


# ------------------------------------------------------------------- helpers


def default_root() -> Path:
    """Where ``LocalBackend`` puts things when nobody says otherwise.

    ``AMASE_HOME`` wins, then ``XDG_DATA_HOME``, then ``~/.local/share/amase``.
    Nothing is created until a write happens.
    """
    if env := os.environ.get("AMASE_HOME"):
        return Path(env).expanduser()
    if xdg := os.environ.get("XDG_DATA_HOME"):
        return Path(xdg).expanduser() / "amase"
    return Path.home() / ".local" / "share" / "amase"


_SAFE = re.compile(r"[^A-Za-z0-9._-]")


def _slug(run_id: str) -> str:
    """Filesystem-safe filename for a run id.

    Run ids come from session files and command lines, so they can contain
    separators, ``..``, or nothing printable at all. Replacing the unsafe
    characters alone would collide (``a/b`` and ``a_b`` land on one file), so a
    short digest of the *original* string is appended. The slug is cosmetic;
    the digest is what makes it injective.
    """
    if not run_id:
        raise ValueError("run_id must be a non-empty string")
    import hashlib

    digest = hashlib.blake2b(run_id.encode("utf-8"), digest_size=6).hexdigest()
    stem = _SAFE.sub("_", run_id)[:64].strip("._") or "run"
    return f"{stem}-{digest}"


def _flatten(event: Mapping[str, Any]) -> str:
    """The text ``search`` matches against.

    Every string value in the event, including nested ones, joined by spaces.
    Keys are included too -- ``tool_name`` as a key is a useful thing to be able
    to search for, not only its value.
    """
    out: list[str] = []

    def walk(node: Any) -> None:
        if isinstance(node, str):
            out.append(node)
        elif isinstance(node, Mapping):
            for key, value in node.items():
                out.append(str(key))
                walk(value)
        elif isinstance(node, (list, tuple)):
            for item in node:
                walk(item)
        elif node is not None:
            out.append(str(node))

    walk(event)
    return " ".join(out)


def _fts5_available(conn: sqlite3.Connection) -> bool:
    try:
        conn.execute("CREATE VIRTUAL TABLE temp._fts_probe USING fts5(x)")
        conn.execute("DROP TABLE temp._fts_probe")
        return True
    except sqlite3.OperationalError:
        return False


# -------------------------------------------------------------- the default


class LocalBackend:
    """JSONL append log plus a ``sqlite3`` index. Standard library only.

    Two stores, on purpose. The JSONL files under ``runs/`` are the record of
    truth: append-only, human-readable, greppable, and survivable if the index
    is deleted. The SQLite database is a derived index for ``search``; losing it
    costs a rebuild, not data. That split is why ``load`` reads the file rather
    than the database -- a corrupted index cannot silently truncate a run.

    Retrieval uses FTS5 with BM25 ranking when the interpreter's SQLite was
    built with it (it usually is), and falls back to a ``LIKE`` scan when it was
    not. The fallback is honest but dumb: it ranks by how many query terms
    appear, nothing more. :attr:`ranked` says which one you got, so a
    measurement that depends on retrieval quality can record it rather than
    assume.
    """

    def __init__(self, root: str | os.PathLike[str] | None = None) -> None:
        self.root = Path(root) if root is not None else default_root()
        self._runs_dir = self.root / "runs"
        self._runs_dir.mkdir(parents=True, exist_ok=True)

        self._lock = threading.RLock()
        self._conn = sqlite3.connect(
            self.root / "index.db", check_same_thread=False, isolation_level=None
        )
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute("PRAGMA synchronous=NORMAL")
        self.ranked = _fts5_available(self._conn)
        self._init_schema()
        self._closed = False

    # -- schema ------------------------------------------------------------

    def _init_schema(self) -> None:
        self._conn.execute(
            """
            CREATE TABLE IF NOT EXISTS events (
                run_id TEXT NOT NULL,
                seq    INTEGER NOT NULL,
                body   TEXT NOT NULL,
                text   TEXT NOT NULL,
                PRIMARY KEY (run_id, seq)
            )
            """
        )
        if self.ranked:
            self._conn.execute(
                "CREATE VIRTUAL TABLE IF NOT EXISTS events_fts "
                "USING fts5(text, content='events', content_rowid='rowid')"
            )

    # -- write -------------------------------------------------------------

    def append(self, run_id: str, event: Mapping[str, Any]) -> int:
        path = self._runs_dir / f"{_slug(run_id)}.jsonl"
        line = json.dumps(event, ensure_ascii=False, sort_keys=True, default=str)
        if "\n" in line:  # json.dumps cannot emit one, but a custom default can
            raise ValueError("event serialised to a multi-line string")

        with self._lock:
            self._require_open()
            seq = self._next_seq(run_id)

            # File first. If the process dies between the two writes the JSONL
            # is ahead of the index, which `reindex` repairs; the reverse would
            # be an index entry pointing at an event that does not exist.
            with path.open("a", encoding="utf-8") as handle:
                handle.write(line + "\n")

            self._index(run_id, seq, line, event)
        return seq

    def _next_seq(self, run_id: str) -> int:
        row = self._conn.execute(
            "SELECT COALESCE(MAX(seq), -1) + 1 FROM events WHERE run_id = ?", (run_id,)
        ).fetchone()
        return int(row[0])

    def _index(self, run_id: str, seq: int, line: str, event: Mapping[str, Any]) -> None:
        """Insert one already-serialised event into the search index.

        Split out of :meth:`append` so that :meth:`reindex` can rebuild the index
        without touching the JSONL. An earlier version had reindex call append,
        which appended every event to the file it was reading -- the generator
        then kept finding new lines and the rebuild never terminated. Reading and
        writing one file at once is the kind of bug that only shows up once the
        repair path is exercised, which is why the repair path has a test.
        """
        text = _flatten(event)
        self._conn.execute(
            "INSERT INTO events (run_id, seq, body, text) VALUES (?, ?, ?, ?)",
            (run_id, seq, line, text),
        )
        if self.ranked:
            rowid = self._conn.execute(
                "SELECT rowid FROM events WHERE run_id = ? AND seq = ?", (run_id, seq)
            ).fetchone()[0]
            self._conn.execute("INSERT INTO events_fts (rowid, text) VALUES (?, ?)", (rowid, text))

    def extend(self, run_id: str, events: Iterable[Mapping[str, Any]]) -> int:
        """Append many events; return the number written."""
        count = 0
        for event in events:
            self.append(run_id, event)
            count += 1
        return count

    # -- read --------------------------------------------------------------

    def load(self, run_id: str) -> Iterator[dict[str, Any]]:
        path = self._runs_dir / f"{_slug(run_id)}.jsonl"
        if not path.exists():
            return
        with path.open("r", encoding="utf-8") as handle:
            for line in handle:
                line = line.strip()
                if line:
                    yield json.loads(line)

    def runs(self) -> list[str]:
        with self._lock:
            self._require_open()
            rows = self._conn.execute(
                "SELECT DISTINCT run_id FROM events ORDER BY run_id"
            ).fetchall()
        return [r[0] for r in rows]

    def search(self, query: str, k: int = 10) -> list[SearchHit]:
        query = query.strip()
        if not query or k <= 0:
            return []
        with self._lock:
            self._require_open()
            if self.ranked:
                rows = self._conn.execute(
                    """
                    SELECT e.run_id, e.seq, e.body, bm25(events_fts) AS rank
                    FROM events_fts
                    JOIN events e ON e.rowid = events_fts.rowid
                    WHERE events_fts MATCH ?
                    ORDER BY rank
                    LIMIT ?
                    """,
                    (_fts_query(query), k),
                ).fetchall()
                # bm25() is negative and lower is better; flip it so that
                # "higher score is better" holds across every backend.
                return [
                    SearchHit(run_id=r[0], seq=r[1], score=-float(r[3]), event=json.loads(r[2]))
                    for r in rows
                ]

            terms = [t for t in re.split(r"\W+", query.lower()) if t]
            if not terms:
                return []
            where = " OR ".join("LOWER(text) LIKE ?" for _ in terms)
            rows = self._conn.execute(
                f"SELECT run_id, seq, body, LOWER(text) FROM events WHERE {where}",
                tuple(f"%{t}%" for t in terms),
            ).fetchall()

        scored = [
            SearchHit(
                run_id=r[0],
                seq=r[1],
                score=float(sum(r[3].count(t) for t in terms)),
                event=json.loads(r[2]),
            )
            for r in rows
        ]
        scored.sort(key=lambda h: (-h.score, h.run_id, h.seq))
        return scored[:k]

    # -- maintenance -------------------------------------------------------

    def reindex(self) -> int:
        """Rebuild the index from the JSONL files. Returns events indexed.

        The files are the record of truth, so this is always safe to run and is
        the repair for an index that got out of step with them.
        """
        with self._lock:
            self._require_open()
            self._conn.execute("DELETE FROM events")
            if self.ranked:
                self._conn.execute("DELETE FROM events_fts")

        total = 0
        for path in sorted(self._runs_dir.glob("*.jsonl")):
            run_id = self._recover_run_id(path)
            # Materialise before writing: the file must not be open for reading
            # while the index is being rebuilt from it.
            events = list(self._read_file(path))
            with self._lock:
                for seq, event in enumerate(events):
                    line = json.dumps(event, ensure_ascii=False, sort_keys=True, default=str)
                    self._index(run_id, seq, line, event)
                    total += 1
        return total

    def _recover_run_id(self, path: Path) -> str:
        """Best effort: events carry their own ``run_id`` when written by AMASE."""
        for event in self._read_file(path):
            candidate = event.get("run_id")
            if isinstance(candidate, str) and candidate and _slug(candidate) == path.stem:
                return candidate
            break
        return path.stem

    @staticmethod
    def _read_file(path: Path) -> Iterator[dict[str, Any]]:
        with path.open("r", encoding="utf-8") as handle:
            for line in handle:
                line = line.strip()
                if line:
                    yield json.loads(line)

    # -- lifecycle ---------------------------------------------------------

    def _require_open(self) -> None:
        if self._closed:
            raise RuntimeError("backend is closed")

    def close(self) -> None:
        with self._lock:
            if not self._closed:
                self._conn.close()
                self._closed = True

    def __enter__(self) -> LocalBackend:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def __repr__(self) -> str:
        kind = "fts5" if self.ranked else "like"
        return f"LocalBackend(root={str(self.root)!r}, search={kind})"


def _fts_query(query: str) -> str:
    """Turn user text into an FTS5 MATCH expression.

    FTS5 treats ``-``, ``*``, ``:``, ``"`` and ``NEAR`` as syntax, so a raw path
    or an error message will either fail to parse or match something the user
    did not ask for. Quoting each term makes every token a literal and the
    implicit AND does the rest.
    """
    terms = [t for t in re.split(r"\W+", query) if t]
    return " ".join(f'"{t}"' for t in terms)


# --------------------------------------------------------------- the control


@dataclass
class NullBackend:
    """Stores nothing. This is arm A8's "no memory" control, made explicit.

    Having a real object here rather than ``backend = None`` means the
    no-memory arm runs the same code path as every other arm, so a difference
    in the results is a difference in memory rather than a difference in how
    the harness branched.
    """

    _counts: dict[str, int] = field(default_factory=dict)

    def append(self, run_id: str, event: Mapping[str, Any]) -> int:
        seq = self._counts.get(run_id, 0)
        self._counts[run_id] = seq + 1
        return seq

    def load(self, run_id: str) -> Iterator[dict[str, Any]]:
        return iter(())

    def search(self, query: str, k: int = 10) -> list[SearchHit]:
        return []

    def runs(self) -> list[str]:
        return sorted(self._counts)

    def close(self) -> None:
        return None


# ---------------------------------------------------------------- the extra


class OracleBackend:
    """Oracle AI Database 23ai+ backing store. Requires ``amase[oracle]``.

    Deliberately thin, and deliberately not imported anywhere in the default
    path. See D5 in ``DECISIONS.md`` for why Oracle AI Agent Memory is treated
    as a measurement subject rather than a dependency: the short version is
    that requiring it would take AMASE from zero dependencies to more than the
    competitor it is being compared against, on the one axis where the
    comparison is unarguable.

    ``import oracledb`` happens in ``__init__``, not at module scope, so this
    class can be referenced, subclassed and type-checked without the package
    installed. Only constructing it needs the driver and a reachable database.
    """

    def __init__(self, dsn: str, *, user: str, password: str, table: str = "amase_events") -> None:
        try:
            import oracledb
        except ModuleNotFoundError as exc:  # pragma: no cover - needs the extra
            raise ModuleNotFoundError(
                "OracleBackend needs the optional extra: pip install 'amase[oracle]'. "
                "The default install has no third-party dependencies and keeps it that way."
            ) from exc

        self.dsn = dsn
        self.table = table
        self._conn = oracledb.connect(user=user, password=password, dsn=dsn)
        self._ensure_table()

    def _ensure_table(self) -> None:  # pragma: no cover - needs a database
        ddl = (
            f"CREATE TABLE {self.table} ("
            "  run_id VARCHAR2(200) NOT NULL,"
            "  seq    NUMBER NOT NULL,"
            "  body   CLOB NOT NULL,"
            "  CONSTRAINT pk_%s PRIMARY KEY (run_id, seq))" % self.table
        )
        with self._conn.cursor() as cur:
            try:
                cur.execute(ddl)
            except Exception as exc:  # ORA-00955: name already used
                if "ORA-00955" not in str(exc):
                    raise
        self._conn.commit()

    def append(self, run_id: str, event: Mapping[str, Any]) -> int:  # pragma: no cover
        body = json.dumps(event, ensure_ascii=False, sort_keys=True, default=str)
        with self._conn.cursor() as cur:
            cur.execute(
                f"SELECT NVL(MAX(seq), -1) + 1 FROM {self.table} WHERE run_id = :r",
                r=run_id,
            )
            seq = int(cur.fetchone()[0])
            cur.execute(
                f"INSERT INTO {self.table} (run_id, seq, body) VALUES (:r, :s, :b)",
                r=run_id,
                s=seq,
                b=body,
            )
        self._conn.commit()
        return seq

    def load(self, run_id: str) -> Iterator[dict[str, Any]]:  # pragma: no cover
        with self._conn.cursor() as cur:
            cur.execute(f"SELECT body FROM {self.table} WHERE run_id = :r ORDER BY seq", r=run_id)
            for (body,) in cur:
                yield json.loads(body.read() if hasattr(body, "read") else body)

    def search(self, query: str, k: int = 10) -> list[SearchHit]:  # pragma: no cover
        raise NotImplementedError(
            "OracleBackend.search is intentionally unimplemented. Retrieval quality is "
            "what arm A9 measures; wiring a particular strategy in here would prejudge it."
        )

    def runs(self) -> list[str]:  # pragma: no cover
        with self._conn.cursor() as cur:
            cur.execute(f"SELECT DISTINCT run_id FROM {self.table} ORDER BY run_id")
            return [row[0] for row in cur]

    def close(self) -> None:  # pragma: no cover
        self._conn.close()


# ---------------------------------------------------------------- selection


def open_backend(spec: str | None = None) -> MemoryBackend:
    """Resolve a backend from a short URL-ish string.

    ``None`` or ``"local"`` gives :class:`LocalBackend` at :func:`default_root`;
    ``"local:/path"`` roots it somewhere else; ``"null"`` gives
    :class:`NullBackend`. ``"oracle://user:pass@dsn"`` needs the extra.

    Kept as a function rather than a registry because three backends do not
    need a plugin system, and the protocol means anyone can pass their own
    object instead of a string.
    """
    if spec is None or spec == "local":
        return LocalBackend()
    if spec.startswith("local:"):
        return LocalBackend(spec[len("local:") :])
    if spec == "null":
        return NullBackend()
    if spec.startswith("oracle://"):
        rest = spec[len("oracle://") :]
        if "@" not in rest or ":" not in rest.split("@", 1)[0]:
            raise ValueError("oracle spec must look like oracle://user:password@dsn")
        creds, dsn = rest.split("@", 1)
        user, password = creds.split(":", 1)
        return OracleBackend(dsn, user=user, password=password)
    raise ValueError(f"unknown backend spec: {spec!r}")
