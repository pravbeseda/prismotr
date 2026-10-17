// Package web renders the panel's pages from the configuration in force.
package web

import (
	"bytes"
	"cmp"
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/pravbeseda/prismotr/internal/config"
	"github.com/pravbeseda/prismotr/internal/status"
)

//go:embed templates/*.html
var templates embed.FS

//go:embed static
var static embed.FS

var panelPage = template.Must(template.New("panel.html").
	Funcs(template.FuncMap{"percent": func(used float64) int { return int(used * 100) }}).
	ParseFS(templates, "templates/panel.html"))

var staticFiles = func() http.Handler {
	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServerFS(files)
}()

// Collector reads the status page for a configuration, the sites in menu order.
type Collector func(ctx context.Context, cfg *config.Config, sites []config.Site) status.Page

// Handler serves the overview at base_path, a location framed beside the rail at
// base_path/site/<name>, and the stylesheet and fonts under base_path/static/. The
// configuration is read on every request, so a reload is seen without a restart.
type Handler struct {
	current func() *config.Config
	version string
	collect Collector
}

// New builds the handler; current returns the configuration in force.
func New(current func() *config.Config, version string, collect Collector) *Handler {
	return &Handler{current: current, version: version, collect: collect}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := h.current()
	ordered := menuOrder(cfg.Sites)
	static := prefix(cfg.BasePath) + "/static/"
	isStatic := strings.HasPrefix(r.URL.Path, static) && !strings.HasSuffix(r.URL.Path, "/")
	name, isSite := strings.CutPrefix(r.URL.Path, prefix(cfg.BasePath)+"/site/")
	open := -1
	if isSite {
		open = framed(ordered, name)
	}
	if !isStatic && open < 0 && !isBasePath(r.URL.Path, cfg.BasePath) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	if isStatic {
		http.StripPrefix(static, staticFiles).ServeHTTP(w, r)
		return
	}
	d := pageData(cfg, ordered, h.version, h.collect(r.Context(), cfg, ordered), open)
	var page bytes.Buffer
	if err := panelPage.Execute(&page, d); err != nil {
		slog.Error("render the panel", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := page.WriteTo(w); err != nil {
		slog.Debug("write the panel", "error", err)
	}
}

// framed is the index of the site a frame route names, or -1 when it names none or a vhost:
// a vhost is another origin and refuses the frame. Names may repeat; the first in menu order
// is the one framed.
func framed(ordered []config.Site, name string) int {
	i := slices.IndexFunc(ordered, func(s config.Site) bool { return s.Name == name })
	if i < 0 || ordered[i].Kind == config.Vhost {
		return -1
	}
	return i
}

// prefix is base_path as the start of a longer path: empty for /.
func prefix(base string) string {
	return strings.TrimSuffix(base, "/")
}

// isBasePath accepts base_path with and without a trailing slash, and nothing else.
func isBasePath(path, base string) bool {
	if base == "/" {
		return path == "/"
	}
	return path == base || path == base+"/"
}

type data struct {
	Title     string
	Host      string
	Domain    string
	Home      string
	Static    string
	Sites     []site
	Open      *site // the location framed beside the rail; nil on the overview
	MainSite  bool
	HubURL    string
	Version   string
	Page      status.Page
	Tiles     []status.Row
	Attention []status.Notice
	Worst     status.State
}

// site is one site as the sidebar and its card show it.
type site struct {
	Title    string
	Kind     string
	Path     string // a location's; empty for a vhost
	Sub      string // the small line under the title in the sidebar
	Monogram string
	Href     string // the frame route for a location, the vhost itself for a vhost
	NewTab   bool
	Current  bool
	Card     status.Card
}

// menuOrder lays the sites out as the static page did: locations by (order, name), then
// vhosts by (order, name). The file carries one list; the grouping is this one rule.
func menuOrder(sites []config.Site) []config.Site {
	var ordered []config.Site
	for _, kind := range []config.Kind{config.Location, config.Vhost} {
		var group []config.Site
		for _, s := range sites {
			if s.Kind == kind {
				group = append(group, s)
			}
		}
		slices.SortStableFunc(group, func(a, b config.Site) int {
			return cmp.Or(cmp.Compare(a.Order, b.Order), strings.Compare(a.Name, b.Name))
		})
		ordered = append(ordered, group...)
	}
	return ordered
}

// pageData is the overview when open is negative, and the site at that index in a frame
// otherwise.
func pageData(cfg *config.Config, ordered []config.Site, version string, page status.Page, open int) data {
	d := data{
		Title:     cmp.Or(cfg.Domain, cfg.Host),
		Host:      cfg.Host,
		Domain:    cfg.Domain,
		Home:      cfg.BasePath,
		Static:    prefix(cfg.BasePath) + "/static",
		MainSite:  cfg.BasePath != "/",
		HubURL:    cfg.HubURL,
		Version:   version,
		Page:      page,
		Tiles:     page.Tiles(),
		Attention: status.NeedsAttention(page.Cards()),
	}
	for _, n := range d.Attention {
		d.Worst = max(d.Worst, n.Row.State)
	}
	for i, s := range ordered {
		v := siteView(s, prefix(cfg.BasePath))
		v.Current = i == open
		if i < len(page.Sites) {
			v.Card = page.Sites[i]
		}
		d.Sites = append(d.Sites, v)
	}
	if open >= 0 {
		d.Open = &d.Sites[open]
	}
	return d
}

func siteView(s config.Site, prefix string) site {
	if s.Kind == config.Vhost {
		return site{Title: s.Domain, Kind: "vhost", Sub: "vhost · opens in a new tab", Monogram: monogram(s.Domain), Href: "//" + s.Domain + "/", NewTab: true}
	}
	title := cmp.Or(s.Title, s.Name)
	return site{Title: title, Kind: "location", Path: s.Path, Sub: s.Path, Monogram: monogram(title), Href: prefix + "/site/" + url.PathEscape(s.Name)}
}

// monogram is the first two letters of a site's name, as the sidebar's square shows it.
func monogram(title string) string {
	runes := []rune(title)
	return string(runes[:min(2, len(runes))])
}
