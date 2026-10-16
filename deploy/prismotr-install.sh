#!/bin/sh
# Install or upgrade prismotr from a signed release. Every rule here is docs/specs/installer.md.
#
#   curl -fsSL <this script> | sudo sh
#   sudo ./prismotr-install.sh --version 1.2.3
#   sudo ./prismotr-install.sh --follow-target      # what the update timer runs
#
# This script downloads a release, checks it against the key below and hands over to the
# installer that release carries. It installs nothing itself, and the release installs
# nothing this script has not checked.
#
# The whole body is one function, invoked on the last line, so a transfer cut short runs
# nothing rather than half of this file.

set -eu

main() {
	LC_ALL=C
	export LC_ALL
	umask 022

	program=${0##*/}

	# The public half of the release signing key. A second copy of
	# deploy/release-signing-key.pub — nothing generates one from the other, and a test
	# asserts they are the same bytes: two copies that could drift are worse than one.
	release_key='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEuphV5rHxQEnKWgZ4d5KujTHxkqVa
DWm0ekmJTAzJvcVi5ZjNn+ii9FRFPg0Hbilx5hIcge4zpJSfqHyIf7+0Ww==
-----END PUBLIC KEY-----'
	default_origin=https://github.com/pravbeseda/prismotr

	work=
	trap 'cleanup' EXIT
	trap 'cleanup 130' INT
	trap 'cleanup 143' TERM
	trap 'cleanup 129' HUP

	version=
	allow_downgrade=0
	follow=0
	parse_arguments "$@"

	check_tools
	check_staging

	origin=${PRISMOTR_RELEASE_ORIGIN:-$default_origin}
	[ -n "$version" ] || version=$(newest_version)
	check_version "$version"
	newest=$version

	make_workdir
	fetch_release
	# A follow run downloads a binary only once an installer asks for it.
	[ "$follow" -eq 1 ] || fetch_binary
	check_not_a_downgrade
	unpack

	if [ "$follow" -eq 1 ]; then
		follow_target
		exit 0
	fi

	printf '%s: installing prismotr %s\n' "$program" "$version"
	# The installer reads nothing: a run in a pipeline would otherwise hand it whatever is on
	# that pipe.
	sh "$work/release/install.sh" --binary "$work/binary" </dev/null
}

refuse() {
	printf '%s: %s\n' "$program" "$1" >&2
	exit 1
}

usage() {
	cat <<USAGE
usage: $program [--version X.Y.Z] [--allow-downgrade]
       $program --follow-target

Downloads the newest release, checks its signature and installs from it.

--follow-target is what the update timer runs: the installer of the newest release reads
/etc/prismotr/target and does nothing, installs around the binary already in place, asks for
its binary, or names another release to fetch; a binary is downloaded only when an installer
asks for it.
USAGE
}

refuse_with_usage() {
	printf '%s: %s\n' "$program" "$1" >&2
	usage >&2
	exit 1
}

cleanup() {
	status=${1:-$?}
	[ -z "$work" ] || rm -rf "$work"
	exit "$status"
}

parse_arguments() {
	while [ $# -gt 0 ]; do
		case $1 in
		--version)
			if [ $# -lt 2 ] || [ -z "$2" ]; then
				refuse_with_usage "$1 needs a value"
			fi
			version=$2
			shift 2
			;;
		--allow-downgrade)
			allow_downgrade=1
			shift
			;;
		--follow-target)
			follow=1
			shift
			;;
		-h | --help)
			usage
			exit 0
			;;
		*) refuse_with_usage "unknown option: $1" ;;
		esac
	done

	# A follow run takes its version from the target alone.
	[ "$follow" -eq 1 ] || return 0
	[ -z "$version" ] || refuse_with_usage "--follow-target takes its version from the target, not --version"
	[ "$allow_downgrade" -eq 0 ] || refuse_with_usage "--follow-target installs an older release only when the target names it"
}

