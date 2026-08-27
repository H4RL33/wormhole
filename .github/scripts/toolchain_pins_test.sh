#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
go_version=1.26.6
builder_digest=116d58cbd88c1297624acc6e967a060012422bacf9930927e23fb719189c6f36

cd "$repo_root"
grep -Fxq "go $go_version" go.mod
grep -Fxq \
	"FROM golang:$go_version-bookworm@sha256:$builder_digest AS build" \
	Dockerfile.fabric
grep -Fxq \
	"FROM golang:$go_version-bookworm@sha256:$builder_digest AS build" \
	.github/scripts/fabric-image-cohere-mock/Dockerfile
grep -Fq "**Tech Stack:** Go $go_version," \
	docs/superpowers/plans/2026-07-23-production-readiness-and-interface-freeze.md

if grep -R -n -F '1.26.4' \
	go.mod \
	Dockerfile.fabric \
	.github/scripts/fabric-image-cohere-mock/Dockerfile \
	docs/superpowers/plans/2026-07-23-production-readiness-and-interface-freeze.md
then
	printf '%s\n' 'authoritative Go toolchain pins still reference 1.26.4' >&2
	exit 1
fi
