package config_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pravbeseda/prismotr/internal/config"
)

const full = `
host: host-a
domain: host-a.example.com
base_path: /prismotr
hub_url: https://hub.example.com/nodes/host-a
sites:
  - name: munin
    kind: location
    title: Munin
    path: /munin
    order: 20
  - name: shop
    kind: vhost
    domain: shop.example.com
    aliases: [www.shop.example.com]
    cert_domain: shop.example.com
    logs: [/var/log/nginx/shop-access.log, /var/log/nginx/shop-error.log]
    php_fpm_versions: ["8.4"]
    order: 50
`

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prismotr.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spec: config.md#loading — the file above, every field as written.
func TestLoadReadsEveryField(t *testing.T) {
	cfg, warnings, err := config.Load(write(t, full))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if cfg.Host != "host-a" || cfg.Domain != "host-a.example.com" ||
		cfg.BasePath != "/prismotr" || cfg.HubURL != "https://hub.example.com/nodes/host-a" {
		t.Errorf("host fields = %+v", cfg)
	}
	if len(cfg.Sites) != 2 {
		t.Fatalf("sites = %d, want 2", len(cfg.Sites))
	}
	munin, shop := cfg.Sites[0], cfg.Sites[1]
	if munin.Name != "munin" || munin.Kind != config.Location || munin.Title != "Munin" ||
		munin.Path != "/munin" || munin.Order != 20 {
		t.Errorf("munin = %+v", munin)
	}
	if shop.Name != "shop" || shop.Kind != config.Vhost || shop.Domain != "shop.example.com" ||
		strings.Join(shop.Aliases, ",") != "www.shop.example.com" ||
		shop.CertDomain != "shop.example.com" ||
		strings.Join(shop.Logs, ",") != "/var/log/nginx/shop-access.log,/var/log/nginx/shop-error.log" ||
		strings.Join(shop.PHPFPMVersions, ",") != "8.4" || shop.Order != 50 {
		t.Errorf("shop = %+v", shop)
	}
}

// spec: config.md#loading — the rows that refuse to start, each naming what is wrong.
func TestLoadRefuses(t *testing.T) {
	cases := []struct {
		name, file, mentions string
	}{
		{"not yaml", "host: [", "prismotr.yaml"},
		{"no host", "base_path: /\n", "host"},
		{"no base_path", "host: host-a\n", "base_path"},
		{"base_path without leading slash", "host: host-a\nbase_path: prismotr\n", "base_path"},
		{"base_path with trailing slash", "host: host-a\nbase_path: /prismotr/\n", "base_path"},
		{"unknown kind", "host: host-a\nbase_path: /\nsites: [{name: x, kind: proxy}]\n", "proxy"},
		{"site without name", "host: host-a\nbase_path: /\nsites: [{kind: location, path: /x}]\n", "1"},
		{"location without path", "host: host-a\nbase_path: /\nsites: [{name: x, kind: location}]\n", "x"},
		{"vhost without domain", "host: host-a\nbase_path: /\nsites: [{name: x, kind: vhost}]\n", "x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := config.Load(write(t, c.file))
			if err == nil {
				t.Fatal("Load = nil error, want a refusal")
			}
			if !strings.Contains(err.Error(), c.mentions) {
				t.Errorf("error %q does not mention %q", err, c.mentions)
			}
		})
	}
}

// spec: config.md#loading — no file at the path.
func TestLoadRefusesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prismotr.yaml")
	_, _, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("Load = %v, want an error naming %s", err, path)
	}
}

// spec: config.md#loading — without sites, or with an empty list, the daemon runs.
func TestLoadWithoutSitesRuns(t *testing.T) {
	for _, file := range []string{"host: host-a\nbase_path: /\n", "host: host-a\nbase_path: /\nsites: []\n"} {
		cfg, _, err := config.Load(write(t, file))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(cfg.Sites) != 0 {
			t.Errorf("sites = %v, want none", cfg.Sites)
		}
	}
}

// spec: config.md#loading — base_path "/" is accepted as is.
func TestLoadAcceptsRootBasePath(t *testing.T) {
	cfg, _, err := config.Load(write(t, "host: host-a\nbase_path: /\n"))
	if err != nil || cfg.BasePath != "/" {
		t.Errorf("Load = %+v, %v", cfg, err)
	}
}

// spec: config.md#loading — an unknown key is reported, not refused.
func TestLoadReportsUnknownKeys(t *testing.T) {
	cfg, warnings, err := config.Load(write(t,
		"host: host-a\nbase_path: /\ncolour: blue\nsites: [{name: x, kind: location, path: /x, icon: star}]\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Sites) != 1 {
		t.Errorf("sites = %v, want the one site", cfg.Sites)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "colour") || !strings.Contains(joined, "icon") || !strings.Contains(joined, "x") {
		t.Errorf("warnings = %q, want colour, and icon named with site x", warnings)
	}
}

// spec: config.md#invariants — order defaults to 0 and the lists to empty.
func TestLoadDefaults(t *testing.T) {
	cfg, _, err := config.Load(write(t, "host: host-a\nbase_path: /\nsites: [{name: x, kind: vhost, domain: x.example.com}]\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	s := cfg.Sites[0]
	if s.Order != 0 || len(s.Aliases) != 0 || len(s.Logs) != 0 || len(s.PHPFPMVersions) != 0 {
		t.Errorf("site = %+v, want zero order and empty lists", s)
	}
}

// spec: config.md#reloading — a file that now lists one more site.
func TestReloadTakesANewFile(t *testing.T) {
	path := write(t, "host: host-a\nbase_path: /\n")
	h, err := config.Open(path, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.WriteFile(path, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := len(h.Current().Sites); got != 2 {
		t.Errorf("sites after reload = %d, want 2", got)
	}
}

// spec: config.md#reloading — a file that now fails, and a file that was removed.
func TestReloadKeepsTheConfigurationInForce(t *testing.T) {
	for name, breakIt := range map[string]func(path string) error{
		"broken":  func(path string) error { return os.WriteFile(path, []byte("host: ["), 0o644) },
		"removed": os.Remove,
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, full)
			h, err := config.Open(path, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := breakIt(path); err != nil {
				t.Fatal(err)
			}
			if err := h.Reload(); err == nil {
				t.Fatal("Reload = nil, want the error")
			}
			if got := len(h.Current().Sites); got != 2 {
				t.Errorf("sites after a failed reload = %d, want the 2 in force", got)
			}
		})
	}
}

// spec: config.md#loading — the first load refuses as Load does; nothing is in force.
func TestOpenRefusesWhatLoadRefuses(t *testing.T) {
	if _, err := config.Open(write(t, "host: ["), slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("Open = nil error, want a refusal")
	}
}

// The example the repository ships is a valid file this version fully understands.
func TestTheShippedExampleLoadsClean(t *testing.T) {
	cfg, warnings, err := config.Load("../../prismotr.yaml.example")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 || len(cfg.Sites) != 2 {
		t.Errorf("example: warnings %v, %d sites", warnings, len(cfg.Sites))
	}
}
