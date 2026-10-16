package status_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
	"github.com/pravbeseda/prismotr/internal/status"
)

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func rows(c status.Card) map[string]string {
	m := map[string]string{}
	for _, r := range c.Rows {
		m[r.Label] = r.Value
	}
	return m
}

// ---- the framework (plan step 7) ----

type fakeSource struct {
	name string
	card func(ctx context.Context) (status.Card, error)
}

func (f fakeSource) Name() string { return f.name }
func (f fakeSource) Read(ctx context.Context) (status.Card, error) {
	return f.card(ctx)
}

// A source that fails renders its error inside its card, and the other cards still render.
func TestAFailingSourceRendersItsErrorInItsCard(t *testing.T) {
	ok := fakeSource{"ok", func(context.Context) (status.Card, error) {
		return status.Card{Title: "Fine", Rows: []status.Row{{Label: "a", Value: "1"}}}, nil
	}}
	broken := fakeSource{"broken", func(context.Context) (status.Card, error) {
		return status.Card{Title: "Broken"}, errors.New("the well is dry")
	}}
	cards := status.Collect(context.Background(), []status.Source{ok, broken}, status.Options{Now: func() time.Time { return now }})
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}
	if cards[0].Err != "" || cards[0].Rows[0].Value != "1" || cards[0].Source != "ok" || !cards[0].ReadAt.Equal(now) {
		t.Errorf("the healthy card = %+v", cards[0])
	}
	if cards[1].Err != "the well is dry" || cards[1].Source != "broken" {
		t.Errorf("the broken card = %+v", cards[1])
	}
}

// A source that hangs is cut off by its own timeout and renders that, never the page.
func TestAHangingSourceIsCutOff(t *testing.T) {
	slow := fakeSource{"slow", func(ctx context.Context) (status.Card, error) {
		<-ctx.Done()
		return status.Card{Title: "Slow"}, ctx.Err()
	}}
	start := time.Now()
	cards := status.Collect(context.Background(), []status.Source{slow}, status.Options{Timeout: 50 * time.Millisecond})
	if time.Since(start) > 2*time.Second {
		t.Fatal("the collection waited for the hanging source")
	}
	if cards[0].Err == "" {
		t.Errorf("the slow card reports no error: %+v", cards[0])
	}
}

// ---- the host card (plan step 8) ----

func fakeProc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"uptime":  "93784.52 180000.00\n",
		"loadavg": "0.42 0.35 0.30 1/123 4567\n",
		"mounts": "sysfs /sys sysfs rw 0 0\n" +
			"/dev/sda1 / ext4 rw,relatime 0 0\n" +
			"tmpfs /run tmpfs rw 0 0\n" +
			"/dev/sdb1 /srv xfs rw 0 0\n" +
			"/dev/sda2 /boot/efi vfat rw 0 0\n" +
			"/dev/sda1 /snap/x squashfs ro 0 0\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeSystemctl answers is-active for the units it is told about and "inactive" otherwise.
