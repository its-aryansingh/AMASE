# Threat model

## What toolgate defends

An agent with shell access, running on a developer's machine or in CI, where the
person is not reading every command. The damage that follows is usually not
malice — it is a correct-looking command with a wrong argument, or a command the
agent copied out of documentation it was shown.

## Adversaries

**A1 — the honest agent with a bad plan.** Most real incidents. `rm -rf` with a
variable that turned out empty, a force-push over a colleague's work, a `kubectl
delete` in the wrong context. Deterministic rules handle this well because the
dangerous shapes are few and well known.

**A2 — text in the repository.** README files, issue comments, dependency
changelogs, test fixtures. The agent reads them and they influence what it does
next. This is the adversary that makes a language-model classifier the wrong tool:
the classifier's input is downstream of the attacker's text. A parser is not
persuadable. It does not matter how the command is justified; `curl | sh` parses
the same either way.

**A3 — a hostile MCP server.** Tool annotations such as `readOnlyHint` are claims
a server makes about itself. goose reads them and skips its judge when they say
read-only. toolgate computes the property from the command text instead, which a
server cannot lie about.

**A4 — a person deliberately bypassing the guard.** Out of scope, and honestly so.
Anyone who can edit `config.yaml` can remove the extension. The journal's hash
chain makes a quiet edit visible after the fact; it does not prevent one.

## Trust boundaries

```
  agent  ──tool call──▶  MCP client  ──stdio──▶  toolgate  ──▶  verdict
    ▲                                               │
    └────────── repository text ────────────────────┘   (A2 enters here)
```

toolgate trusts: the binary, the policy file, the operating system.
toolgate does not trust: the command text, the tool arguments, tool annotations,
the client's self-asserted identity, or a state handle it did not sign.

## Known limits

**Relative paths.** The analyser is told a workspace root but does not know the
process's working directory for an arbitrary command. `cd /etc && rm -rf x` is
judged on `rm -rf x`, which looks local. `exec_guarded` resolves paths against its
own cwd before analysing; `audit_command` cannot, unless the caller passes `cwd`.

**Runtime values.** `rm -rf "$TARGET"` is flagged as unresolvable (TG002/TG003),
not as a root delete, because the value is not in the text. Escalation rather than
a verdict is the right answer and it is also a real loss of precision.

**Shell features not modelled.** Functions, `case`, loop scoping, here-document
bodies, aliases, and `trap`. A command that hides its payload in a shell function
defined earlier in the same string will parse but the rules will see the function
body as ordinary commands — which is usually the desired outcome, though it is not
a guarantee.

**Non-shell tools.** Only shell commands are analysed. A file-write tool, an HTTP
tool, or an editor tool goes through untouched. That is a gap, not a decision;
manifest and file-path analysis is the next rule family.

**Windows.** Rules are written for POSIX shells. PowerShell is recognised as an
interpreter but its syntax is not parsed.

## Failure modes, and which direction they fail

| failure | behaviour | rationale |
|---|---|---|
| toolgate not installed | goose runs as it does today | absence must not break the agent |
| toolgate crashes mid-session | inspector errors, manager logs and continues | `ToolInspectionManager` already works this way |
| command too long to parse | `partial`, never read-only, never allowed | a truncated parse proves nothing |
| journal write fails | verdict still returned, error to stderr | a working guard beats a complete log |
| handle not recognised after restart | "take a new snapshot" | keys are per process by design |

The common thread: every failure degrades toward *more* human involvement or
toward the behaviour that existed before toolgate, never toward silent approval.
