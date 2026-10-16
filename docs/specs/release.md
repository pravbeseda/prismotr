# Spec: Release

- **Status:** approved 2026-10-16 (reviewed and in force)
- **Owns:** `.github/workflows/release.yml`, `.github/workflows/auto-tag.yml`,
  `deploy/verify-release.sh`, `deploy/tag-version.sh`, `deploy/next-tag.sh`,
  `deploy/release-signing-key.pub`, and the version string the binary reports
- **Decisions:** [0001](../decisions/0001-releases-are-signed-and-pulled-as-monitors-are.md)

## Purpose

A tag turns a commit into binaries a host can install without a Go toolchain, and into a
signature that says which binaries this repository released under that version. A merge into
`main` tags itself. This spec is monitor's release spec trimmed to one binary and Linux.

## What a release contains

The version is the tag without its leading `v`. Two binaries and the installer archive, one
manifest listing all three, and one signature over it — five files.

| Asset | Why |
|---|---|
| `prismotr-<version>-linux-amd64` | the hosts |
| `prismotr-<version>-linux-arm64` | the hosts |
| `prismotr-installer-<version>.tar.gz` | the installer that release installs itself with ([installer.md](installer.md)) |
| `SHA256SUMS` | one `<sha256>  <asset name>` line per asset above |
| `SHA256SUMS.sig` | the signature over that manifest |

The manifest is what is signed, so an asset is genuine only under the name this release
gave it.

### The key

A signature is ECDSA P-256 over SHA-256, made and verified by `openssl dgst`. The public half
lives in the repository as `deploy/release-signing-key.pub` and inside `prismotr-install.sh`.
Its fingerprint, the SHA-256 of the DER encoding, is what ansible-hosts carries as the
`prismotr` entry of `signed_release_key_fingerprints` in `roles/signed_release`, its one
registry of release keys, and what an operator checks a downloaded script against:

```
889cfd8d32f832f9b60baa40a49ccb8ab64a153d8ffb4c07d959a54dde4cd9c4
```

The secret is the PEM itself, named `RELEASE_SIGNING_KEY`, in a GitHub environment called
`release` whose deployment policy admits `v*` tags alone. Both are repository settings, made
once:

```sh
gh api --method PUT repos/pravbeseda/prismotr/environments/release \
    -F "deployment_branch_policy[protected_branches]=false" \
    -F "deployment_branch_policy[custom_branch_policies]=true"
gh api --method POST repos/pravbeseda/prismotr/environments/release/deployment-branch-policies \
    -f name='v*' -f type=tag
gh secret set RELEASE_SIGNING_KEY --env release < release-signing-key.priv
```

A rotation is a new pair, a new `.pub` committed and embedded, a new secret, and a new kept
script on every host. There is no second copy of the private half.

```sh
openssl ecparam -name prime256v1 -genkey -noout -out release-signing-key.priv
openssl ec -in release-signing-key.priv -pubout -out deploy/release-signing-key.pub
```

## Behaviour

One row = one test. Anchors: `spec: release.md#<heading>`. The publishing and tagging rows
describe what a run does on GitHub and are proved by a run; the pieces with logic of their
own — `tag-version.sh`, `next-tag.sh`, `verify-release.sh` — are tested here.

### Publishing

| Event | Outcome |
|---|---|
| tag `v1.2.3` pushed | a release named `v1.2.3` appears, carrying the two binaries, the installer archive, `SHA256SUMS` and `SHA256SUMS.sig`, and no client sees it before all five are attached |
| two tags pushed together, their runs finishing in either order | the higher version is the latest release |
| tag `v1.2` or `v1.2.3-rc1` pushed, or a part wider than nine digits | no release; the run fails, naming the tag |
| a tag on a commit that is not on `main` | no release; the run fails, naming the commit |
| a tag on a commit that does not build or whose tests fail | no release |
| a tag with a published release already | the release keeps every asset; the run fails |
| a draft left by an earlier run | it is deleted, and this run publishes its own |
| the committed public key stops matching the signing secret | no release: the run verifies its own manifest with the committed key first |
| the workflow started on a ref that is not a tag | no release; the run fails, naming the ref |

### Tagging a merge

