# Spec: Installer

- **Status:** draft
- **Owns:** `deploy/prismotr-install.sh` — the kept script, and the one an operator runs — and
  the contents of the installer archive a release carries: `install.sh`, the service
  definitions and the examples
- **Decisions:** [0001](../decisions/0001-releases-are-signed-and-pulled-as-monitors-are.md)

## Purpose

One command puts the panel on a host, and the same command upgrades it. A kept copy of that
command, run by a timer, keeps the binary on the version `/etc/prismotr/target` names. This
spec owns what the command does and what it refuses, in both uses. It is monitor's installer
spec trimmed to one role and Linux; the rules it keeps are the ones a kept script depends on.

## The two halves

**`prismotr-install.sh` is what the operator runs and what the timer runs.** It downloads a
release, checks it, unpacks it and hands over; it decides nothing else. It carries the release
signing key, because a release cannot vouch for itself. That copy is the one in
`deploy/release-signing-key.pub`; a test asserts they are the same bytes. Beyond the shell it
needs `curl`, `openssl`, `tar` and `find`, and it names whichever is missing. The whole script
is one function, invoked on its last line, so a transfer cut short executes nothing.

**The installer travels inside the release**, as `prismotr-installer-<version>.tar.gz` listed
in that release's signed manifest ([release.md](release.md) owns the asset). It holds one
directory with `install.sh`, `systemd/` and the two examples, laid out as `deploy/` is.

**An asset is named, not searched for.** The run builds each asset's name from the version
and the platform — `prismotr-<version>-linux-<arch>` — and requires the manifest to list
exactly that name.

**What is fetched is what is checked.** The kept script downloads the manifest, its
signature, the archive and the binary, and verifies each before it is used; a follow run
downloads the binary last, only once the installer asks for it. The installer downloads
nothing and verifies nothing.

## The handover

```
sh <unpacked>/install.sh --binary <verified path>
```

- stdin is `/dev/null`: the installer reads nothing.
- The environment is inherited, `DESTDIR` among it. The run sets `LC_ALL`, `umask` and, on a
  real run, `TMPDIR`.
- The run exits with the installer's status and passes its output through.

A follow run hands over with five options and, at first, no binary:

```
sh <unpacked>/install.sh --follow-target --release <this release's version> \
    --newest <newest version> --digest <sha256 of this release's binary> \
    --answer <file> [--binary <verified path>]
```

- `--answer` names an empty file. An installer that exits 0 and leaves it empty has nothing
  further for the run to do. One that writes into it installs nothing and exits 0; what it
  writes is one `MAJOR.MINOR.PATCH`, a trailing newline allowed. Another release's version
  asks for that release; its own `--release` asks for this release's binary. A non-zero exit
  is a refusal whatever the file holds.
- `--binary` is given only after an installer asked for it, with the same five options, and
  an installer handed it answers nothing: an answer then stops the run.
- `--newest` is how `latest` is resolved without asking the origin; `--digest` is how the
  installer tells, without the binary, whether the binary in place is already this release's.

