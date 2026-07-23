#!/bin/sh
set -eu

repo_root=$(git rev-parse --show-toplevel)
metadata=$repo_root/.github/scripts/release-metadata.sh
verify_tag=$repo_root/.github/scripts/verify-release-tag.sh
publish_release=$repo_root/.github/scripts/publish-github-release.sh
tmp_dir=$(mktemp -d)
cleanup() {
	if test -d "$tmp_dir" && ! test -L "$tmp_dir"; then
		find -P "$tmp_dir" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

git init --bare -q "$tmp_dir/remote.git"
git init -q "$tmp_dir/source"
(
	cd "$tmp_dir/source"
	git config user.name 'Release Test'
	git config user.email release-test@example.invalid
	printf 'one\n' >source
	git add source
	git commit -qm one
	git branch -M main
	git remote add origin "$tmp_dir/remote.git"

	for tag in v1.2.3-alpha v1.2.3-beta.4 v1.2.3; do
		git tag -a "$tag" -m "$tag"
	done
	git tag v1.2.3-lightweight
	git push -q origin main \
		v1.2.3-alpha v1.2.3-beta.4 v1.2.3 v1.2.3-lightweight

	for tag in v1.2.3-alpha v1.2.3-beta.4 v1.2.3; do
		output=$tmp_dir/${tag}.output
		GITHUB_SHA=$(git rev-parse "$tag^{commit}") \
			"$metadata" push "$tag" ignored "$output"
		grep -qx 'publish=true' "$output"
		grep -qx 'release_enabled=false' "$output"
		grep -qx "tag_object=$(git rev-parse "$tag^{tag}")" "$output"
		grep -qx "tag_commit=$(git rev-parse "$tag^{commit}")" "$output"
	done
	grep -qx 'prerelease=true' "$tmp_dir/v1.2.3-alpha.output"
	grep -qx 'prerelease=true' "$tmp_dir/v1.2.3-beta.4.output"
	grep -qx 'prerelease=false' "$tmp_dir/v1.2.3.output"

	if "$metadata" push v1.2.3-lightweight ignored \
		"$tmp_dir/lightweight.output"
	then
		printf 'release metadata accepted a lightweight tag\n' >&2
		exit 1
	fi

	"$metadata" workflow_dispatch ignored 3.4.5-alpha.rehearsal \
		"$tmp_dir/manual.output"
	grep -qx 'publish=false' "$tmp_dir/manual.output"
	grep -qx 'release_enabled=false' "$tmp_dir/manual.output"
	grep -qx 'version=3.4.5-alpha.rehearsal' "$tmp_dir/manual.output"

	WORMHOLE_RELEASE_ENABLED=false \
		GITHUB_SHA=$(git rev-parse 'v1.2.3^{commit}') \
		"$metadata" push v1.2.3 ignored "$tmp_dir/disabled.output"
	grep -qx 'publish=true' "$tmp_dir/disabled.output"
	grep -qx 'release_enabled=false' "$tmp_dir/disabled.output"

	WORMHOLE_RELEASE_ENABLED=TRUE \
		GITHUB_SHA=$(git rev-parse 'v1.2.3^{commit}') \
		"$metadata" push v1.2.3 ignored "$tmp_dir/wrong-case.output"
	grep -qx 'release_enabled=false' "$tmp_dir/wrong-case.output"

	WORMHOLE_RELEASE_ENABLED=true \
		GITHUB_SHA=$(git rev-parse 'v1.2.3^{commit}') \
		"$metadata" push v1.2.3 ignored "$tmp_dir/enabled.output"
	grep -qx 'release_enabled=true' "$tmp_dir/enabled.output"

	alpha_object=$(git rev-parse 'v1.2.3-alpha^{tag}')
	alpha_commit=$(git rev-parse 'v1.2.3-alpha^{commit}')
	"$verify_tag" v1.2.3-alpha "$alpha_object" "$alpha_commit" origin

	release_dir=$tmp_dir/release
	fake_bin=$tmp_dir/bin
	mkdir "$release_dir" "$fake_bin"
	printf 'artifact\n' >"$release_dir/artifact"
	cat >"$fake_bin/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$@" >"$GH_ARGS_FILE"
EOF
	chmod 0755 "$fake_bin/gh"

	GH_ARGS_FILE=$tmp_dir/alpha.args PATH=$fake_bin:$PATH \
		"$publish_release" v1.2.3-alpha "$alpha_object" \
		"$alpha_commit" "$release_dir" origin
	grep -qx -- '--prerelease' "$tmp_dir/alpha.args"
	grep -qx -- '--latest=false' "$tmp_dir/alpha.args"

	beta_object=$(git rev-parse 'v1.2.3-beta.4^{tag}')
	beta_commit=$(git rev-parse 'v1.2.3-beta.4^{commit}')
	GH_ARGS_FILE=$tmp_dir/beta.args PATH=$fake_bin:$PATH \
		"$publish_release" v1.2.3-beta.4 "$beta_object" \
		"$beta_commit" "$release_dir" origin
	grep -qx -- '--prerelease' "$tmp_dir/beta.args"
	grep -qx -- '--latest=false' "$tmp_dir/beta.args"

	stable_object=$(git rev-parse 'v1.2.3^{tag}')
	stable_commit=$(git rev-parse 'v1.2.3^{commit}')
	GH_ARGS_FILE=$tmp_dir/stable.args PATH=$fake_bin:$PATH \
		"$publish_release" v1.2.3 "$stable_object" \
		"$stable_commit" "$release_dir" origin
	if grep -qx -- '--prerelease' "$tmp_dir/stable.args"; then
		printf 'stable release was marked as a prerelease\n' >&2
		exit 1
	fi
	if grep -q -- '--latest' "$tmp_dir/stable.args"; then
		printf 'stable release overrode GitHub latest selection\n' >&2
		exit 1
	fi

	printf 'two\n' >>source
	git add source
	git commit -qm two
	git tag -fa v1.2.3-alpha -m moved
	git push -q --force origin v1.2.3-alpha
	if "$verify_tag" v1.2.3-alpha "$alpha_object" "$alpha_commit" origin; then
		printf 'remote tag drift was not rejected\n' >&2
		exit 1
	fi
)
