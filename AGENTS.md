# prismotr

The per-host hosting panel: one static Go binary per host that renders the host's site menu
and read-only status cards. It reads a configuration file that
[ansible-hosts](https://github.com/pravbeseda/ansible-hosts) writes and decides nothing about
what is attached. The fleet view and all metrics belong to
[monitor](https://github.com/pravbeseda/monitor); the release pipeline, the installer
contract and the quality gates are copied from it.

## Commands

```
go test -race -cover ./...   # tests
gofmt -l . && go vet ./...   # the fast pair, also run by the pre-commit hook
golangci-lint run            # the full linter set, also run by CI

go run ./cmd/prismotr --version
go run ./cmd/prismotr --config prismotr.yaml.example --listen 127.0.0.1:8100
kill -HUP <pid>                  # reload the configuration file
```

**Where the project stands is not written here.** The plan in
[docs/plans/v1-read-only-panel.md](docs/plans/v1-read-only-panel.md) owns that, together
with the checklist in the open pull request.

## Start here

**Read `docs/index.md` first.** It maps every document.

```
docs/index.md              map of all documentation — the entry point
docs/plans/                the work plan, derived from the contract in ansible-hosts
docs/decisions/NNNN-*.md   ADRs, one decision per file (TEMPLATE.md is the skeleton)
docs/specs/<subsystem>.md  behaviour specs: the table each test derives from
```

Documents are cross-linked with **relative Markdown links**, never Obsidian wikilinks.

## The boundary with ansible-hosts

The contract lives in ansible-hosts, `.github/tasks/task12-prismotr-panel.md`, section
*Boundary between the two repositories*. In short:

| prismotr owns | ansible-hosts owns |
|---|---|
| the binary, its templates, `--version`, `--listen`, `--config` | `/etc/prismotr/prismotr.yaml`, `/etc/prismotr/prismotr.env`, `/etc/prismotr/target` |
| `deploy/`: installer, signed manifest, systemd units, `prismotr.env.example` | the nginx location, the auth snippet, the `default` vhost |
| reading local state (systemd, disks, logs, certificates) | which host gets the panel, under which path, pinned to which version |

The daemon parses no nginx configuration and invents no paths: what it shows is what the
file lists. Where this repository and the contract disagree, the contract wins and the plan
here is corrected.

## This repository is public — hard rules

Publishing history is irreversible, so these rules bind every commit, commit message, issue
and pull request:

1. **Never commit** real host names, domains, addresses, log excerpts or screenshots of a
   live panel. Examples and fixtures are synthetic: `host-a`, `example.com`, round numbers.
2. **Product defaults may ship; deployment settings may not.** The listen address has a
   default because it says nothing about an installation; the configuration file has none:
   a missing one is a startup error, never a "reasonable value".
3. **Secrets never enter a file in the tree**, example files included.

**The scanner enforces this, not attention.** `.githooks/pre-commit` refuses a commit that
stages a configuration, an environment file or key material, and runs `gitleaks` over the
staged diff. A fresh clone enables it once:

```
git config core.hooksPath .githooks
brew install gitleaks go golangci-lint shellcheck   # macOS
```

## Language

The repository is English — code, identifiers, comments, documentation, config keys, commit
messages, CLI output, issue and PR text. The interface is English too, as the page it
replaces; a bilingual interface is a decision for later.

## Quality gates

CI runs `gofmt`, `go vet`, `golangci-lint`, `go test -race -cover` and `shellcheck -s sh`
over the shell this repository ships, and a red check blocks the merge. The pre-commit hook
runs the fast pair only. A branch may hold broken work; a merge may not.

## Development process

1. **Spec before code** for the two contracts an observer can test — the configuration file
   and the installer's answer protocol — in `docs/specs/`. Everything else is spec-exempt and
   says so.
2. **Test-first, step by step**: red → green → refactor. A test for a spec row cites it with
   a `spec:` anchor.
3. **Work on a branch off `main`.** Never commit to `main`; never open or merge a pull
   request without the user's explicit permission for that action.
4. **An architectural decision is an ADR** from `docs/decisions/TEMPLATE.md` with the next
   free number and a row in `docs/index.md`. Architectural means reversing it later would
   mean rewriting code rather than editing it. A reversal is a new ADR, never a rewrite.
5. **Documentation is part of the work**: a new document is added to `docs/index.md`, a
   changed behaviour updates the document that describes it, and the plan's step is ticked in
   the pull request. Report at the end of a task which documents were touched.

## Architecture in one screen

- **One binary, `cmd/prismotr`**, shared code in `internal/`. Nothing is imported from
  monitor: its code lives under `internal/` there and cannot be.
- **Configuration is read, never decided**: `/etc/prismotr/prismotr.yaml` at startup and on
  `SIGHUP`; a reload that fails keeps the previous configuration.
- **Loopback only, nginx holds the perimeter**: the daemon listens on `127.0.0.1:8100`
  unless `PRISMOTR_LISTEN` or `--listen` says otherwise; basic auth is nginx's.
- **Every route and link starts with `base_path`**, so one binary serves `/` on one host
  and `/prismotr` on another.
- **Read-only and unprivileged**: the daemon runs as `prismotr`, member of `adm`; each
  status card names its source and when it was read; a source that fails renders its error
  in its card and never fails the page.
- **Releases are signed and pulled**, as monitor's are: a kept copy of the install script
  follows `/etc/prismotr/target` hourly, and the installer's answer protocol is frozen.
