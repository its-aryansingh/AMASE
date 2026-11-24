# Golden wire traces

Recorded on 2026-11-25 on Windows 10 (x64) from the harness CLIs installed on a
development machine. The protocol adapters are tested against these frames
instead of against anyone's memory of the protocol.

Each line of a `*.ndjson` trace is one frame: `{"dir": "in" | "out", "msg": {...}}`.
`in` is what amase sends to the harness; `out` is what the harness sends back.
Hook files hold one HTTP hook call per line: `{"path", "headers", "payload", "response"}`.

| File | Harness | Scenario |
|---|---|---|
| `claude/duplex-allow-then-hook-deny.ndjson` | Claude Code 2.1.331, `claude-haiku-5-5` | `claude -p` stream-json duplex: `initialize`; one user turn; `can_use_tool` for `echo amase-probe > probe.txt` answered allow with `updatedInput`; `rm probe.txt` denied by the PreToolUse HTTP hook, so no `can_use_tool` is sent for it; `result` with cost and `permission_denials` |
| `claude/http-hooks-allow-then-deny.ndjson` | same run | the four hook calls: PreToolUse (pass-through `{}`), PostToolUse, PreToolUse (deny), Stop |
| `claude/argv.json`, `claude/hook-settings.json` | same run | the exact argv and the `--settings` file that injected the hooks |
| `codex/app-server-handshake-usage-limit.ndjson` | Codex CLI 0.166.0 (`node codex.js app-server`) | `initialize` → `initialized` → `thread/start` (approvalPolicy `untrusted`, sandbox `workspace-write`, ephemeral) → `turn/start`; the turn failed with `usageLimitExceeded` before any approval request. Approval frames are still to be recorded |
| `acp/gemini-permission-allow.ndjson` | Gemini CLI 0.68.0 (`node gemini.js --acp -m gemini-3.5-flash-lite`) | `initialize` → `session/new` → `session/prompt`; a `tool_call` of kind `execute` whose command appears only in `title`; `session/request_permission` answered `proceed_once`; `tool_call_update`; the prompt result with token usage |

Scrubbing: the temporary repo path becomes `C:\work\demo-repo`, the home directory
`C:\Users\dev`, thinking signatures `<signature>`; account rate-limit details are
removed and lists longer than eight entries are cut to three plus a marker. Everything
else is verbatim.

The Codex protocol schema is not vendored; regenerate it for the installed version with
`codex app-server generate-json-schema --out <dir>`.

Re-record these traces whenever a harness is upgraded and a conformance test starts failing.
