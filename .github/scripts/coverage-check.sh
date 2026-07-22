#!/bin/sh
set -eu

profile=${1:?coverage profile required}
exceptions=${2:?coverage exception manifest required}
test -f "$profile"
test -f "$exceptions"

report=$(go tool cover -func="$profile")
printf '%s\n' "$report"
total=$(printf '%s\n' "$report" | awk '$1 == "total:" {print $3}')
total_number=${total%\%}

if ! awk -v total="$total_number" 'BEGIN { exit !(total + 0 >= 90) }'; then
    printf '%s\n' "coverage gate failed: merged statement coverage must be at least 90.0%" >&2
    exit 1
fi