func fakeSystemctl(t *testing.T, active ...string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n[ \"$1\" = is-active ] || { echo 'unexpected: '\"$*\" >&2; exit 2; }\ncase \"$2\" in\n"
	for _, unit := range active {
		script += unit + ") echo active; exit 0 ;;\n"
	}
	script += "*) echo inactive; exit 3 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func hostSource(t *testing.T, proc, pathDir, statusURL string, sites ...config.Site) status.Host {
	t.Helper()
	return status.Host{
		ProcRoot:  proc,
		PathDir:   pathDir,
		StatusURL: statusURL,
		Sites:     sites,
		Statfs: func(path string) (total, free uint64, err error) {
			switch path {
			case "/":
				return 100 << 30, 40 << 30, nil
			case "/srv":
				return 1 << 40, 1 << 39, nil
			case "/boot/efi":
				return 300 << 20, 240 << 20, nil
			}
			return 0, 0, errors.New("no such mount")
		},
	}
}

func TestTheHostCardReadsUptimeLoadAndDisks(t *testing.T) {
	h := hostSource(t, fakeProc(t), fakeSystemctl(t), "")
	card, err := h.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got := rows(card)
	for label, want := range map[string]string{
		"Uptime":         "1d 2h 3m",
		"Load":           "0.42 0.35 0.30",
		"Disk /":         "60% used, 40 GiB free of 100 GiB",
		"Disk /srv":      "50% used, 512 GiB free of 1.0 TiB",
		"Disk /boot/efi": "20% used, 240 MiB free of 300 MiB", // a UEFI system partition is under a GiB
	} {
		if got[label] != want {
			t.Errorf("%s = %q, want %q", label, got[label], want)
		}
	}
	for _, unwanted := range []string{"Disk /sys", "Disk /run", "Disk /snap/x"} {
		if _, found := got[unwanted]; found {
			t.Errorf("%s is listed; pseudo filesystems are not disks", unwanted)
		}
	}
}

// nginx and every php<ver>-fpm unit any site declares, through systemctl is-active.
func TestTheHostCardAsksSystemdForNginxAndEachPHPFPM(t *testing.T) {
	sites := []config.Site{
		{Name: "a", Kind: config.Vhost, Domain: "a.example.com", PHPFPMVersions: []string{"8.4"}},
		{Name: "b", Kind: config.Vhost, Domain: "b.example.com", PHPFPMVersions: []string{"8.4", "8.2"}},
	}
	h := hostSource(t, fakeProc(t), fakeSystemctl(t, "nginx", "php8.4-fpm"), "", sites...)
	card, err := h.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got := rows(card)
	for label, want := range map[string]string{"nginx": "active", "php8.4-fpm": "active", "php8.2-fpm": "inactive"} {
		if got[label] != want {
			t.Errorf("%s = %q, want %q", label, got[label], want)
		}
	}
	if n := strings.Count(strings.Join(labels(card), ","), "php8.4-fpm"); n != 1 {
		t.Errorf("php8.4-fpm listed %d times, want once", n)
	}
}

func labels(c status.Card) []string {
	var out []string
	for _, r := range c.Rows {
		out = append(out, r.Label)
	}
	return out
}

// /nginx_status over loopback is parsed when it answers 200 and skipped silently otherwise.
func TestTheHostCardReadsNginxStatusWhenItAnswers(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Active connections: 3 \nserver accepts handled requests\n 100 100 250 \nReading: 0 Writing: 1 Waiting: 2 \n"))
	}))
	defer served.Close()
	h := hostSource(t, fakeProc(t), fakeSystemctl(t), served.URL+"/nginx_status")
	card, _ := h.Read(context.Background())
	if got := rows(card)["nginx connections"]; got != "3 active, 250 requests served" {
		t.Errorf("nginx connections = %q", got)
	}

	silent := httptest.NewServer(http.NotFoundHandler())
	defer silent.Close()
	h.StatusURL = silent.URL + "/nginx_status"
	card, err := h.Read(context.Background())
	if err != nil {
		t.Fatalf("a 404 from nginx_status failed the card: %v", err)
	}
	if _, found := rows(card)["nginx connections"]; found {
		t.Error("a 404 from nginx_status still produced a row")
	}
}

// A host without /proc — the developer's Mac — is an error the card shows, not a crash.
func TestTheHostCardWithoutProcReportsIt(t *testing.T) {
	h := hostSource(t, filepath.Join(t.TempDir(), "no-proc"), fakeSystemctl(t), "")
	if _, err := h.Read(context.Background()); err == nil {
		t.Error("Read = nil error without /proc")
	}
}

// ---- the vhost card (plan step 9) ----

