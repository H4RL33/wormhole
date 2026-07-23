#!/bin/sh
set -eu

repo_root=$(git rev-parse --show-toplevel)
tmp_dir=$(mktemp -d)
test_root=$repo_root/dist/release/.cleanup-test-$$
cleanup() {
	if test -d "$test_root" && ! test -L "$test_root"; then
		find -P "$test_root" -depth -delete
	fi
	if test -d "$tmp_dir" && ! test -L "$tmp_dir"; then
		find -P "$tmp_dir" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

version=9.8.7-alpha.cleanup
epoch=1700000000
mkdir -p "$test_root"

outside=$tmp_dir/outside
if SOURCE_DATE_EPOCH=$epoch \
	"$repo_root/.github/scripts/build-release.sh" "$version" "$outside"
then
	printf 'build-release accepted output outside dist/release\n' >&2
	exit 1
fi

for traversal in dist/release/.. dist/release/../..; do
	if SOURCE_DATE_EPOCH=$epoch \
		"$repo_root/.github/scripts/build-release.sh" "$version" "$traversal"
	then
		printf 'build-release accepted canonical traversal %s\n' \
			"$traversal" >&2
		exit 1
	fi
done

outside_sentinel=$tmp_dir/outside-sentinel
printf 'keep\n' >"$outside_sentinel"
ln -s "$tmp_dir" "$test_root/symlink-output"
if SOURCE_DATE_EPOCH=$epoch \
	"$repo_root/.github/scripts/build-release.sh" "$version" "$test_root/symlink-output"
then
	printf 'build-release accepted a symlink output directory\n' >&2
	exit 1
fi
grep -qx keep "$outside_sentinel"

if grep -Fq 'rm -rf' "$repo_root/.github/scripts/build-release.sh"; then
	printf 'build-release contains broad recursive deletion\n' >&2
	exit 1
fi

unknown_output=$test_root/unknown-output
mkdir "$unknown_output"
printf 'keep\n' >"$unknown_output/operator-file"
if SOURCE_DATE_EPOCH=$epoch \
	"$repo_root/.github/scripts/build-release.sh" "$version" "$unknown_output"
then
	printf 'build-release accepted an output directory with unknown files\n' >&2
	exit 1
fi
grep -qx keep "$unknown_output/operator-file"

linked_output=$test_root/linked-output
mkdir "$linked_output"
ln -s "$outside_sentinel" \
	"$linked_output/wormhole-${version}-linux-amd64.tar.gz"
if SOURCE_DATE_EPOCH=$epoch \
	"$repo_root/.github/scripts/build-release.sh" "$version" "$linked_output"
then
	printf 'build-release accepted a symlink release artifact\n' >&2
	exit 1
fi
grep -qx keep "$outside_sentinel"
