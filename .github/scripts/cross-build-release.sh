#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
output_root=${1:-dist/cross-build}

cd "$repo_root"
for arch in amd64 arm64; do
	target_dir=$output_root/linux-$arch
	mkdir -p "$target_dir"
	for binary in wormhole gatewayd fabric; do
		CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
			go build -mod=readonly -trimpath -buildvcs=false \
			-o "$target_dir/$binary" "./cmd/$binary"
	done
done
