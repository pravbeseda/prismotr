#!/bin/sh
# The installer a release carries: it puts prismotr in place as a system service and answers
# a follow run. Every path, mode and refusal here is docs/specs/installer.md; nothing is
# configurable, no binary is built, nothing is downloaded, and no configuration is invented.
#
#   sudo ./install.sh --binary ./prismotr
#
# The daemon needs /etc/prismotr/prismotr.yaml, which ansible-hosts writes. A run that does
# not find it installs everything else, writes the example beside it and stops short of
# starting the service, naming what is missing.

set -eu

LC_ALL=C
export LC_ALL

# Every file mode below is set explicitly, but a directory's is not: without this, a caller
# whose umask is 0 would leave /etc/prismotr writable by anyone.
umask 022

program=$(basename "$0")
source_dir=$(dirname "$0")

# What has already reached the disk. A run that fails after its first write stops there and
# names this list; it neither continues nor rolls back.
written=

# The temporary of the write in flight, removed however the run ends.
temp=

on_exit() {
	status=${1:-$?}
	[ -z "$temp" ] || rm -f "$temp"
	if [ "$status" -ne 0 ] && [ -n "$written" ]; then
		printf '%s: stopped after writing:\n%s' "$program" "$written" >&2
		written=
	fi
	exit "$status"
}
trap 'on_exit' EXIT
trap 'on_exit 130' INT
trap 'on_exit 143' TERM
trap 'on_exit 129' HUP

usage() {
	cat <<USAGE
usage: $program --binary <path>
       $program --follow-target --release X.Y.Z --newest X.Y.Z --digest <sha256> --answer <file>
           [--binary <path>]

Installs the prismotr binary, its service definition and the example files. The real
configuration is written by ansible-hosts: a run that does not find it leaves the service
stopped.

A follow run reads /etc/prismotr/target first. When the target names another release it
writes that version into the answer file; when it names this one and the daemon already runs
a binary of that digest it does nothing; otherwise it installs the binary it was given,
installs around a binary in place of that digest, or writes this release's version to ask
for it.

DESTDIR stages the whole installation under a prefix and registers no service.
USAGE
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

binary=
follow=
release=
newest=
digest=
answer=
while [ $# -gt 0 ]; do
	case $1 in
	--binary | --release | --newest | --digest | --answer)
		[ "$#" -ge 2 ] || refuse "$1 needs a value"
		case $1 in
		--binary) binary=$2 ;;
		--release) release=$2 ;;
		--newest) newest=$2 ;;
		--digest) digest=$2 ;;
		--answer) answer=$2 ;;
		esac
		shift 2
		;;
	--follow-target)
		follow=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		refuse_with_usage "unknown option: $1"
		;;
	esac
done

# The grammar deploy/tag-version.sh enforces. A copy of the function in prismotr-install.sh,
# which cannot share a file with a release; a test asserts the two are the same.
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

