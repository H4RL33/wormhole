#!/bin/sh
set -eu

if test "$#" -ne 4 && test "$#" -ne 5; then
	printf 'usage: %s TAG EXPECTED_TAG_OBJECT EXPECTED_COMMIT RELEASE_DIR [REMOTE]\n' \
		"$0" >&2
	exit 2
fi

tag=$1
expected_object=$2
expected_commit=$3
release_dir=$4
remote=${5:-origin}
script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd -P)

if ! test -d "$release_dir" || test -L "$release_dir"; then
	printf 'publish-github-release: invalid release directory\n' >&2
	exit 1
fi
set -- "$release_dir"/*
if test "$#" -eq 1 && ! test -e "$1"; then
	printf 'publish-github-release: release directory is empty\n' >&2
	exit 1
fi
for artifact in "$@"; do
	if ! test -f "$artifact" || test -L "$artifact"; then
		printf 'publish-github-release: invalid artifact %s\n' \
			"$artifact" >&2
		exit 1
	fi
done

"$script_dir/verify-release-tag.sh" \
	"$tag" "$expected_object" "$expected_commit" "$remote"

case "$tag" in
	*-alpha | *-beta.*)
		gh release create "$tag" "$release_dir"/* \
			--verify-tag --generate-notes --title "$tag" \
			--prerelease --latest=false
		;;
	*)
		gh release create "$tag" "$release_dir"/* \
			--verify-tag --generate-notes --title "$tag"
		;;
esac
