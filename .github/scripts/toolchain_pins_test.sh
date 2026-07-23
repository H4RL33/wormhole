#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
go_version=1.26.5
builder_digest=1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651

cd "$repo_root"
grep -Fxq "go $go_version" go.mod
grep -Fxq \
	"FROM golang:$go_version-bookworm@sha256:$builder_digest AS build" \
	Dockerfile.fabric
grep -Fq "**Tech Stack:** Go $go_version," \
	docs/superpowers/plans/2026-07-23-production-readiness-and-interface-freeze.md

if grep -R -n -F '1.26.4' \
	go.mod \
	Dockerfile.fabric \
	docs/superpowers/plans/2026-07-23-production-readiness-and-interface-freeze.md
then
	printf '%s\n' 'authoritative Go toolchain pins still reference 1.26.4' >&2
	exit 1
fi
