#!/bin/sh
set -eu

workflow=.github/workflows/release.yml
test -f "$workflow"
# shellcheck disable=SC2016 # These are literal workflow snippets.
annotated_tag_check='test "$(git cat-file -t "$GITHUB_REF_NAME")" = tag'
# shellcheck disable=SC2016 # This is a literal workflow snippet.
release_command='gh release create "$GITHUB_REF_NAME" dist/release/*'

grep -Fq 'workflow_dispatch:' "$workflow"
grep -Fq "tags: ['v*']" "$workflow"
grep -Fq "$annotated_tag_check" "$workflow"
grep -Fq "github.event_name == 'push'" "$workflow"
grep -Fq 'environment: release' "$workflow"
grep -Fq 'ghcr.io/h4rl33/wormhole-fabric' "$workflow"
grep -Fq "$release_command" "$workflow"

for pin in \
	c94ce9fb468520275223c153574b00df6fe4bcc9 \
	c7c53464625b32c7a7e944ae62b3e17d2b600130 \
	8d2750c68a42422c14e847fe6c8ac0403b4cbd6f \
	10e90e3645eae34f1e60eeb005ba3a3d33f178e8 \
	398d4b0eeef1380460a10c8013a76f728fb906ac \
	e8998f949152b193b063cb0ec769d69d929409be \
	f8bdd1d8ac5e901a77a92f111440fdb1b593736b
do
	grep -Fq "@$pin" "$workflow"
done

.github/scripts/check-action-pins.sh

publish_lines=$(grep -cE 'packages: write|id-token: write|attestations: write' "$workflow")
test "$publish_lines" -eq 5

release_line=$(grep -n 'gh release create' "$workflow" | cut -d: -f1)
last_step_line=$(grep -n '^      - name:' "$workflow" | tail -n 1 | cut -d: -f1)
test "$release_line" -gt "$last_step_line"
