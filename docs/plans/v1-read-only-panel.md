# Plan: v1 — the read-only panel

- **Status:** accepted 2026-10-16; progress is tracked in the open pull request
- **Source:** the contract in ansible-hosts, `.github/tasks/task12-prismotr-panel.md`
  (section *Boundary between the two repositories*); this plan derives from it and does not
  restate it. Where the two disagree, the contract wins and this file is corrected.
- **Sibling:** [monitor](https://github.com/pravbeseda/monitor) — the release pipeline, the
  installer contract and the quality gates are copied from it, not re-designed.

One daemon per host: it reads `/etc/prismotr/prismotr.yaml` that ansible-hosts writes, listens
on loopback behind nginx basic auth, and renders the host's site menu plus read-only status
cards. It decides nothing about what is attached. No actions, no fleet view, no hub data.

Each step is test-first (red → green → refactor) and ends green on CI. The contract's step
names (P1, P2, P3) are kept so the two repositories can refer to the same milestones.

## Conventions, fixed before the first commit

- Module `github.com/pravbeseda/prismotr`, one binary `cmd/prismotr`, shared code in
  `internal/`. Nothing is imported from monitor in v1: its code lives under `internal/` and
  cannot be imported; sharing starts when monitor exposes a module, which is monitor's change.
- The repository is public and English, with monitor's hard rules: synthetic names only
  (`host-a`, `example.com`), no deployment values as defaults, no secrets in the tree,
  `.githooks/pre-commit` with the fast pair and `gitleaks`.
- Quality gates as monitor's: `gofmt`, `go vet`, `golangci-lint`, `go test -race -cover`,
  `shellcheck -s sh` over the shell in `deploy/`. A red check blocks the merge.
- Documentation: `docs/index.md` as the map, `docs/decisions/` for ADRs from monitor's
  template, `docs/specs/` for the two contracts an observer can test — the configuration file
  and the installer's answer protocol. `AGENTS.md` holds the agent instructions, `CLAUDE.md`
  is the one-line `@AGENTS.md` stub.
- The interface is English, as the page it replaces. A bilingual interface is a decision for
  later, not a v1 omission to apologise for.

## Steps

### P1 — skeleton

1. **Scaffolding and gates.**
   `go.mod`, `cmd/prismotr` printing `prismotr <version>` on `--version`, `internal/version`
   stamped by `-ldflags -X`, CI workflow with the five gates, pre-commit hook, `AGENTS.md`,
   `docs/index.md`, `LICENSE`. → verify: CI green on a trivial test; the stamped-build test
   from monitor passes.

2. **Configuration loader** (`internal/config`), spec first in `docs/specs/config.md`.
   The schema is the contract's YAML verbatim: `host`, `domain`, `base_path`, `hub_url`,
   `sites[]` with `name`, `kind` (`location` | `vhost`), `title`, `path`, `domain`, `aliases`,
   `cert_domain`, `logs`, `php_fpm_versions`, `order`. Rules: a missing or empty `sites`
   list is an empty menu, never an error; a missing file is a startup error, as a missing
   deployment value is under the rules above — an empty page would hide a provisioning run
   that never wrote it; `host` and `base_path` are required;
   `base_path` is `/` or starts with `/` and has no trailing slash; an unknown `kind` fails
   the load; an unknown key is ignored and logged, so a newer ansible-hosts never crashes an
   older daemon. `SIGHUP` reloads; a file that fails to load on reload keeps the previous
   configuration and logs the error. Ship `prismotr.yaml.example` with synthetic sites.
   → verify: one test per behaviour row; a reload test with a broken file.

3. **Server and shell** (`internal/web`).
   Flags `--config` (default `/etc/prismotr/prismotr.yaml`) and `--listen`; `--listen`
   beats `PRISMOTR_LISTEN`, which beats the default `127.0.0.1:8100`. Every route and every
   rendered link starts with `base_path`; with `base_path: /` nothing is doubled. One
   `html/template` shell: title `<domain or host> - Admin Panel`, a header naming the host,
   graceful shutdown on `SIGTERM`. → verify: handler tests for `base_path` `/` and
   `/prismotr`, flag/env precedence test.

4. **The sites page, with menu parity.**
   Renders the configured list as `admin-panel.html.j2` does today: locations sorted by
   `(order, name)` linking to `path` with `title`, then vhosts sorted the same way linking to
   `//<domain>/`, then the `Main Site` link to `/`. Parity is the done-when of the contract,
   so the two groups stay two groups even though the file carries one list.
   → verify: a golden test against a configuration shaped like a real host's menu, with
   synthetic names; a local run with a hand-written `prismotr.yaml` shows the same menu as
   the static page.

### P2 — release pipeline

5. **`deploy/` after monitor's, trimmed to one role and Linux.**
   Spec first in `docs/specs/installer.md`: the answer protocol of monitor's ADRs 0024,
   0025 and 0027 (`--follow-target --release --newest --answer --digest`, then `--binary`),
   unchanged, because the kept script is frozen on every host that keeps it. Files:
   `prismotr-install.sh` (the kept stub carrying the public key), `install.sh` (the release's
   installer), `verify-release.sh`, `systemd/prismotr.service` (user `prismotr`,
   `EnvironmentFile=-/etc/prismotr/prismotr.env` — optional, it holds no secret and only
   `PRISMOTR_LISTEN` where a host overrides the port; `--config /etc/prismotr/prismotr.yaml`;
   hardened as `monitor-hub.service`), `systemd/prismotr-update.service` and `.timer`
   (hourly, ten-minute spread, persistent), `prismotr.env.example`. Layout:
   `/usr/local/bin/prismotr`, `/usr/local/libexec/prismotr/prismotr-install.sh`,
   `/etc/prismotr/{prismotr.yaml,prismotr.env,target}`. The installer creates no
   configuration and never touches the three files ansible-hosts owns; without
   `prismotr.yaml` it installs everything else, leaves `prismotr.yaml.example` beside the
   missing file, stops short of starting the service and names what is missing, as
   monitor's hub installer does, and a follow run starts only a configured daemon. Go tests drive the
   scripts in a staged root, as monitor's `deploy/*_test.go` do. → verify: the staged suite
   green; `shellcheck` clean.

6. **Signing key and the release workflow.**
   A new ECDSA P-256 key of this project's own: the public half committed as
   `deploy/release-signing-key.pub` and embedded in the stub (a test asserts the two are the
   same bytes), the private half a secret of the `release` environment that admits `v*` tags
   alone. `release.yml` copied from monitor: tag guard, `go test -race`, static builds for
   `linux/amd64` and `linux/arm64`, reproducible installer tarball, signed `SHA256SUMS`,
   self-verification, draft-then-publish with `make_latest=legacy`. `auto-tag.yml` and
   `next-tag.sh` come along so a merge becomes a release unless the pull request says
   `release:none`. → verify: `v0.1.0` publishes its assets; on a Debian 13 container with a
   hand-written `prismotr.yaml` in place, `prismotr-install.sh` installs it and the service
   answers on loopback, and without that file it installs, names the missing file and starts
   nothing; a second run with `--follow-target` installs nothing and reports the version
   already in place.
   → hand-off: the key's fingerprint goes to `sites/prismotr` defaults in ansible-hosts
   (step A2 there); the `release` environment and its tag policy are set by hand in the
   GitHub repository settings, since no file in the tree can.

### P3 — status cards

7. **Card framework** (`internal/status`).
   A card is a source name, the time it was read, and rows. Sources are read per request,
   each under its own short timeout; a source that fails renders its error inside its card
   and never fails the page. No background goroutines and no cache in v1: the page is
   opened by one person a few times a day. → verify: a failing fake source renders an error
   row while the other cards render.

8. **Host card.**
   Uptime and load from `/proc`; disk use per mount from `/proc/mounts` filtered to real
   filesystems; `nginx` and every `php<ver>-fpm` unit named by any site's
   `php_fpm_versions`, through `systemctl is-active`; `http://127.0.0.1/nginx_status` parsed
   when it answers `200`, skipped silently when it does not. → verify: unit tests with fake
   `/proc` files, a fake `systemctl` on `PATH` and a test HTTP server.

9. **Vhost card.**
   Certificate not-after and days left from
   `/etc/letsencrypt/live/<cert_domain>/fullchain.pem`; the last lines of every file in
   `logs`; the PHP versions the site declares. A log the daemon cannot read says so in its
   row — the `adm` membership is ansible-hosts' job, the message is ours. → verify: tests
   with a generated certificate and temp logs, including an unreadable one.

10. **Location card and hub link.**
    The link and title, and whether the path answers over loopback. The probe goes to
    `https://127.0.0.1<path>` with the TLS server name set to `domain` and no certificate
    check — it measures reachability, not identity — and repeats over `http://` only when
    port 443 refuses the connection, as it does on a host without TLS. The literal
    `127.0.0.1`, never `localhost`: on a TLS host the port-80 server answers every path with
    a redirect and answers `::1` with a bare `OK`, so a probe that lands there reports a
    broken location as healthy. Any HTTP status from the server that holds the location
    counts as answering (it is behind basic auth, so `401` is healthy); a refused connection
    on both ports or a timeout does not. The hub card is one link to `hub_url` when it is
    set, nothing when it is not. → verify: handler tests with a TLS test server, a plain
    test server and a closed port, one per outcome.

11. **Manual end-to-end run.**
    A release built from this branch on a Debian 13 container that ansible-hosts has attached
    `drevo.localhost` to (its step A2). → verify: every card above shows live values; a
    detached site disappears from the menu after `SIGHUP`.

## Open questions for the contract

All three decided on 2026-10-16; each leaves one line to change in the contract.

1. **Error logs.** The schema carries `logs` only, the vhost card promises access *and*
   error logs. Provisional: the daemon tails whatever `logs` lists and ansible-hosts lists
   both files. No schema change, and the daemon invents no paths.
   **Decision:** the daemon tails every file in `logs`, each as its own block named by the
   file; ansible-hosts lists the error log beside the access log. The schema stays as the
   contract has it, and the daemon learns nothing about nginx's naming.
2. **The hub link.** `hub_url` is described as the hub's page for this node. Provisional:
   the daemon renders the URL exactly as given, and ansible-hosts composes the node page's
   address — the hub's routes are not this daemon's knowledge.
   **Decision:** rendered as given. `hub_url` holds the full address of this node's page
   on the hub, composed by ansible-hosts, which knows both the hub and the host; a change of
   the hub's routes is an edit to one template there, never a release of this daemon. The
   contract's comment on the key should say so.
3. **Menu order.** The contract says one list sorted by `(order, name)`; the page it must
   match renders locations before vhosts. Provisional: parity wins (step 4). If the contract
   meant one merged list, step 4 drops the grouping and the done-when is re-checked.
   **Decision:** two groups, as the page renders today: locations sorted by
   `(order, name)`, then vhosts sorted the same way, then `Main Site`. The file carries one
   list; grouping by kind is the renderer's one rule, not a discipline of `order` values
   across roles. The contract should say "grouped by kind as the page renders today".

## Out of scope

Actions of any kind (restart, log truncation, renewal) and the sudoers whitelist and audit
log they need; cards fed by the hub's API and the read credential they need; a fleet index;
parsing nginx configuration; macOS builds; a bilingual interface; a background collector or
cache; metrics history of any kind — that is monitor's.

## Done when

A tagged release installs on a Debian 13 container through `prismotr-install.sh` and follows
`/etc/prismotr/target`; the daemon renders, from a configuration shaped like a real host's,
the same menu in the same order as the static page it replaces; every v1 card renders live
values on a container with a vhost attached; CI is green; `docs/index.md`, the two specs and
this plan match what was built.
