#!/bin/sh
# Print the tag the next release takes: the highest release tag read on stdin, moved by the
# merged pull request's release: label. The tagging workflow runs it, so the choice of
# docs/specs/release.md#tagging-a-merge is tested with the rest of the project.
#
#   git tag --list 'v*' | ./next-tag.sh release:minor   # prints v1.3.0 after v1.2.3

set -eu

LC_ALL=C
export LC_ALL

program=${0##*/}
here=$(dirname -- "$0")

refuse() {
	printf '%s: %s\n' "$program" "$1" >&2
	exit 1
}

# GitHub treats label names regardless of case, so a label is compared in lower case.
chosen=
chosen_label=
for label in "$@"; do
	lowered=$(printf '%s' "$label" | tr '[:upper:]' '[:lower:]')
	case $lowered in
	release:minor | release:major | release:none) ;;
	release:*) refuse "$label is not a release label: use release:minor, release:major or release:none" ;;
	*) continue ;;
	esac
	if [ -n "$chosen" ] && [ "$chosen" != "$lowered" ]; then
		refuse "the release labels disagree: $chosen_label and $label"
	fi
	chosen=$lowered
	chosen_label=$label
done

major=0
minor=0
patch=0

# Whether MAJOR MINOR PATCH sorts above the highest version read so far.
is_higher() {
	if [ "$1" -ne "$major" ]; then
		[ "$1" -gt "$major" ]
	elif [ "$2" -ne "$minor" ]; then
		[ "$2" -gt "$minor" ]
	else
		[ "$3" -gt "$patch" ]
	fi
}

# A line that names no version is not a release tag, so it is skipped rather than refused.
while IFS= read -r line || [ -n "$line" ]; do
	version=$(sh "$here/tag-version.sh" "$line" 2>/dev/null) || continue
	oldifs=$IFS
	IFS=.
	# shellcheck disable=SC2086 # deliberate: the IFS above is what makes this split on dots
	set -- $version
	IFS=$oldifs
	if is_higher "$1" "$2" "$3"; then
		major=$1
		minor=$2
		patch=$3
	fi
done

# Only now, with the input read to its end: stopping earlier kills whatever pipes tags in.
[ "$chosen" != release:none ] || exit 0

case $chosen in
release:major)
	major=$((major + 1))
	minor=0
	patch=0
	;;
release:minor)
	minor=$((minor + 1))
	patch=0
	;;
*) patch=$((patch + 1)) ;;
esac

tag=v$major.$minor.$patch
sh "$here/tag-version.sh" "$tag" >/dev/null 2>&1 ||
	refuse "cannot make $tag: a part of it is too wide to be a version"
printf '%s\n' "$tag"