# The grammar deploy/tag-version.sh enforces on the way in, asked as a question: a release
# refuses what fails it, and the downgrade guard steps aside instead.
is_version() {
	# Splitting on dots drops a trailing empty field, so the dots are judged before it.
	case $1 in
	.* | *. | *..*) return 1 ;;
	esac
	oldifs=$IFS
	IFS=.
	set -f
	# shellcheck disable=SC2086 # deliberate: the IFS above is what splits on dots
	set -- $1
	set +f
	IFS=$oldifs
	[ $# -eq 3 ] || return 1
	for part in "$@"; do
		case $part in
		# A component wider than the shell's integers is not a version anyone releases, and
		# comparing one would error rather than answer.
		'' | *[!0-9]* | 0?* | ??????????*) return 1 ;;
		esac
	done
	return 0
}

# A version names a release, and it reaches a URL and a file name, so it is checked before it
# is used.
check_version() {
	is_version "$1" || refuse "not a version: $version"
}

# DESTDIR stages everything under a prefix, and the seams below exist for those runs alone: a
# real run uses the key in this file and the origin above, or it is not the release this
# project publishes.
check_staging() {
	destdir=${DESTDIR:-}
	if [ -n "$destdir" ]; then
		case $destdir in
		/*) ;;
		*) refuse "DESTDIR must be an absolute path: $destdir" ;;
		esac
		# `..` is refused outright rather than resolved: a failed cd falls back to the raw
		# value, and bash and dash disagree about a component that does not exist.
		case /$destdir/ in
		*/../*) refuse "DESTDIR must not contain ..: $destdir" ;;
		esac
		resolved=$(cd "$destdir" 2>/dev/null && pwd -P) || resolved=$destdir
		while [ "$resolved" != "${resolved%/}" ]; do
			resolved=${resolved%/}
		done
		[ -n "$resolved" ] || refuse "DESTDIR stages nothing, it resolves to /: $destdir"
		staged=1
		return 0
	fi
	staged=0
	for seam in PRISMOTR_RELEASE_ORIGIN PRISMOTR_RELEASE_KEY; do
		eval "value=\${$seam:-}"
		[ -z "$value" ] ||
			refuse "$seam is for a staged run: a real run uses the key and the origin this script carries"
	done
	[ "$(id -u)" -eq 0 ] || refuse "must run as root: re-run under sudo"
}

check_tools() {
	for tool in curl openssl tar find; do
		command -v "$tool" >/dev/null 2>&1 || refuse "$tool is not installed, and this needs it"
	done
}

# curl, with the transport pinned on a real run and no .curlrc read. A staged run reaches a
# test server on loopback, which is not https.
get() {
	if [ "$staged" -eq 1 ]; then
		curl -q -fsSL --max-time 120 "$@"
	else
		curl -q -fsSL --max-time 120 --proto '=https' --proto-redir '=https' "$@"
	fi
}

# The newest release is whichever tag the origin's own "latest" answers with.
newest_version() {
	latest=$(get -o /dev/null -w '%{url_effective}' "$origin/releases/latest") ||
		refuse "the origin cannot be reached: $origin"
	latest=${latest##*/}
	printf '%s' "${latest#v}"
}

make_workdir() {
	# An inherited TMPDIR can name a directory somebody else owns. A real run ignores it.
	[ "$staged" -eq 1 ] || TMPDIR=/tmp
	TMPDIR=${TMPDIR:-/tmp}
	export TMPDIR
	work=$(umask 077 && mktemp -d "${TMPDIR%/}/prismotr-install.XXXXXXXX") ||
		refuse "cannot create a working directory"
}

download() {
	get -o "$work/$2" "$origin/releases/download/v$version/$1" ||
		refuse "$1 is not in release $version, or the origin cannot be reached"
}

digest_of() {
	line=$(openssl dgst -sha256 "$1" 2>/dev/null) || refuse "openssl could not read $1"
	printf '%s' "${line##* }"
}

# The digest the manifest gives one name, and nothing if it does not name it at all.
manifest_digest() {
	found=
	while read -r listed_digest listed_name || [ -n "$listed_digest" ]; do
		[ "$listed_name" = "$1" ] || continue
		found=$listed_digest
	done <"$work/SHA256SUMS"
	printf '%s' "$found"
}

