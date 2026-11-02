#!/usr/bin/env bash
# Fails when a tracked file downloads from upstream 3x-ui or Xray-core.
#
# The resline forks point every update and download path at
# resline-networks/*. A download address that still names MHSanaei/3x-ui or
# XTLS/Xray-core would let an update silently install an upstream build, so CI
# refuses it. Matched: raw.githubusercontent.com/<repo>/, api.github.com/repos/<repo>,
# github.com/<repo>/releases|archive|raw, codeload.github.com/<repo>.
# Not matched: Go import paths (github.com/mhsanaei/3x-ui/v3/..., github.com/xtls/xray-core/...)
# and links to issues, wikis or the repo page. Documentation is excluded.
#
# Usage: .github/resline/check-upstream-urls.sh [<rev>]   (from the repo root)
#   Without <rev> it checks the working tree; with one (e.g. a new upstream tag
#   before rebasing onto it) it checks that revision.
set -euo pipefail

rev=("$@")

repos='(mhsanaei/3x-ui|xtls/xray-core)'
pattern="(raw\.githubusercontent\.com/${repos}/|api\.github\.com/repos/${repos}([/\"' ]|$)|codeload\.github\.com/${repos}/|github\.com/${repos}/(releases|archive|raw)([/\"' ]|$))"

excludes=(
    ':(exclude,glob)**/*.md'
    ':(exclude,glob)**/*.mdx'
    ':(exclude)docs'                      # documentation site
    ':(exclude).github/ISSUE_TEMPLATE'    # links for humans, not downloads
    ':(exclude).github/resline/check-upstream-urls.sh'
)

if hits=$(git grep -n -I -i -E "${pattern}" "${rev[@]}" -- . "${excludes[@]}"); then
    echo "Download addresses pointing at upstream (use resline-networks/* instead):" >&2
    echo "${hits}" >&2
    exit 1
fi
echo "No upstream download addresses."
