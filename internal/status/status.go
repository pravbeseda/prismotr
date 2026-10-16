// Package status reads the host's local state into cards: each card names its source and
// when it was read, and a source that fails renders its error inside its card, never the
// page. Everything here is read-only and unprivileged.
package status

import (
	"context"
	"sync"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
)

// Row is one line of a card: a label with a value, or a label with lines under it.
type Row struct {
	Label string
	Value string
	Lines []string
}

// Card is what the page renders for one source.
type Card struct {
	Title  string
	Source string
	ReadAt time.Time
	Rows   []Row
	Err    string
}

// Source reads one card. Read returns the card it managed to fill and the error that
// stopped it, if one did; both reach the page.
type Source interface {
	Name() string
	Read(ctx context.Context) (Card, error)
}

// Options bound a collection.
type Options struct {
	Now     func() time.Time
	Timeout time.Duration // per source; the default is five seconds
}

const defaultTimeout = 5 * time.Second

// Collect reads every source at once, each under its own timeout, and returns the cards in
// the order the sources were given.
func Collect(ctx context.Context, sources []Source, opts Options) []Card {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cards := make([]Card, len(sources))
	var wg sync.WaitGroup
	for i, source := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bounded, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			card, err := source.Read(bounded)
			card.Source = source.Name()
			card.ReadAt = now()
			if err != nil {
				card.Err = err.Error()
			}
			cards[i] = card
		}()
	}
	wg.Wait()
	return cards
}

// Defaults are the production seams of the sources: where /proc, the certificates and
// systemctl are, and which ports a location is probed on.
type Defaults struct {
	ProcRoot  string
	StatusURL string
	LiveDir   string
	TailLines int
	TLSPort   string
	PlainPort string
}

// Production is where everything lives on a Debian host.
var Production = Defaults{
	ProcRoot:  "/proc",
	StatusURL: "http://127.0.0.1/nginx_status",
	LiveDir:   "/etc/letsencrypt/live",
	TailLines: 20,
	TLSPort:   "443",
	PlainPort: "80",
}

// Sources is the host card followed by one card per site, in the order the sites are given.
func Sources(cfg *config.Config, sites []config.Site, d Defaults) []Source {
	sources := []Source{Host{ProcRoot: d.ProcRoot, StatusURL: d.StatusURL, Sites: sites}}
	for _, s := range sites {
		if s.Kind == config.Vhost {
			sources = append(sources, Vhost{Site: s, LiveDir: d.LiveDir, TailLines: d.TailLines})
		} else {
			sources = append(sources, Location{Site: s, Domain: cfg.Domain, TLSPort: d.TLSPort, PlainPort: d.PlainPort})
		}
	}
	return sources
}
