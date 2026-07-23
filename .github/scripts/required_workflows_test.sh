#!/bin/sh
set -eu

ci=.github/workflows/ci.yml
migrations=.github/workflows/migrations.yml
security=.github/workflows/security.yml

for contract in \
	"$ci:Contract Inventory" \
	"$ci:Static" \
	"$ci:Build" \
	"$ci:Integration" \
	"$ci:Race" \
	"$ci:Coverage" \
	"$migrations:Migrations" \
	"$security:Vulnerability" \
	"$security:Secret Scan" \
	"$security:Dependency Review" \
	"$security:Action Pins"
do
	workflow=${contract%%:*}
	check_name=${contract#*:}
	grep -Fq "    name: $check_name" "$workflow"
done

static_block=$(sed -n '/^  static:/,/^  build:/p' "$ci")
printf '%s\n' "$static_block" |
	grep -Fq 'go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7'

build_block=$(sed -n '/^  build:/,/^  integration:/p' "$ci")
printf '%s\n' "$build_block" |
	grep -Fq 'sh .github/scripts/cross-build-release.sh dist/cross-build'
printf '%s\n' "$build_block" | grep -Fq 'GOOS=darwin GOARCH=arm64'
printf '%s\n' "$build_block" | grep -Fq './cmd/gatewayd'

vulnerability_block=$(
	sed -n '/^  vulnerability:/,/^  secret-scan:/p' "$security"
)
printf '%s\n' "$vulnerability_block" |
	grep -Fq 'sh .github/scripts/cross-build-release.sh dist/vulnerability'
printf '%s\n' "$vulnerability_block" | grep -Fq 'govulncheck ./...'
printf '%s\n' "$vulnerability_block" |
	grep -Fq 'govulncheck -mode=binary "dist/vulnerability/linux-$arch/$binary"'
printf '%s\n' "$vulnerability_block" |
	grep -Fq 'for arch in amd64 arm64; do'
printf '%s\n' "$vulnerability_block" |
	grep -Fq 'for binary in wormhole gatewayd fabric; do'

artifact_build_line=$(
	printf '%s\n' "$vulnerability_block" |
		grep -nF 'cross-build-release.sh dist/vulnerability' | cut -d: -f1
)
artifact_scan_line=$(
	printf '%s\n' "$vulnerability_block" |
		grep -nF 'govulncheck -mode=binary' | cut -d: -f1
)
test "$artifact_build_line" -lt "$artifact_scan_line"
