#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
checker=$repo_root/.github/scripts/check-contract-manifest.sh
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wormhole-contract-test.XXXXXX")
cleanup() {
	find -P "$tmp_dir" -depth -delete
}
trap cleanup EXIT HUP INT TERM

fake_bin=$tmp_dir/bin
call_log=$tmp_dir/go-calls
expected=$tmp_dir/expected
output=$tmp_dir/output
mkdir -p "$fake_bin"

cat >"$fake_bin/go" <<'EOF'
#!/bin/sh
set -eu
{
	printf '%s' "$1"
	shift
	for argument in "$@"; do
		printf '\t%s' "$argument"
	done
	printf '\n'
} >>"$CONTRACT_CHECK_CALL_LOG"
printf 'ok\tcontract/fake\t0.001s\n'
EOF
chmod 0755 "$fake_bin/go"

PATH=$fake_bin:$PATH \
	CONTRACT_CHECK_CALL_LOG=$call_log \
	sh "$checker" >"$output"
grep -Fxq 'Contract inventory is deterministic and unchanged.' "$output"

write_expected_calls() {
	for package in \
		./internal/mcp \
		./cmd/wormhole \
		./internal/runtime/sync \
		./internal/runtime/localapi
	do
		printf 'test\t-count=1\t-run\t^TestAlphaContract\t%s\n' \
			"$package"
	done
}

{
	write_expected_calls
	write_expected_calls
} >"$expected"

if ! cmp -s "$expected" "$call_log"; then
	printf '%s\n' 'contract checker did not run the exact focused test set:' >&2
	diff -u "$expected" "$call_log" >&2 || true
	exit 1
fi
