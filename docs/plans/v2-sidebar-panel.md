# Plan: v2 — the sidebar panel

- **Status:** proposed 2026-10-17
- **Source:** the design review of 2026-10-17 (variant 3, "Sidebar console", of ten dark
  mockups). The configuration file and the contract with ansible-hosts do not change.
- **Builds on:** [v1-read-only-panel.md](v1-read-only-panel.md); everything it put out of
  scope stays out.

The page becomes an app shell: a sidebar with the host and its sites, an overview of the
host's vital signs, and a site opened inside the panel. On a wide screen a site opens in a
frame and the sidebar folds into a narrow rail, so the panel is one click away. On a phone
the sidebar is a drawer and a site opens in a new tab.

## Decisions, fixed before the first commit

- **Every site opens in a frame** on a wide screen, locations and vhosts alike. A site that
  refuses framing (`X-Frame-Options`, `frame-ancestors`) or loses its login in a third-party
  frame shows the browser's own error; the rail's ↗ opens it in a new tab. No probe detects
  this in advance.
- **A frame is a route, not a script**: `<base_path>/site/<name>` renders the shell with the
  rail and an `<iframe>` pointing at the site's menu link; `<base_path>/main` does the same
  for `Main Site`. A reload or a copied link keeps the site open. An unknown name is `404`;
  with duplicate names the first in menu order wins, as the config spec allows them.
- **No JavaScript.** The rail's "show the menu" and the phone drawer are CSS-only toggles;
  the rail's reload is a link to the same route; ↗ is `target="_blank"` to the site's root.
  `shortcut: reload and ↗ return to the site's root, not the page inside the frame — a
  cross-origin frame cannot be read; revisit if that annoys in daily use.`
- **Wide or narrow is CSS**: each site's menu entry renders two links, the frame route and
  the site itself with `target="_blank"`, and a media query at 720px shows one.
- **Dark only**, the mockup's palette. Fonts (Space Grotesk, JetBrains Mono, both OFL) are
  embedded in the binary and served under `<base_path>/static/`: the panel makes no request
  outside the host. One variable Latin `woff2` per family, unmodified as published (53 KB
  together), with the families' `OFL.txt` beside them, as the licence requires.
- **Cards carry state.** A row gains a state (`ok`, `warn`, `err`) and an optional used
  fraction for a meter; the overview's tiles, the sidebar's dots and the *Needs attention*
  block are all derived from row states, never computed in the template. Thresholds are
  product defaults: disk and memory `warn` at 80%, `err` at 90%; load `warn` above the core
  count; certificate `warn` under 21 days, `err` under 7; an inactive unit, a failed probe
  or a source error `err`; an unreadable log `warn`.
- **Status pages stay spec-exempt**, as v1's cards were: what an observer can test here is
  the configuration, and it does not change.

## Steps

Each step is test-first and ends green on CI.

1. **Row state and meters** (`internal/status`).
   `Row` gains `State` and `Used`; each v1 source sets them by the thresholds above. A
   page-level helper returns the worst state per card and the list of rows that need
   attention. → verify: one test per threshold edge; a failing source yields `err`.

2. **Memory and cores in the host card.**
   `MemTotal`, `MemAvailable`, `SwapTotal`, `SwapFree` from `/proc/meminfo`; the core count
   from the `processor` lines of `/proc/cpuinfo`; load compared against it; uptime also as
   the boot time. → verify: fake `/proc` files, including a missing `/proc/meminfo`, which
   errors in the card and leaves the rest of it.

3. **The shell and the overview** (`internal/web`).
   One layout template: sidebar (host, Overview, Sites in v1's menu order, Main Site, the hub
   link when `hub_url` is set, version and read time), *Needs attention* when any row is
   not `ok`, four tiles (uptime, load against cores, memory, fullest disk), Services and
   Disks, then one card per site with its logs in a closed `<details>`. Embedded CSS and
   fonts under `<base_path>/static/`. → verify: the golden test for `base_path` `/` and
   `/prismotr`; menu order still matches v1's rule; no URL in the page leaves the host
   except the sites and `hub_url`.

4. **The site route and the rail.**
   `<base_path>/site/<name>` and `<base_path>/main`: the rail (Overview, one monogram per
   site with its state dot, reload, ↗, show the menu) beside a full-height `<iframe>`.
   → verify: handler tests for a location, a vhost, `Main Site`, an unknown name (`404`)
   and both base paths.

5. **Phone layout.**
   Below 720px: a top bar with the host and the attention count, the sidebar as a CSS-only
   drawer, the sites' direct links shown instead of the frame routes. → verify: a manual
   check at 390px wide; the golden test holds both links per site.

6. **End-to-end run.**
   A release built from the branch on the Debian 13 container of v1's step 11.
   → verify: nginx passes `<base_path>/site/…` and `<base_path>/static/…` to the daemon (v1
   served `<base_path>` alone; an exact-match location there is a contract change in
   ansible-hosts); every tile shows live values; Munin opens in the frame; a vhost that sends
   `X-Frame-Options: SAMEORIGIN` shows the browser's error and ↗ opens it; if the default
   vhost refuses framing of its own locations, that is an issue in ansible-hosts, filed with
   the header it sends.

## Out of scope

Detecting sites that refuse framing; tracking the page inside a frame; any JavaScript; a
light theme; metrics history and graphs (monitor's); actions of any kind.

## Done when

The overview and the site route render on a live host from an unchanged configuration file;
every site in the menu opens in the frame on a wide screen and in a new tab on a phone; CI is
green; `docs/index.md` and this plan match what was built.
