#!/usr/bin/env bash
# Prints the release notes of one version from RELEASE.md: the lines after
# the heading "## <tag>" up to the next line that is exactly "---". This is
# the format of every otfabric repository, and the rule the shared Go
# release workflow (otfabric/.github, go-package-release.yml) applies.
#
# Usage: release-notes.sh [<tag>]     e.g. release-notes.sh v0.3.0
#        Without a tag: the version versions.yaml names (interop_version).
#
# Fails when RELEASE.md has no such section, when the section is empty or
# when it does not end with "---": the release workflow publishes exactly
# this text as the body of the GitHub release.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tag="${1:-v$(sed -nE 's/^interop_version: "([^"]+)".*/\1/p' "${ROOT}/versions.yaml")}"
[[ "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "release-notes: '${tag}' is not vX.Y.Z" >&2; exit 2; }

notes=$(awk -v target="## ${tag}" '
    $0 == target { found = 1; next }
    found && /^---$/ { closed = 1; exit }
    found { print }
    END { if (found && !closed) exit 3 }
' "${ROOT}/RELEASE.md") || { echo "release-notes: the section ## ${tag} in RELEASE.md does not end with ---" >&2; exit 1; }

if [[ -z "${notes//[[:space:]]/}" ]]; then
    echo "release-notes: RELEASE.md has no notes under ## ${tag}" >&2
    exit 1
fi
printf '%s\n' "${notes}"
