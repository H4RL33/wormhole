#!/bin/sh
set -eu
umask 022

release_arches='amd64 arm64'
release_binaries='wormhole gatewayd fabric'

validate_version() {
	case "$1" in
		"" | *[!A-Za-z0-9._-]*)
			printf 'build-release: invalid version %s\n' "$1" >&2
			exit 2
			;;
	esac
	if test "${#1}" -gt 64; then
		printf 'build-release: version must not exceed 64 characters\n' >&2
		exit 2
	fi
}

release_archive_name() {
	printf 'wormhole-%s-linux-%s.tar.gz\n' "$1" "$2"
}

release_spdx_name() {
	printf 'wormhole-%s.spdx.json\n' "$1"
}

release_checksum_subject_names() {
	for contract_arch in $release_arches; do
		release_archive_name "$1" "$contract_arch"
	done
	for contract_arch in $release_arches; do
		release_spdx_name "$contract_arch"
	done
}

release_artifact_names() {
	printf '%s\n' SHA256SUMS
	release_checksum_subject_names "$1"
}

print_release_contract() {
	for contract_binary in $release_binaries; do
		printf 'binary\t%s\n' "$contract_binary"
	done
	for contract_arch in $release_arches; do
		printf 'platform\tlinux/%s\n' "$contract_arch"
		printf 'archive\t%s\n' \
			"$(release_archive_name "$1" "$contract_arch")"
		printf 'spdx_sbom\t%s\n' \
			"$(release_spdx_name "$contract_arch")"
	done
	printf 'checksum\tSHA256SUMS\n'
}

if test "$#" -eq 2 && test "$1" = "--print-contract"; then
	version=$2
	validate_version "$version"
	print_release_contract "$version"
	exit 0
fi

if test "$#" -ne 2; then
	printf 'usage: %s VERSION OUTPUT_DIR\n' "$0" >&2
	printf '       %s --print-contract VERSION\n' "$0" >&2
	exit 2
fi

version=$1
output_arg=$2
epoch=${SOURCE_DATE_EPOCH:-}

validate_version "$version"
case "$epoch" in
	"" | *[!0-9]*)
		printf 'build-release: SOURCE_DATE_EPOCH must be a non-negative integer\n' >&2
		exit 2
		;;
esac

for command in git realpath go tar gzip sha256sum jq; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf 'build-release: %s is required\n' "$command" >&2
		exit 1
	fi
done

repo_root=$(cd "$(git rev-parse --show-toplevel)" && pwd -P)
release_root=$repo_root/dist/release
lexical_release_root=$(realpath -ms "$release_root")
canonical_release_root=$(realpath -m "$release_root")
if test "$lexical_release_root" != "$release_root" ||
	test "$canonical_release_root" != "$release_root"
then
	printf 'build-release: dedicated release directory contains a symlink\n' >&2
	exit 2
fi
mkdir -p "$release_root"
chmod 0755 "$release_root"

case "$output_arg" in
	/*) output_candidate=$output_arg ;;
	*) output_candidate=$repo_root/$output_arg ;;
esac
lexical_output=$(realpath -ms "$output_candidate")
canonical_output=$(realpath -m "$output_candidate")
if test "$lexical_output" != "$canonical_output"; then
	printf 'build-release: output path must not contain symlinks: %s\n' \
		"$output_arg" >&2
	exit 2
fi
case "$canonical_output" in
	"$release_root" | "$release_root"/*) ;;
	*)
		printf 'build-release: output must be contained in %s: %s\n' \
			"$release_root" "$output_arg" >&2
		exit 2
		;;
esac
if test -L "$output_candidate"; then
	printf 'build-release: output directory must not be a symlink\n' >&2
	exit 2
fi
mkdir -p "$canonical_output"
output_dir=$(cd "$canonical_output" && pwd -P)
chmod 0755 "$output_dir"

known_names=$(release_artifact_names "$version")
for entry in "$output_dir"/* "$output_dir"/.[!.]* "$output_dir"/..?*; do
	if ! test -e "$entry" && ! test -L "$entry"; then
		continue
	fi
	name=${entry##*/}
	if ! printf '%s' "$known_names" | grep -Fxq "$name"; then
		printf 'build-release: refusing unexpected output entry %s\n' \
			"$name" >&2
		exit 2
	fi
	if ! test -f "$entry" || test -L "$entry"; then
		printf 'build-release: refusing non-regular artifact %s\n' \
			"$name" >&2
		exit 2
	fi
done
release_artifact_names "$version" | while IFS= read -r name; do
	path=$output_dir/$name
	if test -e "$path"; then
		rm -f -- "$path"
	fi
