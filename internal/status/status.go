// Package status reads the host's local state into cards: each card names its source and
// when it was read, and a source that fails renders its error inside its card, never the
// page. Everything here is read-only and unprivileged.
package status

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
)

// State is how a row or a card is doing. The order is the severity: the worse of two states
// is the greater.
type State int

// The states, best first.
const (
	OK State = iota
	Warn
	Err
)

// String is the state's name, which the template uses as a CSS class.
func (s State) String() string { return [...]string{"ok", "warn", "err"}[s] }

// Row is one line of a card: a label with a value, or a label with lines under it.
type Row struct {
	Label  string
	Value  string
	Detail string // a smaller line beside the value
	Lines  []string
	State  State
	Used   float64 // the used fraction a meter shows, 0 to 1; zero shows no meter
}

// Card is what the page renders for one source.
type Card struct {
	Title  string
	Source string
	ReadAt time.Time
	Rows   []Row
	Logs   []Row // the tails of a site's logs, which the page folds away
	Err    string
}

// State is the card's worst row's, or Err when its source failed.
func (c Card) State() State {
	if c.Err != "" {
		return Err
	}
	worst := OK
	for _, r := range slices.Concat(c.Rows, c.Logs) {
		worst = max(worst, r.State)
	}
	return worst
}

// Notice is a row that is not OK, with the title of the card it is in.
type Notice struct {
	Card string
	Row  Row
}

// NeedsAttention is every row that is not OK, in page order; a source that failed is an Err
// row of its own, labelled with the source and carrying the error.
func NeedsAttention(cards []Card) []Notice {
	var notices []Notice
	for _, c := range cards {
		if c.Err != "" {
			notices = append(notices, Notice{Card: c.Title, Row: Row{Label: c.Source, Value: c.Err, State: Err}})
		}
		for _, r := range slices.Concat(c.Rows, c.Logs) {
			if r.State != OK {
				notices = append(notices, Notice{Card: c.Title, Row: r})
			}
		}
	}
	return notices
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

// Sources are the host's vitals, services and disks, then one card per site, in the order
// the sites are given.
func Sources(cfg *config.Config, sites []config.Site, d Defaults) []Source {
	sources := []Source{
		Vitals{ProcRoot: d.ProcRoot},
		Services{StatusURL: d.StatusURL, Sites: sites},
		Disks{ProcRoot: d.ProcRoot},
	}
	for _, s := range sites {
		if s.Kind == config.Vhost {
			sources = append(sources, Vhost{Site: s, LiveDir: d.LiveDir, TailLines: d.TailLines})
		} else {
			sources = append(sources, Location{Site: s, Domain: cfg.Domain, TLSPort: d.TLSPort, PlainPort: d.PlainPort})
		}
	}
	return sources
}

// Page is what the panel renders: the host's vitals, services and disks, and one card per
// site in the order the sites were given.
type Page struct {
	ReadAt                  time.Time
	Vitals, Services, Disks Card
	Sites                   []Card
}

// ReadPage collects every source of a configuration into a page.
func ReadPage(ctx context.Context, cfg *config.Config, sites []config.Site, d Defaults, opts Options) Page {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	cards := Collect(ctx, Sources(cfg, sites, d), opts)
	return Page{ReadAt: opts.Now(), Vitals: cards[0], Services: cards[1], Disks: cards[2], Sites: cards[3:]}
}

// Cards are all of the page's cards, the host's first.
func (p Page) Cards() []Card {
	return append([]Card{p.Vitals, p.Services, p.Disks}, p.Sites...)
}

// Tiles are the overview's vital signs: the vitals' rows, then the fullest disk.
func (p Page) Tiles() []Row {
	return append(slices.Clone(p.Vitals.Rows), p.fullestDisk())
}

func (p Page) fullestDisk() Row {
	if p.Disks.Err != "" {
		return Row{Label: "Fullest disk", Value: p.Disks.Err, State: Err}
	}
	var fullest *Row
	for i, r := range p.Disks.Rows {
		if r.Used > 0 && (fullest == nil || r.Used > fullest.Used) {
			fullest = &p.Disks.Rows[i]
		}
	}
	if fullest == nil {
		return Row{Label: "Fullest disk", Value: "none readable", State: p.Disks.State()}
	}
	tile := *fullest
	tile.Label = "Fullest disk · " + fullest.Label
	return tile
}