func writeCertificate(t *testing.T, dir, domain string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, domain)
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	// fullchain.pem holds the leaf first and the chain after it; a second block must not
	// confuse the reader.
	body := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	body = append(body, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	if err := os.WriteFile(filepath.Join(live, "fullchain.pem"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheVhostCardReadsTheCertificateLogsAndPHP(t *testing.T) {
	live := t.TempDir()
	writeCertificate(t, live, "shop.example.com", now.Add(30*24*time.Hour+time.Hour))
	logDir := t.TempDir()
	access := filepath.Join(logDir, "shop-access.log")
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "line "+string(rune('0'+i%10))+" of the access log")
	}
	if err := os.WriteFile(access, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(logDir, "shop-error.log")
	if err := os.WriteFile(unreadable, []byte("secret\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	v := status.Vhost{
		Site: config.Site{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com",
			CertDomain: "shop.example.com", Logs: []string{access, unreadable}, PHPFPMVersions: []string{"8.4"}},
		LiveDir:   live,
		TailLines: 10,
		Now:       func() time.Time { return now },
	}
	card, err := v.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got := rows(card)
	if got["Certificate"] != "expires 2026-11-15, in 30 days" {
		t.Errorf("Certificate = %q", got["Certificate"])
	}
	if got["PHP"] != "8.4" {
		t.Errorf("PHP = %q", got["PHP"])
	}
	var tail, denied *status.Row
	for i := range card.Rows {
		switch card.Rows[i].Label {
		case access:
			tail = &card.Rows[i]
		case unreadable:
			denied = &card.Rows[i]
		}
	}
	if tail == nil || len(tail.Lines) != 10 || tail.Lines[9] != lines[29] || tail.Lines[0] != lines[20] {
		t.Errorf("the tail of the access log = %+v, want its last 10 lines", tail)
	}
	if os.Getuid() != 0 && (denied == nil || !strings.Contains(denied.Value, "permission denied")) {
		t.Errorf("the unreadable log = %+v, want a row saying it cannot be read", denied)
	}
}

func TestTheVhostCardWithoutACertificateSaysSo(t *testing.T) {
	v := status.Vhost{
		Site:    config.Site{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com", CertDomain: "shop.example.com"},
		LiveDir: t.TempDir(),
		Now:     func() time.Time { return now },
	}
	card, err := v.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := rows(card)["Certificate"]; !strings.Contains(got, "no such file") {
		t.Errorf("Certificate = %q, want the missing file named", got)
	}
}

// A site with no cert_domain asks for no certificate at all.
func TestTheVhostCardSkipsTheCertificateWhenNoneIsDeclared(t *testing.T) {
	v := status.Vhost{Site: config.Site{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com"}, LiveDir: t.TempDir()}
	card, _ := v.Read(context.Background())
	if _, found := rows(card)["Certificate"]; found {
		t.Error("a certificate row without a cert_domain")
	}
}

// ---- the location card (plan step 10) ----

// tlsServer is a loopback server with a self-signed certificate for the host's domain: the
// probe measures reachability, not identity, so it must accept it.
func tlsServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	s.StartTLS()
	return s
}

func plainServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
}

func port(t *testing.T, s *httptest.Server) string {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := port(t, &httptest.Server{URL: "http://" + l.Addr().String()})
	_ = l.Close()
	return p
}

func TestTheLocationCardProbesTLSFirstAndFallsBackToPlain(t *testing.T) {
	site := config.Site{Name: "munin", Kind: config.Location, Title: "Munin", Path: "/munin"}
	tests := []struct {
		name      string
		tlsStatus int // 0: no TLS server
		plain     int // 0: no plain server
		want      string
	}{
		{"behind basic auth on 443", http.StatusUnauthorized, 0, "answers (401)"},
		{"healthy on 443", http.StatusOK, 0, "answers (200)"},
		{"no TLS, plain answers", 0, http.StatusOK, "answers (200)"},
		{"no TLS, plain says 401", 0, http.StatusUnauthorized, "answers (401)"},
		{"nothing listens", 0, 0, "does not answer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			l := status.Location{Site: site, Domain: "host-a.example.com", TLSPort: closedPort(t), PlainPort: closedPort(t)}
			if test.tlsStatus != 0 {
				s := tlsServer(t, test.tlsStatus)
				defer s.Close()
				l.TLSPort = port(t, s)
			}
			if test.plain != 0 {
				s := plainServer(t, test.plain)
				defer s.Close()
				l.PlainPort = port(t, s)
			}
			card, err := l.Read(context.Background())
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			got := rows(card)
			if !strings.HasPrefix(got["Loopback"], test.want) {
				t.Errorf("Loopback = %q, want %q", got["Loopback"], test.want)
			}
			if got["Link"] != "/munin" {
				t.Errorf("Link = %q", got["Link"])
			}
		})
	}
}

// The TLS probe presents the host's domain as the server name, so nginx picks the right vhost.
func TestTheLocationProbeSendsTheDomainAsServerName(t *testing.T) {
	var seen string
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.TLS.ServerName
		w.WriteHeader(http.StatusOK)
	}))
	s.TLS = &tls.Config{}
	s.StartTLS()
	defer s.Close()
	l := status.Location{Site: config.Site{Name: "x", Kind: config.Location, Path: "/x"}, Domain: "host-a.example.com", TLSPort: port(t, s), PlainPort: closedPort(t)}
	if _, err := l.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen != "host-a.example.com" {
		t.Errorf("server name = %q, want host-a.example.com", seen)
	}
}

// The TLS probe does not follow a redirect: the answer of the server that holds the location
// is the answer, wherever it points.
func TestTheLocationProbeDoesNotFollowRedirects(t *testing.T) {
	s := tlsServer(t, http.StatusMovedPermanently)
	defer s.Close()
	l := status.Location{Site: config.Site{Name: "x", Kind: config.Location, Path: "/x"}, TLSPort: port(t, s), PlainPort: closedPort(t)}
	card, _ := l.Read(context.Background())
	if got := rows(card)["Loopback"]; !strings.HasPrefix(got, "answers (301)") {
		t.Errorf("Loopback = %q", got)
	}
}

// The host card comes first, then one card per site in the order given, a vhost's and a
// location's each from the source that reads it.
func TestSourcesFollowTheSitesGiven(t *testing.T) {
	cfg := &config.Config{Host: "host-a", Domain: "host-a.example.com", BasePath: "/"}
	sites := []config.Site{
		{Name: "munin", Kind: config.Location, Path: "/munin"},
		{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com"},
	}
	sources := status.Sources(cfg, sites, status.Production)
	if len(sources) != 3 {
		t.Fatalf("got %d sources, want 3", len(sources))
	}
	if _, ok := sources[0].(status.Host); !ok {
		t.Errorf("the first source is %T, want Host", sources[0])
	}
	if l, ok := sources[1].(status.Location); !ok || l.Site.Name != "munin" || l.Domain != "host-a.example.com" || l.TLSPort != "443" {
		t.Errorf("the second source is %#v, want munin's Location with the host's domain", sources[1])
	}
	if v, ok := sources[2].(status.Vhost); !ok || v.Site.Name != "shop" || v.LiveDir != "/etc/letsencrypt/live" {
		t.Errorf("the third source is %#v, want shop's Vhost", sources[2])
	}
}