done

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/wormhole-release.XXXXXX")
cleanup() {
	if test -d "$build_dir" && ! test -L "$build_dir"; then
		find -P "$build_dir" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

if test -n "${WORMHOLE_SYFT_BIN:-}"; then
	syft_bin=$WORMHOLE_SYFT_BIN
else
	"$(dirname "$0")/install-syft.sh" "$build_dir/tools"
	syft_bin=$build_dir/tools/syft
fi
if ! test -x "$syft_bin"; then
	printf 'build-release: Syft executable not found: %s\n' "$syft_bin" >&2
	exit 1
fi
case "$(uname -s):$(uname -m)" in
	Linux:x86_64)
		syft_checksum=23d4e25a32026ab27351c3c044a40bcc51311c00b8bb990aa204bec4b0bb19cd
		;;
	Linux:aarch64 | Linux:arm64)
		syft_checksum=ce828dc43bc44271f77c5e3fbc35efce71890cd83ba8dd39d6cfc8e2a031b2f2
		;;
	Darwin:x86_64)
		syft_checksum=7b73ad1a2a956a121686c9f18aa2c9ac2cb11f65c5648bd6013e1999b34bdcd2
		;;
	Darwin:arm64)
		syft_checksum=09aa35f766d0ea34b2e64d82b9c6e2e315ff74019bd7cebe7f3bf6057ef6c62f
		;;
	*)
		printf 'build-release: unsupported Syft host %s/%s\n' \
			"$(uname -s)" "$(uname -m)" >&2
		exit 1
		;;
esac
printf '%s  %s\n' "$syft_checksum" "$syft_bin" | sha256sum -c -
syft_version=$("$syft_bin" version -o json | jq -er .version)
if test "$syft_version" != 1.44.0; then
	printf 'build-release: expected Syft 1.44.0, got %s\n' \
		"$syft_version" >&2
	exit 1
fi

created=$(date -u -d "@$epoch" '+%Y-%m-%dT%H:%M:%SZ')

cd "$repo_root"
for arch in $release_arches; do
	base=wormhole-${version}-linux-${arch}
	stage=$build_dir/stage/$base
	archive_name=$(release_archive_name "$version" "$arch")
	archive=$output_dir/$archive_name
	mkdir -p "$stage"
	chmod 0755 "$build_dir/stage" "$stage"

	for binary in $release_binaries; do
		CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
			go build -mod=readonly -trimpath -buildvcs=false \
			-ldflags "-s -w -X main.version=$version" \
			-o "$stage/$binary" "./cmd/$binary"
		chmod 0755 "$stage/$binary"
	done
	cp LICENSE README.md "$stage/"
	chmod 0644 "$stage/LICENSE" "$stage/README.md"

	tar \
		--sort=name \
		--format=ustar \
		--owner=0 \
		--group=0 \
		--numeric-owner \
		--mtime="@$epoch" \
		--mode='u+rwX,go+rX,go-w' \
		-C "$build_dir/stage" \
		-cf - "$base" |
		gzip -9 -n >"$archive"
	chmod 0644 "$archive"

	archive_sha=$(sha256sum "$archive" | cut -d' ' -f1)
	extract_dir=$build_dir/extracted/$base
	mkdir -p "$extract_dir"
	tar --no-same-owner --no-same-permissions -xzf "$archive" \
		-C "$extract_dir" --strip-components=1

	raw_sbom=$build_dir/$arch.raw.spdx.json
	sbom=$output_dir/$(release_spdx_name "$arch")
	SYFT_CHECK_FOR_APP_UPDATE=false \
	SYFT_FILE_METADATA_SELECTION=all \
	SYFT_FILE_METADATA_DIGESTS=sha256 \
		"$syft_bin" scan "dir:$extract_dir" \
		--source-name "$base" \
		--source-version "$version" \
		--parallelism 1 \
		--output "spdx-json=$raw_sbom" \
		--quiet

	jq -S \
		--arg archive "$archive_name" \
		--arg base "$base" \
		--arg version "$version" \
		--arg digest "$archive_sha" \
		--arg created "$created" \
		--arg platform "linux/$arch" \
		'
		([.relationships[] |
			select(.spdxElementId == "SPDXRef-DOCUMENT" and
				.relationshipType == "DESCRIBES")][0].relatedSpdxElement) as $root
		| if $root == null then error("missing document root") else . end
		| .name = $base
		| .documentNamespace =
			("https://github.com/H4RL33/wormhole/releases/" +
				$version + "/" + $archive + "?sha256=" + $digest)
		| .creationInfo.created = $created
		| .packages |= map(
			if .SPDXID == $root then
				.name = $base
				| .versionInfo = $version
				| .packageFileName = $archive
				| .primaryPackagePurpose = "APPLICATION"
				| .checksums = [{
					"algorithm": "SHA256",
					"checksumValue": $digest
				}]
				| .comment =
					("Generated from extracted archive contents for " +
						$platform)
				| .externalRefs = ((.externalRefs // []) + [{
					"referenceCategory": "OTHER",
					"referenceType": "wormhole-target-platform",
					"referenceLocator": $platform
				}])
			else .
			end)
		| .packages |= sort_by(.SPDXID)
		| .files |= sort_by(.SPDXID)
		| .relationships |= sort_by(
			.spdxElementId, .relationshipType, .relatedSpdxElement)
		' "$raw_sbom" >"$sbom"
	chmod 0644 "$sbom"
done

: >"$output_dir/SHA256SUMS"
release_checksum_subject_names "$version" | while IFS= read -r file; do
	hash=$(sha256sum "$output_dir/$file" | cut -d' ' -f1)
	printf '%s  %s\n' "$hash" "$file" >>"$output_dir/SHA256SUMS"
done
chmod 0644 "$output_dir/SHA256SUMS"

"$(dirname "$0")/verify-release-artifacts.sh" "$output_dir"
