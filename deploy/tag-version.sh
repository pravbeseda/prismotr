#!/bin/sh
# Turn a release tag into the version it names, or refuse it. The release workflow reads the
# version from here, so the grammar of docs/specs/release.md#publishing is tested with the
# rest of the project rather than only by pushing a tag.
#
#   version=$(./tag-version.sh v1.2.3)   # prints 1.2.3

set -eu

LC_ALL=C
export LC_ALL

program=${0##*/}

refuse() {
	printf '%s: %s\n' "$program" "$1" >&2
	exit 1
}

[ $# -eq 1 ] || refuse "usage: $program <tag>"

tag=$1
version=${tag#v}
[ "$version" != "$tag" ] || refuse "$tag is not a release tag: a release is vMAJOR.MINOR.PATCH"

# Split on the dots. Globbing is off so a part holding a bracket is not expanded, and IFS is
# restored because the parts are compared byte-wise below.
oldifs=$IFS
IFS=.
set -f
# shellcheck disable=SC2086 # deliberate: the IFS above is what makes this split on dots
set -- $version
set +f
IFS=$oldifs

[ $# -eq 3 ] || refuse "$tag is not a release tag: a release is vMAJOR.MINOR.PATCH"
for part in "$@"; do
	case $part in
	'' | *[!0-9]*) refuse "$tag is not a release tag: $part is not a number" ;;
	# 01 and 1 would be two names for one version, and only one of them is asked for.
	0?*) refuse "$tag is not a release tag: $part has a leading zero" ;;
	# Wider than any shell compares as an integer, which is what reads versions in order.
	??????????*) refuse "$tag is not a release tag: $part is too long to be a version" ;;
	esac
done

printf '%s\n' "$version"
