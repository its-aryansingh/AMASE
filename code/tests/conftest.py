"""Fixture builder producing transcripts in the real Claude Code JSONL shape."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

import pytest


def assistant(
    index: int,
    *,
    tool: str | None = None,
    tool_input: dict[str, Any] | None = None,
    tool_id: str = "t",
    input_tokens: int = 1_000,
    output_tokens: int = 100,
    cache_read: int = 0,
    cache_write: int = 0,
) -> dict[str, Any]:
    content: list[dict[str, Any]] = [{"type": "text", "text": "working"}]
    if tool:
        content.append({"type": "tool_use", "id": tool_id, "name": tool, "input": tool_input or {}})
    return {
        "type": "assistant",
        "uuid": f"a{index}",
        "parentUuid": f"u{index}",
        "timestamp": f"2026-09-17T10:{index:02d}:00Z",
        "sessionId": "s1",
        "message": {
            "content": content,
            "usage": {
                "input_tokens": input_tokens,
                "output_tokens": output_tokens,
                "cache_read_input_tokens": cache_read,
                "cache_creation_input_tokens": cache_write,
            },
        },
    }


def tool_result(
    index: int, *, tool_id: str = "t", text: str = "ok", is_error: bool = False
) -> dict[str, Any]:
    return {
        "type": "user",
        "uuid": f"u{index}",
        "timestamp": f"2026-09-17T10:{index:02d}:30Z",
        "sessionId": "s1",
        "message": {
            "content": [
                {
                    "type": "tool_result",
                    "tool_use_id": tool_id,
                    "is_error": is_error,
                    "content": text,
                }
            ]
        },
    }


def write_jsonl(path: Path, rows: list[dict[str, Any]]) -> Path:
    path.write_text("\n".join(json.dumps(r) for r in rows) + "\n", encoding="utf-8")
    return path


@pytest.fixture
def tmp_transcript(tmp_path: Path):
    def _make(rows: list[dict[str, Any]], name: str = "session.jsonl") -> Path:
        return write_jsonl(tmp_path / name, rows)

    return _make
