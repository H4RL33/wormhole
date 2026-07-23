#!/bin/sh
set -eu
umask 022

SYFT_VERSION=1.44.0

if test "$#" -ne 1; then
	printf 'usage: %s OUTPUT_DIR\n' "$0" >&2
	exit 2
fi

for command in curl sha256sum tar; do
	if ! command -v "$command" >/dev/null 2>&1; then
		printf 'install-syft: %s is required\n' "$command" >&2
		exit 1
	fi
done

case "$(uname -s):$(uname -m)" in
	Linux:x86_64)
		platform=linux_amd64
		checksum=0e91737aee2b5baf1d255b959630194a302335d848ff97bb07921eb6205b5f5a
		binary_checksum=23d4e25a32026ab27351c3c044a40bcc51311c00b8bb990aa204bec4b0bb19cd
		;;
	Linux:aarch64 | Linux:arm64)
		platform=linux_arm64
		checksum=6f6cdcdc695721d91ce756e3b5bc3e3416599c464101f5e32e9c3f33054ee6d9
		binary_checksum=ce828dc43bc44271f77c5e3fbc35efce71890cd83ba8dd39d6cfc8e2a031b2f2
		;;
	Darwin:x86_64)
		platform=darwin_amd64
		checksum=c40ece5407927327f94f35901727dbc604b46857e04f04ec94a310845fb71bde
		binary_checksum=7b73ad1a2a956a121686c9f18aa2c9ac2cb11f65c5648bd6013e1999b34bdcd2
		;;
	Darwin:arm64)
		platform=darwin_arm64
		checksum=24e4d34078ae81da7c82539616f0ccac3e226cf4f74a38ce6fb3463619e50a55
		binary_checksum=09aa35f766d0ea34b2e64d82b9c6e2e315ff74019bd7cebe7f3bf6057ef6c62f
		;;
	*)
		printf 'install-syft: unsupported platform %s/%s\n' \
			"$(uname -s)" "$(uname -m)" >&2
		exit 1
		;;
esac

output_dir=$1
if test -L "$output_dir"; then
	printf 'install-syft: output directory must not be a symlink\n' >&2
	exit 1
fi
mkdir -p "$output_dir"
if test -L "$output_dir/syft"; then
	printf 'install-syft: refusing to replace symlink %s/syft\n' \
		"$output_dir" >&2
	exit 1
fi
if test -e "$output_dir/syft" && ! test -f "$output_dir/syft"; then
	printf 'install-syft: refusing to replace non-regular %s/syft\n' \
		"$output_dir" >&2
	exit 1
fi

tmp_dir=$(mktemp -d)
cleanup() {
	if test -d "$tmp_dir" && ! test -L "$tmp_dir"; then
		find -P "$tmp_dir" -depth -delete
	fi
}
trap cleanup EXIT HUP INT TERM

archive=syft_${SYFT_VERSION}_${platform}.tar.gz
url=https://github.com/anchore/syft/releases/download/v${SYFT_VERSION}/${archive}
curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 \
	--output "$tmp_dir/$archive" "$url"
printf '%s  %s\n' "$checksum" "$tmp_dir/$archive" | sha256sum -c -
tar -xzf "$tmp_dir/$archive" -C "$tmp_dir" syft
chmod 0755 "$tmp_dir/syft"
printf '%s  %s\n' "$binary_checksum" "$tmp_dir/syft" | sha256sum -c -

actual_version=$("$tmp_dir/syft" version |
	awk '$1 == "Version:" { print $2 }')
if test "$actual_version" != "$SYFT_VERSION"; then
	printf 'install-syft: expected version %s, got %s\n' \
		"$SYFT_VERSION" "$actual_version" >&2
	exit 1
fi

mv -f "$tmp_dir/syft" "$output_dir/syft"
chmod 0755 "$output_dir/syft"
