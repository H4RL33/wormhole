#!/bin/sh
set -eu

if test "$#" -ne 4; then
	printf 'usage: %s EVENT REF_NAME REHEARSAL_VERSION OUTPUT_FILE\n' \
		"$0" >&2
	exit 2
fi

event_name=$1
ref_name=$2
rehearsal_version=$3
output_file=$4
publish=false
prerelease=false
release_enabled=false
tag_object=
tag_commit=

if test "${WORMHOLE_RELEASE_ENABLED:-}" = true; then
	release_enabled=true
fi

if test "$event_name" = push; then
	printf '%s\n' "$ref_name" |
		grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-alpha|-beta\.[0-9]+)?$'
	tag_ref=refs/tags/$ref_name
	test "$(git cat-file -t "$tag_ref")" = tag
	tag_object=$(git rev-parse "$tag_ref^{tag}")
	tag_commit=$(git rev-parse "$tag_ref^{commit}")
	if test -n "${GITHUB_SHA:-}" && test "$tag_commit" != "$GITHUB_SHA"; then
		printf 'release-metadata: tag commit %s does not match workflow SHA %s\n' \
			"$tag_commit" "$GITHUB_SHA" >&2
		exit 1
	fi
	version=${ref_name#v}
	publish=true
	case "$ref_name" in
		*-alpha | *-beta.*) prerelease=true ;;
	esac
elif test "$event_name" = workflow_dispatch; then
	version=$rehearsal_version
else
	printf 'release-metadata: unsupported event %s\n' "$event_name" >&2
	exit 1
fi

case "$version" in
	"" | *[!A-Za-z0-9._-]*)
		printf 'release-metadata: invalid version %s\n' "$version" >&2
		exit 1
		;;
esac
if test "${#version}" -gt 64; then
	printf 'release-metadata: version must not exceed 64 characters\n' >&2
	exit 1
fi

{
	printf 'publish=%s\n' "$publish"
	printf 'prerelease=%s\n' "$prerelease"
	printf 'release_enabled=%s\n' "$release_enabled"
	printf 'version=%s\n' "$version"
	printf 'tag_object=%s\n' "$tag_object"
	printf 'tag_commit=%s\n' "$tag_commit"
} >>"$output_file"
