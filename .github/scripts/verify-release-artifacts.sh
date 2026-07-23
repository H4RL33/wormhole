#!/bin/sh
set -eu

if test "$#" -ne 1; then
	printf 'usage: %s OUTPUT_DIR\n' "$0" >&2
	exit 2
fi

repo_root=$(cd "$(git rev-parse --show-toplevel)" && pwd -P)
output_arg=$1
case "$output_arg" in
	/*) output_dir=$output_arg ;;
	*) output_dir=$repo_root/$output_arg ;;
esac
if ! test -d "$output_dir" || test -L "$output_dir"; then
	printf 'verify-release: invalid output directory: %s\n' "$output_arg" >&2
	exit 1
fi
output_dir=$(cd "$output_dir" && pwd -P)

set -- "$output_dir"/wormhole-*-linux-amd64.tar.gz
if test "$#" -ne 1 || ! test -f "$1" || test -L "$1"; then
	printf 'verify-release: expected exactly one regular amd64 archive\n' >&2
	exit 1
fi
amd64_archive=$1
version=${amd64_archive##*/wormhole-}
version=${version%-linux-amd64.tar.gz}
case "$version" in
	"" | *[!A-Za-z0-9._-]*)
		printf 'verify-release: invalid archive version %s\n' "$version" >&2
		exit 1
		;;
esac

expected_names="
SHA256SUMS
wormhole-${version}-linux-amd64.tar.gz
wormhole-${version}-linux-arm64.tar.gz
wormhole-amd64.spdx.json
wormhole-arm64.spdx.json
"
entry_count=0
for entry in "$output_dir"/* "$output_dir"/.[!.]* "$output_dir"/..?*; do
	if ! test -e "$entry" && ! test -L "$entry"; then
		continue
	fi
	entry_count=$((entry_count + 1))
	name=${entry##*/}
	if ! printf '%s' "$expected_names" | grep -Fxq "$name"; then
		printf 'verify-release: unexpected output entry %s\n' "$name" >&2
		exit 1
	fi
	if ! test -f "$entry" || test -L "$entry"; then
		printf 'verify-release: %s must be a regular non-symlink file\n' \
			"$name" >&2
		exit 1
	fi
done
if test "$entry_count" -ne 5; then
	printf 'verify-release: expected 5 release files, found %s\n' \
		"$entry_count" >&2
	exit 1
fi

case "$output_dir" in
	"$repo_root"/*) checksum_prefix=${output_dir#"$repo_root"/}/ ;;
	*) checksum_prefix= ;;
esac
if ! awk \
	-v version="$version" \
	-v prefix="$checksum_prefix" \
	'
	BEGIN {
		expected["wormhole-" version "-linux-amd64.tar.gz"] = 1
		expected["wormhole-" version "-linux-arm64.tar.gz"] = 1
		expected["wormhole-amd64.spdx.json"] = 1
		expected["wormhole-arm64.spdx.json"] = 1
	}
	{
		if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-f]/) {
			exit 1
		}
		path = $2
		name = path
		sub(/^.*\//, "", name)
		if (!(name in expected) ||
		    (path != name && path != prefix name) ||
		    ++seen[name] != 1) {
			exit 1
		}
	}
	END {
		if (NR != 4) {
			exit 1
		}
		for (name in expected) {
			if (seen[name] != 1) {
				exit 1
			}
		}
	}
	' "$output_dir/SHA256SUMS"
then
	printf 'verify-release: invalid checksum manifest subjects\n' >&2
	exit 1
fi

checksum_subject=$(awk 'NR == 1 { print $2 }' "$output_dir/SHA256SUMS")
case "$checksum_subject" in
	*/*)
		(
			cd "$repo_root"
			sha256sum -c "$output_dir/SHA256SUMS"
		)
		;;
	*)
		(
			cd "$output_dir"
			sha256sum -c SHA256SUMS
		)
		;;
esac

