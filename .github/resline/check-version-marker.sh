#!/usr/bin/env bash
# Checks the first line of `xray version`:
#   - the second field is a plain X.Y.Z (3x-ui and the hteppl exporter parse it)
#   - the line carries the "(resline; ..." marker
#   - with a tag given, the tag is v<X.Y.Z>-resline.<n>
#
# Usage: .github/resline/check-version-marker.sh <xray-binary> [<tag>]
set -euo pipefail

bin="$1"
tag="${2:-}"

out=$("${bin}" version)
echo "${out}"
first=${out%%$'\n'*}
read -r _ version _ <<< "${first}"

if [[ ! "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "version field '${version}' is not plain X.Y.Z" >&2
    exit 1
fi
if [[ "${first}" != *"(resline; "* ]]; then
    echo "no resline marker in: ${first}" >&2
    exit 1
fi
if [[ -n "${tag}" && ! "${tag}" =~ ^v${version//./\\.}-resline\.[1-9][0-9]*$ ]]; then
    echo "tag ${tag} is not v${version}-resline.<n>" >&2
    exit 1
fi
echo "xray version output OK${tag:+ for tag ${tag}}"
