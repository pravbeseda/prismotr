# prismotr — Documentation Map

The per-host hosting panel. This file is the entry point to every document in the project.

## Plan

| Document | Contents |
|---|---|
| [../AGENTS.md](../AGENTS.md) | Agent instructions: commands, the boundary with ansible-hosts, the public-repository rules, the process |
| [plans/v1-read-only-panel.md](plans/v1-read-only-panel.md) | The v1 plan — skeleton, release pipeline, status cards — derived from the contract in ansible-hosts, with its open questions and their decisions |

## Architecture decisions

One decision per file, each stating what was rejected and why. New records start from
[TEMPLATE.md](decisions/TEMPLATE.md) with the next free number.

| # | Decision | Status |
|---|---|---|
| [0001](decisions/0001-releases-are-signed-and-pulled-as-monitors-are.md) | Releases are signed and pulled as monitor's are | accepted |

## Behaviour specs

One table per subsystem; each test cites the row it covers. New specs start from
[TEMPLATE.md](specs/TEMPLATE.md).

| Spec | Owns | Status |
|---|---|---|
| [config.md](specs/config.md) | `internal/config`: the file ansible-hosts writes, its validation, reload | draft |
| [installer.md](specs/installer.md) | `deploy/prismotr-install.sh` and the installer a release carries: fetching, verifying, installing, following a target | draft |
| [release.md](specs/release.md) | The release and tagging workflows, the signing key, `verify-release.sh`, `tag-version.sh`, `next-tag.sh` | draft |
