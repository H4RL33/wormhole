#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
script=$repo_root/.github/scripts/cross-build-release.sh
test -f "$script"

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/wormhole-cross-build-test.XXXXXX")
cleanup() {
	find -P "$tmp_dir" -depth -delete
}
trap cleanup EXIT HUP INT TERM

mkdir "$tmp_dir/bin"
fake_go=$tmp_dir/bin/go
cat >"$fake_go" <<'EOF'
#!/bin/sh
set -eu

output=
package=
while test "$#" -gt 0; do
	case "$1" in
		-o)
			shift
			output=$1
			;;
		./cmd/*)
			package=$1
			;;
	esac
	shift
done

test -n "$output"
test -n "$package"
mkdir -p "$(dirname "$output")"
: >"$output"
printf '%s|%s|%s|%s\n' \
	"${GOOS:?}" "${GOARCH:?}" "$package" "$output" >>"${CROSS_BUILD_LOG:?}"
EOF
chmod +x "$fake_go"

actual=$tmp_dir/actual
expected=$tmp_dir/expected
output_root=$tmp_dir/output
: >"$actual"
: >"$expected"

for arch in amd64 arm64; do
	for binary in wormhole gatewayd fabric; do
		printf 'linux|%s|./cmd/%s|%s/linux-%s/%s\n' \
			"$arch" "$binary" "$output_root" "$arch" "$binary" >>"$expected"
	done
done

PATH=$tmp_dir/bin:$PATH CROSS_BUILD_LOG=$actual \
	sh "$script" "$output_root"
cmp "$expected" "$actual"
