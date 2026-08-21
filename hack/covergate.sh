#!/usr/bin/env bash
# Fails when a core package drops below the coverage floor.
set -euo pipefail

threshold=85.0
packages=(./internal/k8s ./internal/store ./internal/project ./internal/editor ./internal/diff)

failed=0
for pkg in "${packages[@]}"; do
  line=$(go test -count=1 -cover "$pkg" | tail -n 1)
  percent=$(printf '%s\n' "$line" | sed -n 's/.*coverage: \([0-9.]*\)% of statements.*/\1/p')
  if [ -z "$percent" ]; then
    printf 'covergate: no coverage reported for %s: %s\n' "$pkg" "$line" >&2
    failed=1
    continue
  fi
  if awk -v p="$percent" -v t="$threshold" 'BEGIN { exit (p + 0 >= t + 0) ? 0 : 1 }'; then
    printf 'covergate: %-20s %5s%% >= %s%%\n' "$pkg" "$percent" "$threshold"
  else
    printf 'covergate: %-20s %5s%% <  %s%%  FAIL\n' "$pkg" "$percent" "$threshold" >&2
    failed=1
  fi
done
exit "$failed"
