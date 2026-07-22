#!/bin/sh
set -eu

profile=${1:?coverage profile required}
exceptions=${2:?coverage exception manifest required}
test -f "$profile"
test -f "$exceptions"

report=$(go tool cover -func="$profile")
printf '%s\n' "$report"
uncovered=$(printf '%s\n' "$report" | awk '$1 != "total:" && $3 != "100.0%" {print}')
total=$(printf '%s\n' "$report" | awk '$1 == "total:" {print $3}')

if [ -n "$uncovered" ] || [ "$total" != "100.0%" ]; then
    printf '%s\n' "coverage gate failed: testable functions/statements must be 100.0%" >&2
    exit 1
fi
