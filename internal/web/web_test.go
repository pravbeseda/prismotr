package web_test

import (
	"context"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
	"github.com/pravbeseda/prismotr/internal/status"
	"github.com/pravbeseda/prismotr/internal/web"
)

var update = flag.Bool("update", false, "rewrite the golden files from the current output")

func serve(cfg *config.Config) http.Handler {
	return web.New(func() *config.Config { return cfg }, "1.2.3", fakeCollect(status.Page{}))
}

func fakeCollect(page status.Page) web.Collector {
	return func(context.Context, *config.Config, []config.Site) status.Page { return page }
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// nav is every entry of the sidebar's navigation as a wide screen shows it, in order.
func nav(t *testing.T, body string) []string {
	t.Helper()
	block := regexp.MustCompile(`(?s)<nav class="nav"[^>]*>(.*?)</nav>`).FindStringSubmatch(body)
	if block == nil {
		t.Fatalf("no navigation in:\n%s", body)
	}
	var hrefs []string
	for _, m := range regexp.MustCompile(`<a class="item(?: wide)?" href="([^"]*)"`).FindAllStringSubmatch(block[1], -1) {
		hrefs = append(hrefs, m[1])
	}
	return hrefs
}

var mixedSites = []config.Site{
	{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com", Order: 10},
	{Name: "munin", Kind: config.Location, Title: "Munin", Path: "/munin", Order: 20},
	{Name: "blog", Kind: config.Vhost, Domain: "blog.example.com", Order: 50},
	{Name: "adminer", Kind: config.Location, Path: "/adminer", Order: 20},
	{Name: "files", Kind: config.Location, Title: "Files", Path: "/files", Order: 5},
}

// The sites follow v1's menu: locations by (order, name), then vhosts by (order, name),
// whatever order the file lists them in, and even when a vhost's order is lower than a
// location's. Main Site follows them only under a sub-path: at / it is the panel itself.
func TestMenuParity(t *testing.T) {
	for base, want := range map[string][]string{
		"/":         {"/", "/site/files", "/site/adminer", "/site/munin", "//shop.example.com/", "//blog.example.com/"},
		"/prismotr": {"/prismotr", "/prismotr/site/files", "/prismotr/site/adminer", "/prismotr/site/munin", "//shop.example.com/", "//blog.example.com/", "/"},
	} {
		cfg := &config.Config{Host: "host-a", BasePath: base, Sites: mixedSites}
		if got := nav(t, get(t, serve(cfg), base).Body.String()); !slices.Equal(got, want) {
			t.Errorf("base %s: navigation\n%s\nwant\n%s", base, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

func TestTitleIsTheDomainOrTheHost(t *testing.T) {
	withDomain := &config.Config{Host: "host-a", Domain: "host-a.example.com", BasePath: "/"}
	if body := get(t, serve(withDomain), "/").Body.String(); !strings.Contains(body, "<title>host-a.example.com - Admin Panel</title>") {
		t.Errorf("title with a domain:\n%s", body)
	}
	withoutDomain := &config.Config{Host: "host-a", BasePath: "/"}
	if body := get(t, serve(withoutDomain), "/").Body.String(); !strings.Contains(body, "<title>host-a - Admin Panel</title>") {
		t.Errorf("title without a domain:\n%s", body)
	}
}

// A vhost, Main Site and the hub open in a new tab; a location opens in the panel's frame, and
// its own address only ever in a new tab.
func TestNewTabsAreForOtherSites(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/prismotr", HubURL: "https://hub.example.com/nodes/host-a", Sites: mixedSites}
	body := get(t, serve(cfg), "/prismotr").Body.String()
	for _, href := range []string{"//shop.example.com/", "/", "https://hub.example.com/nodes/host-a"} {
		if !strings.Contains(body, `href="`+href+`" target="_blank" rel="noopener"`) {
			t.Errorf("%s does not open in a new tab:\n%s", href, body)
		}
	}
	if !strings.Contains(body, `href="/prismotr/site/munin"`) {
		t.Errorf("a location does not open in the frame:\n%s", body)
	}
	if strings.Contains(body, `href="/munin">`) {
		t.Errorf("a location's own address opens in the panel's tab:\n%s", body)
	}
}

// The page answers at base_path, with and without the trailing slash, its static files under
// base_path/static/, and nothing else.
func TestRoutesFollowBasePath(t *testing.T) {
	cases := []struct {
		base, path string
		status     int
	}{
		{"/", "/", http.StatusOK},
		{"/", "/prismotr", http.StatusNotFound},
		{"/", "/other", http.StatusNotFound},
		{"/", "/static/panel.css", http.StatusOK},
		{"/prismotr", "/prismotr", http.StatusOK},
		{"/prismotr", "/prismotr/", http.StatusOK},
		{"/prismotr", "/", http.StatusNotFound},
		{"/prismotr", "/prismotr/x", http.StatusNotFound},
		{"/prismotr", "/prismotrx", http.StatusNotFound},
		{"/prismotr", "/prismotr/static/panel.css", http.StatusOK},
		{"/prismotr", "/prismotr/static/fonts/space-grotesk/OFL.txt", http.StatusOK},
		{"/prismotr", "/static/panel.css", http.StatusNotFound},
		{"/prismotr", "/prismotr/static/", http.StatusNotFound},
		{"/prismotr", "/prismotr/static/fonts/", http.StatusNotFound},
		{"/prismotr", "/prismotr/static/missing.css", http.StatusNotFound},
	}
	for _, c := range cases {
		cfg := &config.Config{Host: "host-a", BasePath: c.base}
		if got := get(t, serve(cfg), c.path).Code; got != c.status {
			t.Errorf("base %s, GET %s = %d, want %d", c.base, c.path, got, c.status)
		}
	}
}

func TestOnlyGetIsServed(t *testing.T) {
	for _, path := range []string{"/", "/static/panel.css"} {
		rec := httptest.NewRecorder()
		serve(&config.Config{Host: "host-a", BasePath: "/"}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, rec.Code)
		}
	}
}

// Every font the stylesheet names is served beside it, so the page needs nothing off the host.
func TestTheStylesheetsFontsAreServed(t *testing.T) {
	h := serve(&config.Config{Host: "host-a", BasePath: "/prismotr"})
	css := get(t, h, "/prismotr/static/panel.css")
	if got := css.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Errorf("panel.css is served as %q", got)
	}
	urls := regexp.MustCompile(`url\(([^)]*)\)`).FindAllStringSubmatch(css.Body.String(), -1)
	if len(urls) == 0 {
		t.Fatal("the stylesheet names no font")
	}
	for _, u := range urls {
		if strings.Contains(u[1], ":") || strings.HasPrefix(u[1], "/") {
			t.Errorf("url(%s) is not relative to the stylesheet", u[1])
			continue
		}
		if got := get(t, h, "/prismotr/static/"+u[1]).Code; got != http.StatusOK {
			t.Errorf("url(%s) = %d", u[1], got)
		}
	}
}

// The page reads the configuration on every request, so a reload is seen without a restart.
func TestThePageFollowsTheCurrentConfiguration(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/"}
	h := web.New(func() *config.Config { return cfg }, "1.2.3", fakeCollect(status.Page{}))
	if body := get(t, h, "/").Body.String(); strings.Contains(body, "munin") {
		t.Fatal("munin before it was added")
	}
	cfg = &config.Config{Host: "host-a", BasePath: "/", Sites: []config.Site{
		{Name: "munin", Kind: config.Location, Path: "/munin"},
	}}
	if body := get(t, h, "/").Body.String(); !strings.Contains(body, "/munin") {
		t.Errorf("munin missing after the change:\n%s", body)
	}
}

func TestTheSidebarNamesTheVersion(t *testing.T) {
	body := get(t, serve(&config.Config{Host: "host-a", BasePath: "/"}), "/").Body.String()
	if !strings.Contains(body, "prismotr 1.2.3") {
		t.Errorf("no version in:\n%s", body)
	}
}

func TestSiteValuesAreEscaped(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/", Sites: []config.Site{
		{Name: "x", Kind: config.Location, Title: "<b>x</b>", Path: "/x"},
	}}
	body := get(t, serve(cfg), "/").Body.String()
	if strings.Contains(body, "<b>x</b>") {
		t.Errorf("title rendered unescaped:\n%s", body)
	}
}

// ---- the overview ----

var readAt = time.Date(2026, 10, 16, 12, 34, 56, 0, time.UTC)

// page is one of everything: tiles, a warning, an error, a failed card and a folded log.
func page() status.Page {
	return status.Page{
		ReadAt: readAt,
		Vitals: status.Card{Title: "Host", Source: "/proc", ReadAt: readAt, Rows: []status.Row{
			{Label: "Uptime", Value: "1d 2h 3m", Detail: "since 2026-10-15 10:31 UTC"},
			{Label: "Load", Value: "0.35", Detail: "1m 0.42 · 15m 0.30 · 4 cores"},
			{Label: "Memory", Value: "62%", Detail: "3 GiB available of 8 GiB · no swap", Used: 0.625},
		}},
		Services: status.Card{Title: "Services", Source: "systemctl is-active", ReadAt: readAt, Rows: []status.Row{
			{Label: "nginx", Value: "active"},
			{Label: "php8.4-fpm", Value: "failed", State: status.Err},
		}},
		Disks: status.Card{Title: "Disks", Source: "/proc/mounts, statfs", ReadAt: readAt, Rows: []status.Row{
			{Label: "/", Value: "85%", Detail: "15 GiB free of 100 GiB", Used: 0.85, State: status.Warn},
		}},
		Sites: []status.Card{
			{Title: "Munin", Source: "a request over loopback", ReadAt: readAt, Err: "the well is dry"},
			{Title: "shop.example.com", Source: "the logs listed", ReadAt: readAt,
				Rows: []status.Row{{Label: "Certificate", Value: "expires 2026-11-15, in 30 days"}},
				Logs: []status.Row{{Label: "/var/log/nginx/shop.log", Lines: []string{"first line", "second <line>"}}}},
		},
	}
}

var pageSites = []config.Site{
	{Name: "munin", Kind: config.Location, Title: "Munin", Path: "/munin"},
	{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com"},
}

func overview(t *testing.T, base string) string {
	t.Helper()
	cfg := &config.Config{Host: "host-a", Domain: "host-a.example.com", BasePath: base, HubURL: "https://hub.example.com/nodes/host-a", Sites: pageSites}
	rec := get(t, web.New(func() *config.Config { return cfg }, "1.2.3", fakeCollect(page())), base)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", base, rec.Code)
	}
	return rec.Body.String()
}

// The overview at both base paths, byte for byte; go test ./internal/web -update rewrites the
// files after a deliberate change, and the diff is reviewed like code.
func TestGoldenOverview(t *testing.T) {
	for base, file := range map[string]string{"/": "overview-root.html", "/prismotr": "overview-prismotr.html"} {
		got := overview(t, base)
		path := filepath.Join("testdata", file)
		if *update {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v; run go test ./internal/web -update", err)
		}
		if got != string(want) {
			t.Errorf("base %s differs from %s; run go test ./internal/web -update and review the diff:\n%s", base, path, got)
		}
	}
}

// Every link and source on the page stays on the host, except the sites and the hub.
func TestNoURLLeavesTheHost(t *testing.T) {
	for _, base := range []string{"/", "/prismotr"} {
		prefix := strings.TrimSuffix(base, "/")
		allowed := []string{base, "/", prefix + "/site/munin", "/munin", "//shop.example.com/", "https://hub.example.com/nodes/host-a"}
		for _, m := range regexp.MustCompile(`(?:href|src)="([^"]*)"`).FindAllStringSubmatch(overview(t, base), -1) {
			if !slices.Contains(allowed, m[1]) && !strings.HasPrefix(m[1], prefix+"/static/") {
				t.Errorf("base %s: %s leaves the host", base, m[1])
			}
		}
	}
}

// Every card renders its title, its rows, its source and when it was read; a card that
// failed shows its error in place and the others still render.
func TestCardsRenderRowsSourceAndErrors(t *testing.T) {
	body := overview(t, "/")
	for _, want := range []string{
		"1d 2h 3m", "since 2026-10-15 10:31 UTC", "php8.4-fpm", "15 GiB free of 100 GiB",
		"systemctl is-active", "12:34:56", "Munin", "the well is dry", "a request over loopback",
		"expires 2026-11-15, in 30 days", "first line", "second &lt;line&gt;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
}

// Needs attention lists every row that is not ok and every failed card, and is absent when
// nothing is wrong.
func TestNeedsAttentionAppearsOnlyWhenSomethingIsWrong(t *testing.T) {
	block := regexp.MustCompile(`(?s)<section class="attention".*?</section>`).FindString(overview(t, "/"))
	for _, want := range []string{"Services · php8.4-fpm", "Disks · /", "Munin · a request over loopback"} {
		if !strings.Contains(block, want) {
			t.Errorf("needs attention lacks %q:\n%s", want, block)
		}
	}
	healthy := get(t, web.New(func() *config.Config { return &config.Config{Host: "host-a", BasePath: "/"} }, "1.2.3",
		fakeCollect(status.Page{Vitals: status.Card{Rows: []status.Row{{Label: "Uptime", Value: "1d"}}}})), "/").Body.String()
	if strings.Contains(healthy, "Needs attention") {
		t.Errorf("a healthy page needs attention:\n%s", healthy)
	}
}

// A site's logs are folded away until asked for.
func TestLogsAreFolded(t *testing.T) {
	body := overview(t, "/")
	if !strings.Contains(body, "<details>") || strings.Contains(body, "<details open") {
		t.Errorf("the logs are not in a closed <details>:\n%s", body)
	}
}

// The collector is handed the sites in menu order, so the site cards follow the menu.
func TestCardsAreCollectedInMenuOrder(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/", Sites: []config.Site{
		{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com", Order: 10},
		{Name: "munin", Kind: config.Location, Path: "/munin", Order: 20},
	}}
	var handed []string
	collect := func(_ context.Context, _ *config.Config, sites []config.Site) status.Page {
		for _, s := range sites {
			handed = append(handed, s.Name)
		}
		return status.Page{}
	}
	get(t, web.New(func() *config.Config { return cfg }, "1.2.3", collect), "/")
	if strings.Join(handed, ",") != "munin,shop" {
		t.Errorf("sites handed to the collector = %v, want locations first", handed)
	}
}

// The hub is one link to hub_url when it is set, nothing when it is not.
func TestTheHubLinkFollowsTheConfiguration(t *testing.T) {
	with := &config.Config{Host: "host-a", BasePath: "/", HubURL: "https://hub.example.com/nodes/host-a"}
	if body := get(t, serve(with), "/").Body.String(); !strings.Contains(body, `href="https://hub.example.com/nodes/host-a"`) {
		t.Errorf("no hub link:\n%s", body)
	}
	without := &config.Config{Host: "host-a", BasePath: "/"}
	if body := get(t, serve(without), "/").Body.String(); strings.Contains(strings.ToLower(body), "hub") {
		t.Errorf("a hub mention without a hub_url:\n%s", body)
	}
}

// ---- the site route (v2 plan step 4) ----

func framed(t *testing.T, base, path string) *httptest.ResponseRecorder {
	t.Helper()
	cfg := &config.Config{Host: "host-a", Domain: "host-a.example.com", BasePath: base, Sites: pageSites}
	return get(t, web.New(func() *config.Config { return cfg }, "1.2.3", fakeCollect(page())), path)
}

// A location opens in a frame beside the rail: reload is the same route, ↗ is the location in
// a new tab, and the rail marks the site as the current page.
func TestTheSiteRouteFramesALocation(t *testing.T) {
	for base, route := range map[string]string{"/": "/site/munin", "/prismotr": "/prismotr/site/munin"} {
		rec := framed(t, base, route)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", route, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			`<iframe src="/munin" title="Munin"`,
			`<a class="icon-btn" href="` + route + `"`,
			`<a class="icon-btn" href="/munin" target="_blank" rel="noopener"`,
			`<a class="item wide" href="` + route + `" aria-current="page"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("base %s: the page lacks %s:\n%s", base, want, body)
			}
		}
		if strings.Contains(body, `class="overview"`) {
			t.Errorf("base %s: the overview renders beside the frame", base)
		}
	}
}

// A vhost is another origin and is never framed; a name nobody declared is not a page.
func TestTheSiteRouteRefusesVhostsAndUnknownNames(t *testing.T) {
	for base, paths := range map[string][]string{
		"/":         {"/site/shop", "/site/nobody", "/site/", "/site/munin/x", "/site"},
		"/prismotr": {"/prismotr/site/shop", "/prismotr/site/nobody", "/site/munin"},
	} {
		for _, path := range paths {
			if got := framed(t, base, path).Code; got != http.StatusNotFound {
				t.Errorf("base %s, GET %s = %d, want 404", base, path, got)
			}
		}
	}
}

// Names are not unique in the file; the first in menu order is the one framed.
func TestTheFirstOfTwoNamesIsFramed(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/", Sites: []config.Site{
		{Name: "x", Kind: config.Location, Path: "/second", Order: 2},
		{Name: "x", Kind: config.Location, Path: "/first", Order: 1},
	}}
	if body := get(t, serve(cfg), "/site/x").Body.String(); !strings.Contains(body, `<iframe src="/first"`) {
		t.Errorf("the frame is not the first x in menu order:\n%s", body)
	}
}

func TestTheSiteRouteIsGetOnly(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/", Sites: pageSites}
	rec := httptest.NewRecorder()
	serve(cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/site/munin", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /site/munin = %d, want 405", rec.Code)
	}
}

func TestGoldenSite(t *testing.T) {
	got := framed(t, "/prismotr", "/prismotr/site/munin").Body.String()
	path := filepath.Join("testdata", "site-prismotr.html")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/web -update", err)
	}
	if got != string(want) {
		t.Errorf("the site page differs from %s; run go test ./internal/web -update and review the diff:\n%s", path, got)
	}
}

// ---- the phone layout (v2 plan step 5) ----

// Each location's entry carries both of its links, the frame route for a wide screen and the
// location in a new tab for a phone, and CSS shows one; a vhost's is one link at every width.
func TestEachLocationHasAFrameLinkAndANewTabLink(t *testing.T) {
	body := overview(t, "/prismotr")
	for _, want := range []string{
		`<a class="item wide" href="/prismotr/site/munin"`,
		`<a class="item narrow" href="/munin" target="_blank" rel="noopener"`,
		`<a class="btn primary wide" href="/prismotr/site/munin"`,
		`<a class="btn primary narrow" href="/munin" target="_blank" rel="noopener"`,
		`<a class="item" href="//shop.example.com/" target="_blank" rel="noopener"`,
		`<a class="btn primary" href="//shop.example.com/" target="_blank" rel="noopener"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
}

// A phone gets a top bar with the host, the attention count and the drawer's toggle.
func TestThePhoneTopBarCarriesTheHostAndTheCount(t *testing.T) {
	for _, body := range []string{overview(t, "/"), framed(t, "/", "/site/munin").Body.String()} {
		bar := regexp.MustCompile(`(?s)<header class="topbar">.*?</header>`).FindString(body)
		for _, want := range []string{`for="drawer"`, "host-a", `<span class="count err">3</span>`} {
			if !strings.Contains(bar, want) {
				t.Errorf("the top bar lacks %s:\n%s", want, bar)
			}
		}
		if !strings.Contains(body, `<input type="checkbox" id="drawer"`) {
			t.Error("no drawer toggle")
		}
	}
}
