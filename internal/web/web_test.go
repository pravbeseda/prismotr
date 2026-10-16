package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
	"github.com/pravbeseda/prismotr/internal/status"
	"github.com/pravbeseda/prismotr/internal/web"
)

func serve(cfg *config.Config) http.Handler {
	return web.New(func() *config.Config { return cfg }, "1.2.3", fakeCollect())
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The menu as admin-panel.html.j2 renders it today: locations by (order, name), then vhosts
// by (order, name), then Main Site — whatever order the file lists them in, and even when
// a vhost's order is lower than a location's.
func TestMenuParity(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/", Sites: []config.Site{
		{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com", Order: 10},
		{Name: "munin", Kind: config.Location, Title: "Munin", Path: "/munin", Order: 20},
		{Name: "blog", Kind: config.Vhost, Domain: "blog.example.com", Order: 50},
		{Name: "adminer", Kind: config.Location, Path: "/adminer", Order: 20},
		{Name: "files", Kind: config.Location, Title: "Files", Path: "/files", Order: 5},
	}}
	body := get(t, serve(cfg), "/").Body.String()
	links := regexp.MustCompile(`<a href="([^"]*)">([^<]*)</a>`).FindAllStringSubmatch(body, -1)
	var got []string
	for _, l := range links {
		got = append(got, l[1]+" "+l[2])
	}
	want := []string{
		"/files Files",
		"/adminer adminer", // no title: the name stands in
		"/munin Munin",
		"//shop.example.com/ shop.example.com",
		"//blog.example.com/ blog.example.com",
		"/ Main Site",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("menu:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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

func TestAnEmptyMenuStillRendersMainSite(t *testing.T) {
	body := get(t, serve(&config.Config{Host: "host-a", BasePath: "/"}), "/").Body.String()
	if !strings.Contains(body, `<a href="/">Main Site</a>`) {
		t.Errorf("empty menu:\n%s", body)
	}
}

// The page answers at base_path, with and without the trailing slash, and nowhere else.
func TestRoutesFollowBasePath(t *testing.T) {
	cases := []struct {
		base, path string
		status     int
	}{
		{"/", "/", http.StatusOK},
		{"/", "/prismotr", http.StatusNotFound},
		{"/", "/other", http.StatusNotFound},
		{"/prismotr", "/prismotr", http.StatusOK},
		{"/prismotr", "/prismotr/", http.StatusOK},
		{"/prismotr", "/", http.StatusNotFound},
		{"/prismotr", "/prismotr/x", http.StatusNotFound},
		{"/prismotr", "/prismotrx", http.StatusNotFound},
	}
	for _, c := range cases {
		cfg := &config.Config{Host: "host-a", BasePath: c.base}
		if got := get(t, serve(cfg), c.path).Code; got != c.status {
			t.Errorf("base %s, GET %s = %d, want %d", c.base, c.path, got, c.status)
		}
	}
}

func TestOnlyGetIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	serve(&config.Config{Host: "host-a", BasePath: "/"}).ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
}

// The page reads the configuration on every request, so a reload is seen without a restart.
func TestThePageFollowsTheCurrentConfiguration(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/"}
	h := web.New(func() *config.Config { return cfg }, "1.2.3", fakeCollect())
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

func TestTheFooterNamesTheVersion(t *testing.T) {
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

// ---- the cards (plan steps 7–10) ----

func fakeCollect(cards ...status.Card) func(context.Context, *config.Config, []config.Site) []status.Card {
	return func(context.Context, *config.Config, []config.Site) []status.Card { return cards }
}

var readAt = time.Date(2026, 10, 16, 12, 34, 56, 0, time.UTC)

// Every card renders its title, its rows, its lines, its source and when it was read; a card
// that failed shows its error in place and the others still render.
func TestCardsRenderRowsLinesSourceAndErrors(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/"}
	h := web.New(func() *config.Config { return cfg }, "1.2.3", fakeCollect(
		status.Card{Title: "Host", Source: "/proc", ReadAt: readAt, Rows: []status.Row{
			{Label: "Uptime", Value: "1d 2h 3m"},
			{Label: "/var/log/nginx/x.log", Lines: []string{"first line", "second <line>"}},
		}},
		status.Card{Title: "Broken", Source: "a well", ReadAt: readAt, Err: "the well is dry"},
	))
	body := get(t, h, "/").Body.String()
	for _, want := range []string{
		"Host", "Uptime", "1d 2h 3m", "/var/log/nginx/x.log", "first line", "second &lt;line&gt;",
		"/proc", "12:34:56", "Broken", "the well is dry", "a well",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q:\n%s", want, body)
		}
	}
}

// The collector is handed the sites in menu order, so the cards follow the menu.
func TestCardsAreCollectedInMenuOrder(t *testing.T) {
	cfg := &config.Config{Host: "host-a", BasePath: "/", Sites: []config.Site{
		{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com", Order: 10},
		{Name: "munin", Kind: config.Location, Path: "/munin", Order: 20},
	}}
	var handed []string
	collect := func(_ context.Context, _ *config.Config, sites []config.Site) []status.Card {
		for _, s := range sites {
			handed = append(handed, s.Name)
		}
		return nil
	}
	get(t, web.New(func() *config.Config { return cfg }, "1.2.3", collect), "/")
	if strings.Join(handed, ",") != "munin,shop" {
		t.Errorf("sites handed to the collector = %v, want locations first", handed)
	}
}

// The hub card is one link to hub_url when it is set, nothing when it is not.
func TestTheHubLinkFollowsTheConfiguration(t *testing.T) {
	with := &config.Config{Host: "host-a", BasePath: "/", HubURL: "https://hub.example.com/nodes/host-a"}
	body := get(t, web.New(func() *config.Config { return with }, "1.2.3", fakeCollect()), "/").Body.String()
	if !strings.Contains(body, `<a href="https://hub.example.com/nodes/host-a">`) {
		t.Errorf("no hub link:\n%s", body)
	}
	without := &config.Config{Host: "host-a", BasePath: "/"}
	body = get(t, web.New(func() *config.Config { return without }, "1.2.3", fakeCollect()), "/").Body.String()
	if strings.Contains(body, "hub") {
		t.Errorf("a hub mention without a hub_url:\n%s", body)
	}
}
