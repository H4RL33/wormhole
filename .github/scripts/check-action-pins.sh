#!/bin/sh
set -eu
bad=$(
  grep -RhoE 'uses:[[:space:]]+[^[:space:]]+@[^[:space:]#]+' .github/workflows |
  awk -F@ '$2 !~ /^[0-9a-f]{40}$/ { print }'
)
if test -n "$bad"; then
  printf '%s\n' "Actions must be pinned to full commit SHAs:" "$bad" >&2
  exit 1
fi