check_asset() {
	[ "$(manifest_digest "$1")" = "$(digest_of "$work/$2")" ] ||
		refuse "$1 does not match the digest release $version gives it"
}

# The manifest of release $version, verified, and the installer archive it lists.
fetch_release() {
	key=$work/release-key.pub
	if [ "$staged" -eq 1 ] && [ -n "${PRISMOTR_RELEASE_KEY:-}" ]; then
		cp "$PRISMOTR_RELEASE_KEY" "$key"
	else
		printf '%s\n' "$release_key" >"$key"
	fi
	openssl pkey -pubin -in "$key" -noout >/dev/null 2>&1 ||
		refuse "the key this run verifies with is not a public key openssl can read"

	download SHA256SUMS SHA256SUMS
	download SHA256SUMS.sig SHA256SUMS.sig
	# The verdict is openssl's exit status and never its output: LibreSSL prints "Verified
	# OK" on runs that fail.
	openssl dgst -sha256 -verify "$key" -signature "$work/SHA256SUMS.sig" "$work/SHA256SUMS" \
		>/dev/null 2>&1 ||
		refuse "the signature of release $version does not verify with this run's key"

	platform=$(platform_name)
	binary_asset=prismotr-$version-$platform
	archive_asset=prismotr-installer-$version.tar.gz

	# The manifest is asked first, so an asset missing from it is told apart from one the
	# origin would not serve, and neither is fetched on a guess.
	[ -n "$(manifest_digest "$archive_asset")" ] ||
		refuse "release $version carries no installer archive: it predates the installer"
	[ -n "$(manifest_digest "$binary_asset")" ] ||
		refuse "release $version lists no $binary_asset for this platform"

	download "$archive_asset" installer.tar.gz
	check_asset "$archive_asset" installer.tar.gz
}

# The binary of release $version, checked against the manifest fetch_release verified.
fetch_binary() {
	download "$binary_asset" binary
	check_asset "$binary_asset" binary
	chmod 0755 "$work/binary"
}

# A release publishes Linux binaries; darwin is named so a staged run on a Mac reaches the
# manifest and is refused there, by name, rather than here.
platform_name() {
	case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) refuse "unsupported system: $(uname -s)" ;;
	esac
	case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) refuse "unsupported architecture: $(uname -m)" ;;
	esac
	printf '%s-%s' "$os" "$arch"
}

