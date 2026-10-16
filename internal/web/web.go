// Package web renders the panel's pages from the configuration in force.
package web

import (
	"bytes"
	"cmp"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/pravbeseda/prismotr/internal/config"
)

//go:embed templates/*.html
var templates embed.FS

var sitesPage = template.Must(template.ParseFS(templates, "templates/sites.html"))

// Handler serves the sites page at base_path. The configuration is read on every request,
// so a reload is seen without a restart.
type Handler struct {
	current func() *config.Config
	version string
}

// New builds the handler; current returns the configuration in force.
func New(current func() *config.Config, version string) *Handler {
	return &Handler{current: current, version: version}
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
	var page bytes.Buffer
	if err := sitesPage.Execute(&page, pageData(cfg, h.version)); err != nil {
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
	Version string
}

// pageData lays the menu out as the static page did: locations by (order, name), then
// vhosts by (order, name). The file carries one list; the grouping is this one rule.
func pageData(cfg *config.Config, version string) data {
	d := data{Title: cmp.Or(cfg.Domain, cfg.Host), Version: version}
	for _, kind := range []config.Kind{config.Location, config.Vhost} {
		var group []config.Site
		for _, s := range cfg.Sites {
			if s.Kind == kind {
				group = append(group, s)
			}
		}
		slices.SortStableFunc(group, func(a, b config.Site) int {
			return cmp.Or(cmp.Compare(a.Order, b.Order), strings.Compare(a.Name, b.Name))
		})
		for _, s := range group {
			d.Menu = append(d.Menu, menuEntry(s))
		}
	}
	return d
}

func menuEntry(s config.Site) entry {
	if s.Kind == config.Vhost {
		return entry{Href: "//" + s.Domain + "/", Text: s.Domain}
	}
	return entry{Href: s.Path, Text: cmp.Or(s.Title, s.Name)}
}
