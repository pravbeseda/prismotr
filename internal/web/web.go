// Package web renders the panel's pages from the configuration in force.
package web

import (
	"bytes"
	"cmp"
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/pravbeseda/prismotr/internal/config"
	"github.com/pravbeseda/prismotr/internal/status"
)

//go:embed templates/*.html
var templates embed.FS

var sitesPage = template.Must(template.ParseFS(templates, "templates/sites.html"))

// Collector reads the status cards for a configuration, the sites in menu order.
type Collector func(ctx context.Context, cfg *config.Config, sites []config.Site) []status.Card

// Handler serves the sites page at base_path. The configuration is read on every request,
// so a reload is seen without a restart.
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
	if !isBasePath(r.URL.Path, cfg.BasePath) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	ordered := menuOrder(cfg.Sites)
	d := pageData(cfg, ordered, h.version)
	d.Cards = h.collect(r.Context(), cfg, ordered)
	var page bytes.Buffer
	if err := sitesPage.Execute(&page, d); err != nil {
		slog.Error("render the sites page", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := page.WriteTo(w); err != nil {
		slog.Debug("write the sites page", "error", err)
	}
}

// isBasePath accepts base_path with and without a trailing slash, and nothing else.
func isBasePath(path, base string) bool {
	if base == "/" {
		return path == "/"
	}
	return path == base || path == base+"/"
}

type entry struct {
	Href, Text string
}

type data struct {
	Title   string
	Menu    []entry
	Cards   []status.Card
	HubURL  string
	Version string
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

func pageData(cfg *config.Config, ordered []config.Site, version string) data {
	d := data{Title: cmp.Or(cfg.Domain, cfg.Host), HubURL: cfg.HubURL, Version: version}
	for _, s := range ordered {
		d.Menu = append(d.Menu, menuEntry(s))
	}
	return d
}

func menuEntry(s config.Site) entry {
	if s.Kind == config.Vhost {
		return entry{Href: "//" + s.Domain + "/", Text: s.Domain}
	}
	return entry{Href: s.Path, Text: cmp.Or(s.Title, s.Name)}
}