tmp_dir=$(mktemp -d)
cleanup() {
	if test -d "$tmp_dir" && ! test -L "$tmp_dir"; then
		find -P "$tmp_dir" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

for arch in amd64 arm64; do
	base=wormhole-${version}-linux-${arch}
	archive_name=$base.tar.gz
	archive=$output_dir/$archive_name
	sbom=$output_dir/wormhole-${arch}.spdx.json
	test -f "$archive" && ! test -L "$archive"
	test -f "$sbom" && ! test -L "$sbom"

	actual=$tmp_dir/${arch}.contents
	expected=$tmp_dir/${arch}.expected
	tar -tzf "$archive" | sort >"$actual"
	{
		printf '%s/\n' "$base"
		printf '%s/LICENSE\n' "$base"
		printf '%s/README.md\n' "$base"
		printf '%s/fabric\n' "$base"
		printf '%s/gatewayd\n' "$base"
		printf '%s/wormhole\n' "$base"
	} >"$expected"
	if ! cmp "$expected" "$actual"; then
		printf 'verify-release: unexpected %s archive contents\n' "$arch" >&2
		exit 1
	fi
	if ! tar --numeric-owner -tvzf "$archive" |
		awk '
		$2 != "0/0" { bad = 1 }
		NR == 1 && substr($1, 1, 1) != "d" { bad = 1 }
		NR > 1 && substr($1, 1, 1) != "-" { bad = 1 }
		END { exit bad }
		'
	then
		printf 'verify-release: %s archive type/ownership is not normalized\n' \
			"$arch" >&2
		exit 1
	fi

	extract_dir=$tmp_dir/extract-$arch
	mkdir -p "$extract_dir"
	tar --no-same-owner --no-same-permissions -xzf "$archive" \
		-C "$extract_dir"
	stage=$extract_dir/$base
	test "$(stat -c %a "$stage")" = 755
	test "$(stat -c %a "$stage/LICENSE")" = 644
	test "$(stat -c %a "$stage/README.md")" = 644
	for binary in wormhole gatewayd fabric; do
		test "$(stat -c %a "$stage/$binary")" = 755
	done

	archive_sha=$(sha256sum "$archive" | cut -d' ' -f1)
	if ! jq -e \
		--arg archive "$archive_name" \
		--arg digest "$archive_sha" \
		--arg platform "linux/$arch" \
		'
		.spdxVersion == "SPDX-2.3" and
		.dataLicense == "CC0-1.0" and
		(.creationInfo.creators | index("Tool: syft-1.44.0") != null) and
		(.documentNamespace | contains($digest)) and
		([.files[] | select(.fileName != "") | .fileName] | sort ==
			["LICENSE", "README.md", "fabric", "gatewayd", "wormhole"]) and
		(all(.files[] | select(.fileName != "");
			any(.checksums[]; .algorithm == "SHA256"))) and
		(.packages | length > 5) and
		(any(.packages[]; .name == "github.com/H4RL33/wormhole")) and
		(any(.packages[];
			.packageFileName == $archive and
			any(.checksums[]; .algorithm == "SHA256" and
				.checksumValue == $digest) and
			any(.externalRefs[];
				.referenceCategory == "OTHER" and
				.referenceType == "wormhole-target-platform" and
				.referenceLocator == $platform))) and
		(any(.relationships[]; .relationshipType == "DESCRIBES")) and
		(any(.relationships[]; .relationshipType == "CONTAINS"))
		' "$sbom" >/dev/null
	then
		printf 'verify-release: invalid %s SPDX package/file inventory\n' \
			"$arch" >&2
		exit 1
	fi

	for file in LICENSE README.md fabric gatewayd wormhole; do
		file_sha=$(sha256sum "$stage/$file" | cut -d' ' -f1)
		if ! jq -e --arg file "$file" --arg digest "$file_sha" \
			'any(.files[];
				.fileName == $file and
				any(.checksums[];
					.algorithm == "SHA256" and
					.checksumValue == $digest))' \
			"$sbom" >/dev/null
		then
			printf 'verify-release: %s SBOM has wrong digest for %s\n' \
				"$arch" "$file" >&2
			exit 1
		fi
	done
done
