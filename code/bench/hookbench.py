"""Measure the real AMASE guard workload in Python.

Not a framework benchmark. This does exactly what the governor must do on every
tool call: accept a Claude Code hook POST, parse it, update incremental
features, evaluate gates, and answer allow/deny.
"""

from __future__ import annotations

import http.server
import json
import socket
import statistics
import threading
import time
import urllib.request
import hashlib

# ---------------------------------------------------------------- the workload

class Features:
    """Incremental, O(1) per call — no recomputation over history."""

    __slots__ = ("seen", "edits", "reads", "error_run", "steps")

    def __init__(self) -> None:
        self.seen: dict[str, int] = {}
        self.edits: dict[str, int] = {}
        self.reads: dict[str, int] = {}
        self.error_run = 0
        self.steps = 0

    def update(self, payload: dict) -> float:
        self.steps += 1
        name = payload.get("tool_name", "")
        tool_input = payload.get("tool_input", {})

        blob = json.dumps({"n": name, "i": tool_input}, sort_keys=True,
                          separators=(",", ":"), default=str)
        sig = hashlib.blake2b(blob.encode(), digest_size=16).hexdigest()
        self.seen[sig] = self.seen.get(sig, 0) + 1

        target = tool_input.get("file_path") or tool_input.get("command") or ""
        if name in ("Edit", "Write"):
            self.edits[target] = self.edits.get(target, 0) + 1
        elif name == "Read":
            self.reads[target] = self.reads.get(target, 0) + 1

        # gate score: the governor's actual decision arithmetic
        repeat = self.seen[sig] / 3.0
        thrash = self.edits.get(target, 0) / 5.0
        churn = self.reads.get(target, 0) / 4.0
        errs = self.error_run / 2.0
        return max(repeat, thrash, churn, errs)


FEATURES = Features()
THRESHOLD = 1.0


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", 0))
        payload = json.loads(self.rfile.read(length) or b"{}")
        score = FEATURES.update(payload)

        if score >= THRESHOLD:
            body = json.dumps({
                "hookSpecificOutput": {
                    "hookEventName": "PreToolUse",
                    "permissionDecision": "deny",
                    "permissionDecisionReason": "amase: run predicted to fail",
                }
            }).encode()
        else:
            body = b"{}"

        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args) -> None:  # silence
        pass


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def main(n: int = 3000) -> None:
    port = free_port()
    server = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    time.sleep(0.2)

    url = f"http://127.0.0.1:{port}/hooks/pre-tool-use"
    payload = json.dumps({
        "session_id": "bench",
        "hook_event_name": "PreToolUse",
        "tool_name": "Bash",
        "tool_input": {"command": "pytest tests/ -q --tb=short", "timeout": 120000},
        "cwd": "/home/user/project",
        "permission_mode": "default",
    }).encode()

    opener = urllib.request.build_opener()

    # warm up
    for _ in range(200):
        req = urllib.request.Request(url, data=payload,
                                     headers={"Content-Type": "application/json"})
        opener.open(req).read()

    samples: list[float] = []
    for _ in range(n):
        req = urllib.request.Request(url, data=payload,
                                     headers={"Content-Type": "application/json"})
        t0 = time.perf_counter_ns()
        opener.open(req).read()
        samples.append((time.perf_counter_ns() - t0) / 1e6)

    samples.sort()
    def pct(p: float) -> float:
        return samples[min(len(samples) - 1, int(len(samples) * p))]

    print(f"  n            {len(samples)}")
    print(f"  mean         {statistics.fmean(samples):.3f} ms")
    print(f"  p50          {pct(0.50):.3f} ms")
    print(f"  p90          {pct(0.90):.3f} ms")
    print(f"  p99          {pct(0.99):.3f} ms")
    print(f"  p99.9        {pct(0.999):.3f} ms")
    print(f"  max          {samples[-1]:.3f} ms")

    server.shutdown()


if __name__ == "__main__":
    main()