# The five options of a follow run come together or not at all, and only a follow run may go
# without a binary: it asks for one (installer.md#the-handover).
if [ -n "$follow$release$newest$digest$answer" ]; then
	[ -n "$follow" ] || refuse "a follow run needs --follow-target as well"
	[ -n "$release" ] || refuse "a follow run needs --release as well"
	[ -n "$newest" ] || refuse "a follow run needs --newest as well"
	[ -n "$digest" ] || refuse "a follow run needs --digest as well"
	[ -n "$answer" ] || refuse "a follow run needs --answer as well"
	is_version "$release" || refuse "--release is not a version: $release"
	is_version "$newest" || refuse "--newest is not a version: $newest"
	case $digest in
	*[!0-9a-f]*) refuse "--digest is not a SHA-256 in lowercase hexadecimal: $digest" ;;
	esac
	[ ${#digest} -eq 64 ] || refuse "--digest is not a SHA-256 in lowercase hexadecimal: $digest"
	if [ ! -f "$answer" ] || [ -s "$answer" ]; then
		refuse "--answer must name an empty file: $answer"
	fi
else
	[ -n "$binary" ] || refuse "--binary is required"
fi
if [ -n "$binary" ]; then
	[ -f "$binary" ] || refuse "--binary names no file: $binary"
	[ -x "$binary" ] || refuse "--binary names a file that is not executable: $binary"
fi

# DESTDIR stages the whole installation under a prefix: no root, no account, no service. A
# value that stages nothing is refused rather than quietly writing into the real system.
destdir=${DESTDIR:-}
if [ -n "$destdir" ]; then
	case $destdir in
	/*) ;;
	*) refuse "DESTDIR must be an absolute path: $destdir" ;;
	esac
	case /$destdir/ in
	*/../*) refuse "DESTDIR must not contain ..: $destdir" ;;
	esac
	resolved=$(cd "$destdir" 2>/dev/null && pwd -P) || resolved=$destdir
	while [ "$resolved" != "${resolved%/}" ]; do
		resolved=${resolved%/}
	done
	[ -n "$resolved" ] || refuse "DESTDIR stages nothing, it resolves to /: $destdir"
else
	[ "$(id -u)" -eq 0 ] || refuse "must run as root: re-run under sudo"
	command -v systemctl >/dev/null 2>&1 ||
		refuse "this host has no systemd; prismotr installs on Debian"
fi

binary_file=/usr/local/bin/prismotr
config_dir=/etc/prismotr
config_file=$config_dir/prismotr.yaml
target_file=$config_dir/target
service_file=/etc/systemd/system/prismotr.service
service_source=$source_dir/systemd/prismotr.service
account=prismotr

[ -f "$service_source" ] || refuse "the service definition is missing: $service_source"

# The examples travel beside this script inside a release; in a checkout the configuration's
# own is the repository's prismotr.yaml.example.
yaml_example=$source_dir/prismotr.yaml.example
[ -f "$yaml_example" ] || yaml_example=$source_dir/../prismotr.yaml.example
env_example=$source_dir/prismotr.env.example
for example in "$yaml_example" "$env_example"; do
	[ -f "$example" ] || refuse "the example configuration is missing: $example"
done

# A directory the layout owns must not be a symlink somebody else planted, and on a real run
# must belong to root.
check_dir() {
	[ ! -L "$1" ] || refuse "a directory the layout owns is a symlink: $1"
	if [ -z "$destdir" ] && [ -d "$1" ] && [ -z "$(find "$1" -maxdepth 0 -user root)" ]; then
		refuse "$1 is not owned by root"
	fi
}

# A file that chooses what root installs — or a directory holding one — must be root's alone:
# no symlink, nothing group or other may write, and on a real run root as its owner.
check_root_only() {
	[ -e "$1" ] || [ -L "$1" ] || return 0
	[ ! -L "$1" ] || refuse "$2 is a symlink, and $3"
	[ -z "$(find "$1" -maxdepth 0 \( -perm -g+w -o -perm -o+w \))" ] ||
		refuse "$2 is writable by group or other, and $3"
	if [ -z "$destdir" ] && [ -z "$(find "$1" -maxdepth 0 -user root)" ]; then
		refuse "$2 is not owned by root, and $3"
	fi
}

create_temp() {
	mkdir -p "$(dirname "$1")"
	temp=$(mktemp "$1.XXXXXXXX")
}

# Copy one file into place with the mode the layout table gives it. The temporary and the
# rename are what let a running daemon's binary be replaced under it.
install_file() {
	create_temp "$3"
	cp "$1" "$temp"
	chmod "$2" "$temp"
	mv "$temp" "$3"
	temp=
	written="$written  $3
"
}

# The SHA-256 of a file, or nothing when it cannot be read.
digest_of() {
	line=$(openssl dgst -sha256 "$1" 2>/dev/null) || return 0
	printf '%s' "${line##* }"
}

# Whether the systemd unit runs a process whose executable has the release's digest: a binary
# replaced under a running service leaves the old bytes behind /proc/<pid>/exe.
systemd_runs_release() {
	pid=$(systemctl show -p MainPID --value prismotr.service 2>/dev/null) || return 1
	[ "${pid:-0}" != 0 ] || return 1
	[ "$(digest_of "/proc/$pid/exe")" = "$digest" ]
}

# Whether the binary in place is the release handed over, as the layout installs it: a regular
# file of that digest, mode 0755 and, on a real run, root's.
binary_is_release() {
	[ ! -L "$destdir$binary_file" ] || return 1
	[ -f "$destdir$binary_file" ] || return 1
	[ "$(digest_of "$destdir$binary_file")" = "$digest" ] || return 1
	[ -z "$(find "$destdir$binary_file" -maxdepth 0 ! -perm 0755)" ] || return 1
	[ -n "$destdir" ] || [ -n "$(find "$binary_file" -maxdepth 0 -user root)" ]
}

# Whether, with binary_is_release already true, the rest of the release is installed and on a
# real run the service is running that binary.
already_running() {
	cmp -s "$service_source" "$destdir$service_file" || return 1
	[ -z "$destdir" ] || return 0
	systemd_runs_release
}

check_dir "$destdir$config_dir"

# A follow run decides from the target before anything is written: it answers with another
# version, finds nothing to change, or goes on to install (installer.md#answering-a-follow-run).
if [ -n "$follow" ]; then
	why="the target chooses what root installs"
	check_root_only "$destdir$config_dir" "$config_dir" "$why"
	check_root_only "$destdir$target_file" "$target_file" "$why"
	[ -f "$destdir$target_file" ] ||
		refuse "there is no $target_file: it holds latest or a version such as 1.2.3"
	lines=$(wc -l <"$destdir$target_file")
	target=$(cat "$destdir$target_file")
	if [ $((lines)) -gt 1 ]; then
		refuse "$target_file holds more than one line"
	fi
	case $target in
	latest) wanted=$newest ;;
	*)
		is_version "$target" || refuse "$target_file holds neither latest nor a version: $target"
		wanted=$target
		;;
	esac

	if [ "$wanted" != "$release" ]; then
		printf '%s\n' "$wanted" >"$answer"
		printf '%s: %s names %s, not this release %s; nothing is installed from it\n' \
			"$program" "$target_file" "$wanted" "$release"
		exit 0
	fi
	if binary_is_release; then
		if already_running; then
			printf '%s: prismotr %s is already installed; nothing to do\n' "$program" "$release"
			exit 0
		fi
		[ -n "$binary" ] ||
			printf '%s: prismotr %s is in place; keeping the binary in place and installing the rest\n' \
				"$program" "$release"
	elif [ -z "$binary" ]; then
		printf '%s\n' "$release" >"$answer"
		printf '%s: %s names this release %s; asking for its binary\n' "$program" "$target_file" "$release"
		exit 0
	fi
fi

# The account the service runs as. Its groups are the provisioning's business.
if [ -z "$destdir" ] && ! id "$account" >/dev/null 2>&1; then
	if command -v adduser >/dev/null 2>&1; then
		adduser --system --group --no-create-home "$account" >/dev/null
	else
		useradd --system --no-create-home --shell /usr/sbin/nologin "$account"
	fi
fi

mkdir -p "$destdir$config_dir"
# Again, because whoever owns the parent could have planted a symlink between the check above
# and this line.
check_dir "$destdir$config_dir"
# The mode is the layout's on every run, staged or not: a directory anyone may write to hands
# over the files inside it whatever their own modes say.
chmod 0755 "$destdir$config_dir"

[ -z "$binary" ] || install_file "$binary" 0755 "$destdir$binary_file"
install_file "$service_source" 0644 "$destdir$service_file"
install_file "$yaml_example" 0644 "$destdir$config_dir/prismotr.yaml.example"
install_file "$env_example" 0644 "$destdir$config_dir/prismotr.env.example"

printf '%s: installed\n%s' "$program" "$written"

# The unit file was replaced, so systemd is told either way; enabling it waits until there is
# a configuration, or a reboot would start a service that cannot run and restart it for ever.
[ -n "$destdir" ] || systemctl daemon-reload

if [ ! -f "$destdir$config_file" ]; then
	printf 'The service will not start until this exists:\n  %s\n' "$config_file"
	printf 'ansible-hosts writes it; by hand, copy the example beside it and fill it in:\n'
	printf '  sudo install -o root -g root -m 0644 %s.example %s\n' "$config_file" "$config_file"
	printf 'Then run this script again.\n'
elif [ -z "$destdir" ]; then
	systemctl enable prismotr.service
	systemctl restart prismotr.service
fi

if [ -n "$destdir" ]; then
	printf 'Staged under %s: no account was created and no service was registered.\n' "$destdir"
fi
printf 'The service state: systemctl status prismotr.service\n'
