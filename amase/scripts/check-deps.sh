#!/usr/bin/env bash
# Fails if the amase module gains a dependency that is not on the allowlist.
# A new entry here is a decision, not a convenience: record why in the
# project's decision log before adding one.
set -euo pipefail
cd "$(dirname "$0")/.."

allow=(
  "github.com/its-aryansingh/amase"
  "github.com/its-aryansingh/toolgate"
)

bad=0
while read -r mod _; do
  ok=0
  for a in "${allow[@]}"; do
    if [ "$mod" = "$a" ]; then ok=1; fi
  done
  if [ "$ok" -eq 0 ]; then
    echo "dependency not on the allowlist: $mod"
    bad=1
  fi
done < <(go list -m all)

if [ "$bad" -eq 0 ]; then echo "dependencies: allowlist only"; fi
exit "$bad"
