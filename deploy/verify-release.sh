#!/bin/sh
# Check a downloaded release artifact against the signed manifest of the release it came
# from. Every rule here is docs/specs/release.md#verifying-an-artifact.
#
#   ./verify-release.sh prismotr-1.2.3-linux-amd64
#
# The exit status is the verdict; what is printed is diagnostic. openssl is the only tool
# this needs, which is what lets it run on a node that has nothing installed for it.

set -eu

# Byte-wise comparisons and the same digest spelling whatever the operator's locale is.
LC_ALL=C
export LC_ALL

# Parameter expansion rather than basename and dirname: openssl is then the one command
# this script depends on, and a stripped PATH says so instead of failing on a helper.
program=${0##*/}
case $0 in
*/*) script_dir=${0%/*} ;;
*) script_dir=. ;;
esac

key=$script_dir/release-signing-key.pub
sums=
artifact=

# printf rather than a here-document: openssl is then the only command outside the shell
# this script needs, and a stripped PATH says so instead of failing to print its own usage.
usage() {
	printf '%s\n' \
		"usage: $program [--key <path>] [--sums <path>] <artifact>" \
		"" \
		"Checks one downloaded artifact against the release manifest that names it. The" \
		"manifest defaults to SHA256SUMS beside the artifact, its signature to that name" \
		"plus .sig, and the key to release-signing-key.pub beside this script."
}

refuse() {
	printf '%s: %s\n' "$program" "$1" >&2
	exit 1
}

refuse_with_usage() {
	printf '%s: %s\n' "$program" "$1" >&2
	usage >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case $1 in
	--key)
		[ $# -ge 2 ] || refuse_with_usage "--key needs a path"
		key=$2
		shift 2
		;;
	--sums)
		[ $# -ge 2 ] || refuse_with_usage "--sums needs a path"
		sums=$2
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	-*)
		refuse_with_usage "unknown option $1"
		;;
	*)
		[ -z "$artifact" ] || refuse_with_usage "one artifact at a time, and $1 is a second"
		artifact=$1
		shift
		;;
	esac
done

[ -n "$artifact" ] || refuse_with_usage "no artifact named"
[ -f "$artifact" ] || refuse_with_usage "$artifact is not a file"

command -v openssl >/dev/null 2>&1 ||
	refuse "openssl is not installed, and it is what checks the signature"

if [ -z "$sums" ]; then
	case $artifact in
	*/*) sums=${artifact%/*}/SHA256SUMS ;;
	*) sums=SHA256SUMS ;;
	esac
fi
signature=$sums.sig

[ -f "$key" ] || refuse "no public key at $key"
[ -f "$sums" ] || refuse "no manifest at $sums"
[ -f "$signature" ] || refuse "no signature at $signature"

# A key openssl cannot use fails verification exactly as a forged signature does, so the key
# is checked first and named: a rotation that left the wrong file behind must not read as
# tampering.
openssl pkey -pubin -in "$key" -noout >/dev/null 2>&1 ||
	refuse "$key is not a public key openssl can read"

# The verdict is openssl's exit status and never its output: LibreSSL, which is what
# /usr/bin/openssl is on macOS, prints "Verified OK" on runs that fail.
openssl dgst -sha256 -verify "$key" -signature "$signature" "$sums" >/dev/null 2>&1 ||
	refuse "$signature does not verify $sums with $key"

# openssl spells the line differently across versions — SHA256(f)= and SHA2-256(f)= — so the
# digest is taken as the last field rather than matched by name.
digest_line=$(openssl dgst -sha256 "$artifact" 2>/dev/null) ||
	refuse "openssl could not read $artifact"
digest=${digest_line##* }

name=${artifact##*/}
expected=
while read -r listed_digest listed_name || [ -n "$listed_digest" ]; do
	[ "$listed_name" = "$name" ] || continue
	expected=$listed_digest
done <"$sums"

[ -n "$expected" ] || refuse "$sums does not list $name (signature checked with $key)"
[ "$expected" = "$digest" ] ||
	refuse "$name is not the file $sums lists under that name (signature checked with $key)"

printf '%s: %s matches %s\n' "$program" "$name" "$sums"