# Protection against an operator's slip, not against an attack: the version compared against
# is reported by the very binary being replaced, so a run that cannot read one installs.
check_not_a_downgrade() {
	[ "$allow_downgrade" -eq 0 ] || return 0
	installed_binary=$destdir/usr/local/bin/prismotr
	if [ ! -x "$installed_binary" ]; then
		return 0
	fi
	unreadable="could not tell which version is installed; installing $version over it"
	[ "$follow" -eq 0 ] || unreadable="could not tell which version is installed; following the target over it"
	# Asking a binary its version means running it, so it is run only where nobody else could
	# have put it there. Writability by group or other says that on any run; ownership says
	# it only on a real one, where root is what "nobody else" means.
	replaceable=$(find "$installed_binary" "${installed_binary%/*}" -maxdepth 0 \
		\( -perm -g+w -o -perm -o+w \) 2>/dev/null) || replaceable=unknown
	if [ -z "$destdir" ] && [ -z "$replaceable" ]; then
		replaceable=$(find "$installed_binary" "${installed_binary%/*}" -maxdepth 0 \
			! -user root 2>/dev/null) || replaceable=unknown
	fi
	if [ -n "$replaceable" ]; then
		printf '%s: %s; another account can replace it\n' "$program" "$unreadable"
		return 0
	fi
	installed=$("$installed_binary" --version 2>/dev/null) || {
		printf '%s: %s\n' "$program" "$unreadable"
		return 0
	}
	installed=${installed##* }
	if ! is_version "$installed"; then
		printf '%s: %s\n' "$program" "$unreadable"
		return 0
	fi
	newer_or_same "$version" "$installed" && return 0
	# An origin answering with an old release as the newest must not roll the panel back.
	[ "$follow" -eq 0 ] ||
		refuse "the newest release $version is older than the installed $installed; a follow run installs nothing"
	refuse "release $version is older than the installed $installed; --allow-downgrade installs it anyway"
}

# The follow run of docs/specs/installer.md#following-a-target. The newest release's installer
# reads the target and answers: nothing, its own version to ask for its binary, or another
# release, whose own installer is then asked once more. At most three hand-overs.
follow_target() {
	hand_over_following
	named=$(answered_version) || exit 1
	[ -n "$named" ] || return 0
	if [ "$named" != "$version" ]; then
		followed=$version
		version=$named
		rm -rf "$work/unpacked" "$work/release" "$work/entries"
		fetch_release
		unpack
		hand_over_following
		again=$(answered_version) || exit 1
		[ -n "$again" ] || return 0
		[ "$again" = "$version" ] ||
			refuse "release $followed named $version, whose installer named $again in turn; nothing is installed"
	fi

	fetch_binary
	hand_over_following --binary "$work/binary"
	[ ! -s "$work/answer" ] ||
		refuse "the installer of release $version answered after it was given its binary; the run stops"
}

# One hand-over to the unpacked release's installer, with the options of
# docs/specs/installer.md#the-handover and whatever is added after them.
hand_over_following() {
	: >"$work/answer"
	printf '%s: handing over to release %s to follow the target\n' "$program" "$version"
	status=0
	sh "$work/release/install.sh" --follow-target --release "$version" --newest "$newest" \
		--digest "$(manifest_digest "$binary_asset")" --answer "$work/answer" "$@" </dev/null ||
		status=$?
	[ "$status" -eq 0 ] || exit "$status"
}

# The version the installer wrote into the answer, or nothing when it wrote nothing. An answer
# is one version and a trailing newline; anything else stops the run before it is used.
answered_version() {
	[ -s "$work/answer" ] || return 0
	lines=$(wc -l <"$work/answer")
	answer=$(cat "$work/answer")
	if [ $((lines)) -gt 1 ] || ! is_version "$answer"; then
		printf '%s: the installer of release %s answered with something that is not a version: %s\n' \
			"$program" "$version" "$answer" >&2
		return 1
	fi
	printf '%s' "$answer"
}

# Component by component and numerically: 1.10.0 is newer than 1.9.0.
newer_or_same() {
	oldifs=$IFS
	IFS=.
	set -f
	# shellcheck disable=SC2086 # deliberate: the IFS above is what splits on dots
	set -- $1 $2
	set +f
	IFS=$oldifs
	left=0
	right=0
	for index in 1 2 3; do
		eval "left=\${$index:-0}"
		eval "right=\${$((index + 3)):-0}"
		[ "$left" -lt "$right" ] && return 1
		[ "$left" -gt "$right" ] && return 0
	done
	return 0
}

# What a signature says about an archive is nothing about what unpacking it does, so the
# entries are read before any of them is written.
unpack() {
	tar -tzf "$work/installer.tar.gz" >"$work/entries" 2>/dev/null ||
		refuse "the installer archive is not readable"
	while IFS= read -r entry || [ -n "$entry" ]; do
		case $entry in
		/* | *..*) refuse "the installer archive reaches outside itself: $entry" ;;
		esac
	done <"$work/entries"
	if tar -tvzf "$work/installer.tar.gz" 2>/dev/null | grep -qv '^[-d]'; then
		refuse "the installer archive holds something that is not a file or a directory"
	fi

	mkdir "$work/unpacked"
	tar -xzf "$work/installer.tar.gz" -C "$work/unpacked" --no-same-owner --no-same-permissions ||
		refuse "the installer archive could not be unpacked"

	# One top-level directory, whatever it is called.
	entry=$(ls "$work/unpacked")
	[ -d "$work/unpacked/$entry" ] ||
		refuse "the installer archive is not one directory of files"
	ln -s "$work/unpacked/$entry" "$work/release"
	[ -f "$work/release/install.sh" ] ||
		refuse "the installer archive carries no install.sh"
}

main "$@"
