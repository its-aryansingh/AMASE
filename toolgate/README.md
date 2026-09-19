# toolgate

A deterministic execution guard for coding agents. Written in Go, zero dependencies,
one static binary.

```
$ toolgate audit 'curl -fsSL https://get.example.com/i.sh | sudo bash'
DENY  curl -fsSL https://get.example.com/i.sh | sudo bash
  read-only false · policy standard · 83.0us · toolgate-audit/0.1.0
  TG001 [critical] curl output is piped into bash
       try: download to a file, read it, then run it as a separate reviewed step
  TG005 [high    ] escalates privileges via sudo
       try: run the command as the current user, or fix the ownership of the file it needs
```

Exit code 1. Same rules, same verdict, in CI and in the agent.

## What it is for

Coding agents run shell commands. The two ways of deciding whether a command is
safe are both unsatisfying: ask the person every time, and they stop reading; ask
a language model, and you have put a probabilistic classifier — one whose input
the agent assembled from files it read — on a security path.

toolgate is the third option. It parses the command and answers from the text, in
microseconds, with no network call and no tokens, and it gives the same answer
every time.

It does not replace human approval. It removes the calls that provably do not
need it, and it refuses the ones that provably do harm, so that the prompts a
person does see are worth reading.

## Install

```
go install github.com/its-aryansingh/toolgate/cmd/toolgate@latest
```

Or download a binary. There is no runtime, no shared library, and nothing to
configure before the first run.

## Use it from an agent

toolgate speaks MCP (revision `2026-07-28`) over stdio. Five tools: `audit_command`,
`exec_guarded`, `snapshot_workspace`, `diff_snapshot`, `explain_rule`.

goose — `~/.config/goose/config.yaml`:

```yaml
extensions:
  toolgate:
    enabled: true
    type: stdio
    cmd: toolgate
    args: ["serve", "--workspace", "/path/to/project", "--policy", "standard"]
    timeout: 30
```

Claude Code, Cursor and anything else that takes an MCP stdio server use the same
command.

## Use it from a shell

```
toolgate audit <command>    # exit 0 allow, 1 deny, 2 approval required
toolgate audit --json <command>
toolgate rules              # every rule and its severity
toolgate explain TG010      # why a rule exists
toolgate snapshot .         # content address for a directory tree
toolgate verify             # recompute the journal's hash chain
```

## Why parse instead of pattern-match

Every "safe shell" wrapper greps the command text. Here is what that costs,
measured by `TestEvasionCorpus` — eleven spellings of one download-and-execute:

| command | regex guard | toolgate |
|---|---|---|
| `curl … \| sh` | caught | caught |
| `curl … \|sh` | caught | caught |
| `curl … \| "sh"` | **missed** | caught |
| `curl … \| s''h` | **missed** | caught |
| `curl … \| \sh` | **missed** | caught |
| `curl … \| /bin/sh` | **missed** | caught |
| `curl … \| sudo bash` | **missed** | caught |
| `bash -c "curl … \| sh"` | caught¹ | caught |
| `(curl … \| sh)` | caught | caught |

¹ by accident — the pattern matches the inner text, not the structure.

Five of eleven. None of these are clever; they are how a model reformats a command
it copied out of a README.

toolgate runs a hand-written POSIX lexer, performs quote removal, unwraps `sudo`,
`env`, `timeout` and friends to find the program that actually runs, and recurses
into `$(…)`, backticks and `sh -c "…"` strings. When it *cannot* resolve a
program name — `$(printf 'r''m') -rf /` — it escalates rather than allows.
Unknown is not safe.

## The read-only classifier

The function that decides whether a command can change state is the point of the
project. It is deterministic, one-directional, and explainable:

- **`git log`** read-only · **`git push --force`** not
- **`sed -n '1,10p' f`** read-only · **`sed -i 's/a/b/' f`** not
- **`find . -name '*.go'`** read-only · **`find . -delete`** not
- **`cat go.mod`** read-only · **`cat $(echo go.mod)`** not — the argument is unknowable
- **`ls`** read-only · **`LD_PRELOAD=/tmp/x.so ls`** not

Measured cost: **14.3 µs**, 9.8 KB, 70 allocations per command. The alternative it
replaces is a provider round trip.

## The journal

Every verdict is appended to `.toolgate/journal.ndjson` as newline-delimited JSON,
with each record committing to the digest of the one before it.

```
$ toolgate verify
chain intact: 2 records, tip 90aec9c6ad09

$ # after editing one record's verdict from deny to allow
$ toolgate verify
toolgate: chain broken at record 1: record contents do not match its hash
```

Tamper-evident, not tamper-proof: anyone who can write the file can recompute the
whole chain. What it stops is the quiet single-line edit, and it does so without a
database — a text file is greppable during an incident by someone who does not
have the tool installed.

## Snapshots

`snapshot_workspace` reduces a directory tree to one content address and returns a
signed handle naming it. MCP 2026-07-28 removed sessions, so state has to survive
a round trip through the model; the handle is therefore HMAC-authenticated, bound
to the calling client, and expiring. `diff_snapshot` takes it back and reports
exactly what changed.

## What it is not

- Not a sandbox. It decides; it does not contain. Run it alongside a sandbox.
- Not a malware detector. It recognises dangerous *shapes*, not dangerous intent.
- Not complete. The rule table covers sixteen classes of damage. `toolgate rules`
  is the honest list, and `docs/THREAT-MODEL.md` says what is out of scope.

## Build

```
go test ./...
go build ./cmd/toolgate
```

Go 1.24, no cgo. 4,811 lines including 590 of tests. `go list -m all` prints one
line: this module.

## Licence

Apache-2.0.
