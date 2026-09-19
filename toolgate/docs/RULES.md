# Rules

`toolgate rules` prints this table; `toolgate explain <id>` prints one rationale.

| id | severity | what it catches |
|---|---|---|
| TG001 | critical | remote content piped into an interpreter |
| TG002 | high | program name built at runtime, so unknowable |
| TG003 | critical | recursive deletion outside the workspace |
| TG004 | high | access to credential material |
| TG005 | high | privilege escalation or permission widening |
| TG006 | critical | audit or history tampering |
| TG007 | high | destructive version-control operation |
| TG008 | high | local data sent to the network |
| TG009 | critical | write to a startup or hook location |
| TG010 | critical | obfuscated or decoded payload execution |
| TG011 | high | write outside the workspace |
| TG012 | critical | destructive infrastructure operation |
| TG013 | high | download followed by execution |
| TG014 | critical | security control disabled |
| TG015 | critical | raw write to a device |
| TG016 | high | resource exhaustion |

## Policies

| policy | approval at | denial at |
|---|---|---|
| permissive | high | critical |
| standard (default) | medium | critical |
| strict | low | high |

`standard` is tuned so the rules that fire on ordinary development work ask
rather than refuse. A guardrail that blocks `git push` is uninstalled within a
day, and an uninstalled guardrail protects nothing.

## On false positives

TG004 fires on any reference to a credential path, including `cat .env.example`.
TG008 fires on `tar czf - . | curl`, which is sometimes a legitimate deploy.
Both are deliberate: at `standard` these produce an approval prompt, not a
refusal, and an approval prompt for something the person meant to do costs one
keystroke. The reverse error costs the credential.

Report a rule that fires on ordinary work at `standard` as a bug. Report a
command that should have fired and did not as a more interesting bug.
