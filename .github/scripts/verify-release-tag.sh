#!/bin/sh
set -eu

if test "$#" -ne 3 && test "$#" -ne 4; then
	printf 'usage: %s TAG EXPECTED_TAG_OBJECT EXPECTED_COMMIT [REMOTE]\n' \
		"$0" >&2
	exit 2
fi

tag=$1
expected_object=$2
expected_commit=$3
remote=${4:-origin}
printf '%s\n' "$tag" |
	grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-alpha|-beta\.[0-9]+)?$'
for digest in "$expected_object" "$expected_commit"; do
	printf '%s\n' "$digest" | grep -Eq '^[0-9a-f]{40}$'
done

remote_refs=$(git ls-remote "$remote" \
	"refs/tags/$tag" "refs/tags/$tag^{}")
remote_object=$(printf '%s\n' "$remote_refs" |
	awk -v ref="refs/tags/$tag" '$2 == ref { print $1 }')
remote_commit=$(printf '%s\n' "$remote_refs" |
	awk -v ref="refs/tags/$tag^{}" '$2 == ref { print $1 }')

if test "$remote_object" != "$expected_object" ||
	test "$remote_commit" != "$expected_commit"
then
	printf 'verify-release-tag: remote %s moved: object=%s commit=%s\n' \
		"$tag" "${remote_object:-missing}" "${remote_commit:-missing}" >&2
	exit 1
fi
