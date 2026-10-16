# 0001. Releases are signed and pulled as monitor's are

- **Status:** accepted
- **Date:** 2026-10-16
- **Source:** the contract in ansible-hosts, `.github/tasks/task12-prismotr-panel.md`,
  decisions 4 and 5 and *Installing and following a target*; monitor's ADRs
  [0022](https://github.com/pravbeseda/monitor/blob/main/docs/decisions/0022-updates-are-pulled.md),
  [0024](https://github.com/pravbeseda/monitor/blob/main/docs/decisions/0024-the-hub-follows-a-target-with-a-kept-install-script.md),
  [0025](https://github.com/pravbeseda/monitor/blob/main/docs/decisions/0025-the-hub-checks-hourly-and-downloads-a-binary-to-install-it.md)
  and [0027](https://github.com/pravbeseda/monitor/blob/main/docs/decisions/0027-the-hub-installer-reuses-the-binary-in-place.md)

## Context

The panel is installed on every host that attaches it by the same Ansible repository that
installs monitor's hub, through a role generalised from the hub's. That role writes a target
file, places a kept copy of an install script and a timer, and starts the update service once.
Monitor has already decided how such a script and such an installer behave, proved it with a
staged suite, and frozen the parts a kept script depends on. A second design would cost a
second suite and a second set of frozen rules for the same operator.

## Decision

The release pipeline and the update mechanism are monitor's, copied and trimmed, not
re-designed:

1. **A release is a tag on `main`**, `vMAJOR.MINOR.PATCH`, published by a workflow that builds
   static Linux binaries for `amd64` and `arm64` and an installer archive, writes one
   `SHA256SUMS` over them and signs it with ECDSA P-256, with a key of this project's own. A
   merge tags itself unless the pull request says `release:none`.
2. **The kept script is `deploy/prismotr-install.sh`**, run by `prismotr-update.timer` as
   `prismotr-install.sh --follow-target`, hourly. It carries the public key, fetches and
   verifies the newest release, and hands over to the installer that release carries.
3. **The installer's answer protocol is monitor's, unchanged**: `--follow-target --release
   --newest --digest --answer`, then `--binary` once asked; a target of `latest` or one
   version in `/etc/prismotr/target`; at most three hand-overs; a binary downloaded only when
   an installer asks; a binary in place with the release's digest kept.
4. **One role, one binary, Linux only.** The archive holds one `install.sh`, the service
   definitions and the examples; there is no dispatcher and no second installer.
5. **The installer creates no configuration and starts nothing without one.** Without
   `/etc/prismotr/prismotr.yaml` it installs everything else, leaves the example beside the
   missing file and names it. It creates the `prismotr` system account when there is none,
   and touches no group: which groups the account joins is the provisioning's decision.

## Consequences

- What a kept script depends on is frozen from the first host that keeps one: the options
  above, the answer's meaning, the asset names, the manifest format, the archive's shape and
  the update unit's command line. Changing one is replacing the script on every host.
- Monitor's staged suite is ported with the scripts, so the rules are proved here too and a
  divergence from monitor is a deliberate edit to a test, not drift.
- A change monitor makes to its mechanism does not reach this project by itself; porting it
  is a decision of its own.
- The key's fingerprint is the `prismotr` entry of `signed_release_key_fingerprints` in
  `roles/signed_release` of ansible-hosts, which is where an operator checks a downloaded
  script against something outside itself.

## Alternatives

- **Share the scripts with monitor through a Go module** — rejected: shell cannot be imported,
  and a release must carry its own installer; what can be shared is the design, which is
  what this record does.
- **A simpler updater of its own** — rejected: it would be a second verifier and a second
  frozen contract for the same operator and the same Ansible role, for a project whose
  threat model is the hub's.
- **Install through apt** — rejected by monitor's 0022 for the same reasons here.
