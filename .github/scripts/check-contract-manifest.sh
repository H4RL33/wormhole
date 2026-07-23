#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
manifest=$repo_root/docs/contracts/alpha-contract.json
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wormhole-contract.XXXXXX")
cleanup() {
	find -P "$tmp_dir" -depth -delete
}
trap cleanup EXIT HUP INT TERM

manifest_hash() {
	sha256sum "$manifest" | cut -d' ' -f1
}

run_suite() {
	run_name=$1
	normalized=$tmp_dir/$run_name.normalized
	: >"$normalized"
	for package in \
		./internal/mcp \
		./cmd/wormhole \
		./internal/runtime/sync
	do
		raw=$tmp_dir/$run_name.$(printf '%s' "$package" | tr '/.' '__').raw
		if ! go test -count=1 "$package" >"$raw" 2>&1; then
			cat "$raw" >&2
			return 1
		fi
		sed -E 's/[[:space:]]+[0-9]+(\.[0-9]+)?s$/ <elapsed>/' \
			"$raw" >>"$normalized"
	done
}

cd "$repo_root"
before=$(manifest_hash)
run_suite first
after_first=$(manifest_hash)
if test "$before" != "$after_first"; then
	printf '%s\n' "contract tests mutated $manifest" >&2
	exit 1
fi

run_suite second
after_second=$(manifest_hash)
if test "$before" != "$after_second"; then
	printf '%s\n' "contract tests mutated $manifest" >&2
	exit 1
fi

if ! cmp -s "$tmp_dir/first.normalized" "$tmp_dir/second.normalized"; then
	printf '%s\n' "contract test output is not deterministic:" >&2
	diff -u "$tmp_dir/first.normalized" "$tmp_dir/second.normalized" >&2 || true
	exit 1
fi

printf '%s\n' "Contract inventory is deterministic and unchanged."
