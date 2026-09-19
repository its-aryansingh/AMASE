"""Parsing for Claude Code JSONL session transcripts.

The format is one JSON object per line. Every entry carries ``type``, ``uuid``,
``parentUuid``, ``timestamp`` and ``sessionId``. Assistant entries carry
``message.content`` (a list of ``text`` / ``thinking`` / ``tool_use`` blocks)
and ``message.usage``. User entries carry either a string prompt or a list of
blocks including ``tool_result``.

One gotcha matters enough to state here: ``usage.input_tokens`` on an assistant
turn is the size of the *whole* context sent for that call, not an increment.
Summing it across turns is therefore correct for **billing** -- each API call
really is charged for everything it sent -- but wrong if read as "how much new
text appeared". This module only ever uses it for billing.
"""

from __future__ import annotations

import hashlib
import json
from collections.abc import Iterator
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Literal


@dataclass(frozen=True)
class Usage:
    """Token accounting for a single assistant turn."""

    input_tokens: int = 0
    output_tokens: int = 0
    cache_creation_input_tokens: int = 0
    cache_read_input_tokens: int = 0

    @property
    def total(self) -> int:
        """Every token the provider was asked to handle for this call."""
        return (
            self.input_tokens
            + self.output_tokens
            + self.cache_creation_input_tokens
            + self.cache_read_input_tokens
        )

    @classmethod
    def parse(cls, raw: Any) -> Usage:
        if not isinstance(raw, dict):
            return cls()

        def n(key: str) -> int:
            v = raw.get(key, 0)
            return v if isinstance(v, int) and v >= 0 else 0

        return cls(
            input_tokens=n("input_tokens"),
            output_tokens=n("output_tokens"),
            cache_creation_input_tokens=n("cache_creation_input_tokens"),
            cache_read_input_tokens=n("cache_read_input_tokens"),
        )


@dataclass(frozen=True)
class ToolCall:
    """A ``tool_use`` block issued by the assistant."""

    id: str
    name: str
    input: dict[str, Any] = field(default_factory=dict)

    @property
    def signature(self) -> str:
        """Stable hash of (tool, arguments).

        Two calls with the same signature are the same request. This is what
        loop detection compares, so it must be order-insensitive over dict keys
        and must not vary run to run.
        """
        payload = json.dumps(
            {"name": self.name, "input": self.input},
            sort_keys=True,
            separators=(",", ":"),
            default=str,
        )
        return hashlib.blake2b(payload.encode("utf-8"), digest_size=16).hexdigest()

    @property
    def target(self) -> str | None:
        """The file or command this call acts on, when there is one."""
        for key in ("file_path", "path", "notebook_path", "filePath"):
            v = self.input.get(key)
            if isinstance(v, str) and v:
                return v
        v = self.input.get("command")
        if isinstance(v, str) and v:
            return v.strip()
        return None


@dataclass(frozen=True)
class ToolResult:
    """A ``tool_result`` block returned to the assistant."""

    tool_use_id: str
    is_error: bool
    text: str


@dataclass
class Entry:
    """One line of the transcript, normalised."""

    index: int
    type: Literal["user", "assistant", "system", "other"]
    uuid: str | None
    timestamp: str | None
    usage: Usage
    tool_calls: list[ToolCall] = field(default_factory=list)
    tool_results: list[ToolResult] = field(default_factory=list)

    @property
    def billed_tokens(self) -> int:
        return self.usage.total


_ERROR_MARKERS = (
    "error:",
    "traceback (most recent call last)",
    "command failed",
    "no such file or directory",
    "permission denied",
    "exit code 1",
    "exception:",
    "fatal:",
    "failed with",
)


def _blocks(content: Any) -> list[dict[str, Any]]:
    if isinstance(content, list):
        return [b for b in content if isinstance(b, dict)]
    return []


def _result_text(block: dict[str, Any]) -> str:
    content = block.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts = [
            b.get("text", "")
            for b in content
            if isinstance(b, dict) and isinstance(b.get("text"), str)
        ]
        return "\n".join(parts)
    return ""


def parse_entry(raw: dict[str, Any], index: int) -> Entry:
    etype = raw.get("type")
    if etype not in ("user", "assistant", "system"):
        etype = "other"

    message = raw.get("message")
    message = message if isinstance(message, dict) else {}

    calls: list[ToolCall] = []
    results: list[ToolResult] = []

    for block in _blocks(message.get("content")):
        kind = block.get("type")
        if kind == "tool_use":
            calls.append(
                ToolCall(
                    id=str(block.get("id", "")),
                    name=str(block.get("name", "")),
                    input=block.get("input") if isinstance(block.get("input"), dict) else {},
                )
            )
        elif kind == "tool_result":
            text = _result_text(block)
            flagged = bool(block.get("is_error"))
            if not flagged:
                lowered = text.lower()
                flagged = any(marker in lowered for marker in _ERROR_MARKERS)
            results.append(
                ToolResult(
                    tool_use_id=str(block.get("tool_use_id", "")),
                    is_error=flagged,
                    text=text,
                )
            )

    return Entry(
        index=index,
        type=etype,  # type: ignore[arg-type]
        uuid=raw.get("uuid") if isinstance(raw.get("uuid"), str) else None,
        timestamp=raw.get("timestamp") if isinstance(raw.get("timestamp"), str) else None,
        usage=Usage.parse(message.get("usage")),
        tool_calls=calls,
        tool_results=results,
    )


def read_entries(path: Path) -> list[Entry]:
    """Read a transcript, skipping malformed lines rather than failing.

    A transcript truncated mid-write is normal -- Claude Code appends as it
    goes, and a session that is still open has no final line. Refusing to
    analyse those would make the tool useless on live sessions.
    """
    entries: list[Entry] = []
    with path.open("r", encoding="utf-8", errors="replace") as fh:
        for i, line in enumerate(fh):
            line = line.strip()
            if not line:
                continue
            try:
                raw = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(raw, dict):
                entries.append(parse_entry(raw, i))
    return entries


def discover(root: Path | None = None) -> Iterator[Path]:
    """Yield Claude Code transcripts under the default project directory."""
    base = root or (Path.home() / ".claude" / "projects")
    if not base.exists():
        return
    yield from sorted(base.rglob("*.jsonl"))
