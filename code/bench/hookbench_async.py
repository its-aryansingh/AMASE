"""Same workload, but Python's fast path: a raw asyncio protocol server.

The first benchmark used ThreadingHTTPServer, which is the slowest reasonable
option. This is the fastest thing Python can do without a C extension: no
framework, no per-request object churn, minimal HTTP parsing. If Go still wins
against this, the gap is real rather than an artefact of a bad baseline.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import socket
import statistics
import threading
import time
import urllib.request


class Features:
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
        return max(
            self.seen[sig] / 3.0,
            self.edits.get(target, 0) / 5.0,
            self.reads.get(target, 0) / 4.0,
            self.error_run / 2.0,
        )


FEATURES = Features()
THRESHOLD = 1.0

_ALLOW = (b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n"
          b"Content-Length: 2\r\n\r\n{}")
_DENY_BODY = (b'{"hookSpecificOutput":{"hookEventName":"PreToolUse",'
              b'"permissionDecision":"deny","permissionDecisionReason":'
              b'"amase: run predicted to fail"}}')
_DENY = (b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "
         + str(len(_DENY_BODY)).encode() + b"\r\n\r\n" + _DENY_BODY)


class HookProtocol(asyncio.Protocol):
    __slots__ = ("transport", "buf")

    def connection_made(self, transport: asyncio.BaseTransport) -> None:
        sock = transport.get_extra_info("socket")
        if sock is not None:
            sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        self.transport = transport
        self.buf = b""

    def data_received(self, data: bytes) -> None:
        self.buf += data
        while True:
            head_end = self.buf.find(b"\r\n\r\n")
            if head_end < 0:
                return
            head = self.buf[:head_end]
            clen = 0
            for line in head.split(b"\r\n"):
                if line[:15].lower() == b"content-length:":
                    clen = int(line[15:])
                    break
            total = head_end + 4 + clen
            if len(self.buf) < total:
                return
            body = self.buf[head_end + 4:total]
            self.buf = self.buf[total:]

            try:
                score = FEATURES.update(json.loads(body) if body else {})
            except Exception:
                score = 0.0
            self.transport.write(_DENY if score >= THRESHOLD else _ALLOW)


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def serve(port: int, ready: threading.Event) -> None:
    async def run() -> None:
        loop = asyncio.get_running_loop()
        server = await loop.create_server(HookProtocol, "127.0.0.1", port)
        ready.set()
        async with server:
            await server.serve_forever()

    asyncio.run(run())


def main(n: int = 3000) -> None:
    port = free_port()
    ready = threading.Event()
    threading.Thread(target=serve, args=(port, ready), daemon=True).start()
    ready.wait(5)
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

    def post() -> None:
        req = urllib.request.Request(url, data=payload,
                                     headers={"Content-Type": "application/json"})
        opener.open(req).read()

    for _ in range(200):
        post()

    samples: list[float] = []
    for _ in range(n):
        t0 = time.perf_counter_ns()
        post()
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


if __name__ == "__main__":
    main()