A push to `main` is tagged by `auto-tag.yml`, which then starts the release workflow for that
tag. A pull request chooses how far the version moves with at most one label; without one, a
merge is a patch release. The labels are repository settings, made once:

```sh
gh label create release:minor --description "Merging releases the next minor version"
gh label create release:major --description "Merging releases the next major version"
gh label create release:none --description "Merging releases nothing"
```

`gh workflow disable auto-tag.yml` switches to releasing by hand; a tag pushed by hand
releases either way. A merge that changes only files under `docs/` or `.md` files is not
tagged.

`deploy/next-tag.sh [label...]` reads the repository's tags on stdin and prints the tag the
next release takes. **The exit status is the verdict.**

| Given | Outcome |
|---|---|
| tags `v1.2.3` and `v1.10.0`, no labels | prints `v1.10.1`: versions compare part by part as numbers |
| tag `v1.2.3`, label `release:minor` | prints `v1.3.0` |
| tag `v1.2.3`, label `release:major` | prints `v2.0.0` |
| tag `v1.2.3`, label `release:none` | prints nothing and exits 0, with the pipe feeding it unbroken |
| tag `v1.2.3`, labels that do not start with `release:` beside `release:minor` | prints `v1.3.0` |
| tag `v1.2.3`, label `Release:Minor`, or `release:minor` given twice | prints `v1.3.0` |
| tag `v1.2.3`, labels `release:minor` and `release:major` | exits non-zero, naming both |
| tag `v1.2.3`, label `release:patch` or `release:` | exits non-zero, naming the label |
| no tags, with no label, `release:minor` or `release:major` | prints `v0.0.1`, `v0.1.0` or `v1.0.0` |
| lines that name no version beside `v1.0.0` | ignored: prints `v1.0.1` |
| a tag whose next part would be too wide to be a version | exits non-zero, naming the tag it could not make |

`deploy/tag-version.sh <tag>` prints the version a tag names, or refuses it: `vMAJOR.MINOR.PATCH`,
each part a number of at most nine digits with no leading zero, and nothing else.

### The version a binary reports

| Given | Outcome |
|---|---|
| a binary from release `v1.2.3`, asked with `--version` | it answers `prismotr 1.2.3`, before it needs any configuration |
| a binary built from a checkout | it reports the development default, `0.0.0`, and releasing edits no source file |

### Verifying an artifact

`deploy/verify-release.sh [--key <path>] [--sums <path>] <artifact>` — the manifest defaults
to `SHA256SUMS` beside the artifact, its signature to that name plus `.sig`, and the key to
`release-signing-key.pub` beside the script. **The exit status is the verdict**, and a failure
that reached the signature or the digest names the key and the manifest it used.

| Given | Outcome |
|---|---|
| an asset, manifest and signature as published, with or without `--key` and `--sums` | exits 0 |
| an asset changed by one byte, or renamed to another asset's name | exits non-zero, saying its digest is not the one the release named |
| an artifact the manifest does not list | exits non-zero, naming it |
| a manifest changed after signing, signed by another key, or a signature file that is empty or not a signature | exits non-zero, saying the signature does not verify |
| no manifest, or no signature beside it | exits non-zero, naming the file it wanted |
| `--key` naming a path that does not exist, or a file that is not a key | exits non-zero, naming the path, and not as a bad signature |
| more than one artifact named, or none, or a directory | exits non-zero, printing the usage |
| no `openssl` on `PATH` | exits non-zero, naming `openssl` |
| `-h` | prints the usage on stdout and exits 0 |

The verdict comes from `openssl`'s exit status and never from its output: LibreSSL prints
`Verified OK` on runs that fail.

## Invariants

- A release is published whole or not at all.
- The private key never leaves the release environment's secrets: no job that does not sign
  sees it, it is written outside the workspace and removed whatever the outcome.
- Every action the release workflow runs is pinned by commit.
- The version a published binary reports equals the tag without its `v`.
- An existing published release is never rewritten by a run: a fix is a new tag.
- The archive is packed reproducibly: the same commit gives the same bytes.

## Out of scope

What is inside the installer archive and what installing does: [installer.md](installer.md).

## Open questions

None.