**Frozen from the first kept script on**: these options and the answer's meaning, at most
three hand-overs, the handover form, the two URL shapes of [Where a release is fetched
from](#where-a-release-is-fetched-from), the asset names and manifest format of
[release.md](release.md), the archive being one directory holding `install.sh`, the
signature algorithm, and the command line the update unit starts: `--follow-target`.

## Behaviour

One row = one test. Anchors: `spec: installer.md#<heading>`. **Every refusal exits non-zero
and says why on stderr.** The rows are tested against a synthetic release served over
loopback, using the seams of [Staged installs](#staged-installs).

Some rows a staged suite cannot reach, because staging is defined as not doing those things:
creating the account, telling systemd about the unit and starting the service, refusing a
host with no systemd, ignoring an inherited `TMPDIR`, treating a binary owned by anyone but
root as replaceable, refusing a target owned by anyone but root, and telling a running daemon
on the release's binary from a stopped one. They are proved on a real host.

### Fetching and checking a release

| Event | Outcome |
|---|---|
| a run | the binary in place is the one the newest release names for this platform, and its version is that release's |
| `--version 1.2.3` | that release is used instead of the newest, and the origin is not asked what the newest is |
| `--version` that is not `MAJOR.MINOR.PATCH`, or whose component is wider than a shell compares as a number | nothing is fetched, and the run names the value it refused |
| `--version` naming a release that does not exist | nothing is installed, and the run names the version |
| a manifest whose signature does not verify, a manifest signed by another key, a signature file that is empty or not a signature | nothing is unpacked and nothing is installed |
| an artifact whose digest is not the one the manifest names | the same |
| a release whose manifest lists no installer archive | nothing is installed, and the run says that release predates the installer |
| a release with no binary for this platform | nothing is installed, and the run names the asset it wanted |
| the release cannot be reached | nothing is installed and the host is left as it was |
| a version older than the one the installed binary reports | nothing is installed, and the run says so; `--allow-downgrade` installs it |
| `1.10.0` against an installed `1.9.0` | it installs: versions compare number by number |
| nothing installed, or a binary that will not run or reports something that is not a version | it installs, and says it could not tell what was there |
| an installed binary that another account could have replaced — it or its directory writable by group or other | its version is not read at all, and the run says so and installs |
| an option this run does not know, or one given without its value | the usage on stderr |
| no `curl`, `openssl`, `tar` or `find` on `PATH` | the run names the one that is missing |
| a run that installs anything | it prints the version it installed and the command that shows the service's state |
| `-h` | the usage on stdout, exit 0 |

### Unpacking

| Event | Outcome |
|---|---|
| any run | everything downloaded lands in a directory this run made for itself, readable by nobody else, and gone when the run ends — on success, on a refusal and on `INT`, `TERM` or `HUP` |
| an inherited `TMPDIR` | a real run ignores it; only a staged run honours it |
| an archive holding an absolute path, a `..` path, or anything but a file or a directory | nothing is extracted and nothing is installed |
| an archive whose entries are not one top-level directory holding `install.sh` | nothing is installed |
| an archive whose files carry an owner, a setuid or a setgid bit | what lands takes none of them |

### Installing

What `install.sh --binary <path>` does.

| Event | Outcome |
|---|---|
| a run on a host with no panel and no configuration | the binary, the service definition and the two examples of [the layout](#the-layout) are in place, and the service is installed but not started |
| the same run | it names `prismotr.yaml` as missing and prints the command that installs the example as the real one |
| a run where `prismotr.yaml` is present | the service ends up running on the new binary; `prismotr.env` is optional |
| a run where an older panel is installed and configured | the binary and the service definition are replaced, the configuration is untouched, and the service comes back on the new binary |
| a `prismotr` account that already exists | it is used unchanged, whatever its groups |
| `/etc/prismotr` that is a symlink | nothing is installed, and the run names the path |
| examples the operator has edited | they are overwritten; the examples are the release's |
| a real run on a host with no systemd | nothing is installed |
| `--binary` missing, naming no file, or naming a file that is not executable | nothing is written, and the run names it |

### Following a target

`prismotr-install.sh --follow-target` is what `prismotr-update.service` runs from the kept
copy. The kept script reads no target itself; the target is `/etc/prismotr/target`, holding
exactly `latest` or one `MAJOR.MINOR.PATCH`, a trailing newline allowed. The rows of the two
sections above hold for each release a follow run fetches.

| Event | Outcome |
|---|---|
| target `latest`, or naming the newest version | the newest release is installed |
| target naming an older release | that release is installed, even over a newer one in place: a target is a deliberate choice, and the downgrade guard judges the newest release alone |
| the newest release older than the binary in place | nothing is installed whatever the target, and the run says so |
| any follow run | the newest release's manifest and installer are downloaded and verified first, even when the target names another, and no binary is downloaded before an installer asks for it |
| a daemon already running the release its target resolves to | no binary is downloaded, and nothing is installed |
| a binary in place that is already the release its target resolves to, but the service definition differs | no binary is downloaded, and the rest is installed around the binary in place |
| an installer that asks for its own release's binary | that binary is downloaded once, checked against the manifest this run already verified for that release, and handed over with the same options |
| an installer handed its binary that answers anyway | nothing is installed, and the run says that installer answered after it was given its binary |
| target naming a release whose manifest lists no installer archive | nothing is installed, and the run says that release predates the installer |
| target naming a release whose installer does not take this hand-over | nothing is installed, and that installer's refusal is passed through |
| target naming a release that cannot be fetched once the newest one was verified | nothing is installed, and the run names the version |
| a hand-over to the release an installer named | it runs that release's installer, given that release's own `--digest`, the same newest version and an empty answer |
| the named release's installer names a version other than its own | nothing is installed, and the run names the version it followed and the one named after it |
| an answer that is not one `MAJOR.MINOR.PATCH` | nothing more is fetched, nothing is installed, and the run names the answer |
| an installer that exits 0 leaving the answer empty | the run ends with that installer's output and adds no claim of its own |
| `--follow-target` with `--version` or `--allow-downgrade` | the usage on stderr |

### Answering a follow run

What `install.sh` does when it is handed the five options.

| Event | Outcome |
|---|---|
| a target that resolves to `--release`, with the daemon already running that release — the binary in place is a regular file with the digest `--digest` names, mode 0755 and, on a real run, root's; the service definition is the release's; on a real run the service runs that binary | nothing is written, the answer stays empty, the run says the panel is already at that version, and no service command runs |
| a target that resolves to `--release`, the binary in place already that release but the rest not so, and no `--binary` | the answer stays empty, the run says it keeps the binary in place, and it installs as [Installing](#installing) says with that binary untouched and its path not among those printed |
| a target that resolves to `--release`, the binary in place not that release — another binary, a symlink, a mode that is not 0755, or none — and no `--binary` | the answer holds `--release`, nothing is written outside it, and the exit is 0 |
| a target that resolves to `--release`, the daemon not already running it, with `--binary` | it installs as [Installing](#installing) says and the answer stays empty |
| a target that resolves to another version | the answer holds that version, nothing is written outside it, and the exit is 0 |
| target `latest` | it resolves to `--newest` |
| no target file | nothing is installed, and the run names the file and the two forms it may take |
| a target that is empty, holds anything beside the value and one trailing newline, or holds neither `latest` nor `MAJOR.MINOR.PATCH` | nothing is installed, and the run names the file |
| the target or `/etc/prismotr` a symlink, writable by group or other, or on a real run owned by anyone but root | nothing is installed, and the run names the path: the target chooses what root installs |
| only some of the five options | nothing is installed, and the run names what is missing |
| `--release` or `--newest` that is not `MAJOR.MINOR.PATCH`, `--digest` that is not 64 lowercase hexadecimal digits, or `--answer` naming no file or a file that is not empty | nothing is installed, and the run names the value |

### Staged installs

| Event | Outcome |
|---|---|
| `DESTDIR` naming an absolute path that is not `/` | everything is staged under it and read from under it, the target and the binary in place included; no account is created, and no service command runs |
| `DESTDIR` relative, or naming a path that resolves to `/` — `/`, `//`, `/.`, `/etc/..` | nothing is written, and the run says why |
| no `DESTDIR` and not root | nothing is written, and the run says it needs root |
| `PRISMOTR_RELEASE_ORIGIN` or `PRISMOTR_RELEASE_KEY` in a run that is not staged | nothing is fetched, and the run names the variable it refused, before the check for root |
| either of them in a staged run | honoured: the release is fetched from that origin and checked with that key |

## The layout

| What | Path | Owner, mode | Placed by |
|---|---|---|---|
| binary | `/usr/local/bin/prismotr` | root, 0755 | the installer |
| service | `/etc/systemd/system/prismotr.service` | root, 0644 | the installer |
| configuration directory | `/etc/prismotr` | root, 0755 | the installer |
| configuration example | `/etc/prismotr/prismotr.yaml.example` | root, 0644 | the installer |
| environment example | `/etc/prismotr/prismotr.env.example` | root, 0644 | the installer |
| configuration | `/etc/prismotr/prismotr.yaml` | root, 0644 | ansible-hosts |
| environment, optional | `/etc/prismotr/prismotr.env` | root, 0644 | ansible-hosts |
| target | `/etc/prismotr/target` | root, 0644 | ansible-hosts |
| kept script | `/usr/local/libexec/prismotr/prismotr-install.sh` | root, 0755 | ansible-hosts |
| update service and timer | `/etc/systemd/system/prismotr-update.service`, `prismotr-update.timer` | root, 0644 | ansible-hosts |

The daemon runs as the system account `prismotr`. The installer creates it when it is
missing and never changes its groups: membership of `adm`, which reads nginx's logs, is the
provisioning's decision ([0001](../decisions/0001-releases-are-signed-and-pulled-as-monitors-are.md)).
No file here holds a secret, so no mode is corrected and the environment file is optional.

## Invariants

- No code from a release runs, and no file from one is installed, before its digest and the
  manifest's signature have been checked — with the key the kept script carries, or with the
  one `PRISMOTR_RELEASE_KEY` names, which only a staged run accepts.
- The installer downloads nothing and invents no configuration.
- Nothing is written outside the run's own directory until every check has passed; after
  that, a failure stops and names what it wrote, and does not roll back.
- A second run against the same bytes leaves every installed file byte-identical and the
  service running.
- The downgrade guard is protection against an operator's slip, never against an attack.

## Edge cases

- **A broken newest installer blocks every target**, rollback included. The remedy is a fixed
  release, or installing by hand with `--version`.
- **A daemon that never counts as running** — unconfigured, crash-looping, or stopped by
  hand — is installed again around the binary in place at every run, and a configured one is
  started again. A daemon stopped for maintenance stays stopped only with its timer stopped.
- **Rotating the signing key** touches the secret in the release environment,
  `deploy/release-signing-key.pub`, the copy inside `prismotr-install.sh`, the fingerprint in
  [release.md](release.md) and in ansible-hosts, and the kept script on every host; the
  in-repository copies are held in step by tests.
- **A signature does not prove freshness.** Whoever answers for the origin can serve a
  genuine older release for ever; the newest release's downgrade guard is what stops it
  rolling the fleet back.

## Where a release is fetched from

The origin is one base URL, a product default naming this repository: the newest release is
the version the origin's `releases/latest` answers with, and an asset is
`<origin>/releases/download/v<version>/<asset name>`. A staged run may point both at another
base.

## Out of scope

Placing the kept script, its units, the target, the account's groups and the nginx location:
ansible-hosts. What a release contains and how it is signed: [release.md](release.md).

## Open questions

None.
