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
	"fmt"
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

// ---- the host: vitals, services, disks (plan step 8; v2 plan steps 1–2) ----

const gib = 1 << 30

func fakeProc(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"uptime":  "93784.52 180000.00\n",
		"loadavg": "0.42 0.35 0.30 1/123 4567\n",
		"cpuinfo": "processor\t: 0\nmodel name\t: a CPU\n\nprocessor\t: 1\n\nprocessor\t: 2\n\nprocessor\t: 3\n",
		"meminfo": "MemTotal:        8388608 kB\nMemFree:          524288 kB\nMemAvailable:    3145728 kB\n" +
			"SwapTotal:       2097152 kB\nSwapFree:        1572864 kB\n",
		"mounts": "sysfs /sys sysfs rw 0 0\n" +
			"/dev/sda1 / ext4 rw,relatime 0 0\n" +
			"tmpfs /run tmpfs rw 0 0\n" +
			"/dev/sdb1 /srv xfs rw 0 0\n" +
			"/dev/sda2 /boot/efi vfat rw 0 0\n" +
			"/dev/sda1 /snap/x squashfs ro 0 0\n",
	}
	for name, body := range files {
		writeProc(t, dir, name, body)
	}
	return dir
}

func writeProc(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func vitals(proc string) status.Vitals {
	return status.Vitals{ProcRoot: proc, Now: func() time.Time { return now }}
}

func TestTheVitalsReadUptimeLoadAndMemory(t *testing.T) {
	card, err := vitals(fakeProc(t)).Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, want := range []status.Row{
		{Label: "Uptime", Value: "1d 2h 3m", Detail: "since 2026-10-15 09:56 UTC"},
		{Label: "Load", Value: "0.35", Detail: "1m 0.42 · 15m 0.30 · 4 cores"},
		{Label: "Memory", Value: "62%", Detail: "3 GiB available of 8 GiB · swap 25% of 2 GiB", Used: 0.625},
	} {
		got := row(t, card, want.Label)
		if got.Value != want.Value || got.Detail != want.Detail || got.Used != want.Used || got.State != status.OK {
			t.Errorf("%s = %+v, want %+v", want.Label, got, want)
		}
	}
}

func TestMemoryWithoutSwapSaysSo(t *testing.T) {
	proc := fakeProc(t)
	writeProc(t, proc, "meminfo", "MemTotal: 8388608 kB\nMemAvailable: 3145728 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	card, _ := vitals(proc).Read(context.Background())
	if got := row(t, card, "Memory").Detail; got != "3 GiB available of 8 GiB · no swap" {
		t.Errorf("Memory detail = %q", got)
	}
}

// lxcfs can report more available than total; the tile reads none used, not an overflow.
func TestMoreAvailableThanTotalIsNoneUsed(t *testing.T) {
	proc := fakeProc(t)
	writeProc(t, proc, "meminfo", "MemTotal: 8388608 kB\nMemAvailable: 9437184 kB\nSwapTotal: 0 kB\n")
	card, _ := vitals(proc).Read(context.Background())
	got := row(t, card, "Memory")
	if got.Value != "0%" || got.Used != 0 || got.State != status.OK {
		t.Errorf("Memory = %q, used %v, state %v; want 0%%, 0, ok", got.Value, got.Used, got.State)
	}
}

// The same lxcfs over-report on swap reads none used, not an overflow.
func TestMoreSwapFreeThanTotalIsNoneUsed(t *testing.T) {
	proc := fakeProc(t)
	writeProc(t, proc, "meminfo", "MemTotal: 8388608 kB\nMemAvailable: 3145728 kB\nSwapTotal: 1048576 kB\nSwapFree: 2097152 kB\n")
	card, _ := vitals(proc).Read(context.Background())
	if got := row(t, card, "Memory").Detail; got != "3 GiB available of 8 GiB · swap 0% of 1 GiB" {
		t.Errorf("Memory detail = %q", got)
	}
}

// Memory warns from 80% used and errs from 90%, as a disk does.
func TestAMemoryStateFollowsItsUsage(t *testing.T) {
	tests := []struct {
		availableMiB int
		want         status.State
	}{
		{2049, status.OK},
		{2048, status.Warn},
		{1025, status.Warn},
		{1024, status.Err},
	}
	for _, test := range tests {
		proc := fakeProc(t)
		writeProc(t, proc, "meminfo", fmt.Sprintf("MemTotal: %d kB\nMemAvailable: %d kB\n", 10240<<10, test.availableMiB<<10))
		card, _ := vitals(proc).Read(context.Background())
		if got := row(t, card, "Memory").State; got != test.want {
			t.Errorf("%d MiB available of 10240: state %v, want %v", test.availableMiB, got, test.want)
		}
	}
}

// Load warns when the 5-minute average is above the core count, not at it.
func TestTheLoadWarnsAboveTheCoreCount(t *testing.T) {
	for loadavg, want := range map[string]status.State{
		"9.00 4.00 0.10 1/1 1": status.OK,
		"0.10 4.01 9.00 1/1 1": status.Warn,
	} {
		proc := fakeProc(t)
		writeProc(t, proc, "loadavg", loadavg)
		card, _ := vitals(proc).Read(context.Background())
		if got := row(t, card, "Load").State; got != want {
			t.Errorf("loadavg %q on 4 cores: state %v, want %v", loadavg, got, want)
		}
	}
}

// A file that cannot be read errs in its own row and leaves the others.
func TestAMissingProcFileErrsInItsRowOnly(t *testing.T) {
	for _, file := range []string{"meminfo", "cpuinfo", "uptime", "loadavg"} {
		proc := fakeProc(t)
		if err := os.Remove(filepath.Join(proc, file)); err != nil {
			t.Fatal(err)
		}
		card, err := vitals(proc).Read(context.Background())
		if err != nil {
			t.Fatalf("without %s: Read = %v, want the error in a row", file, err)
		}
		broken := map[string]string{"meminfo": "Memory", "cpuinfo": "Load", "uptime": "Uptime", "loadavg": "Load"}[file]
		for _, r := range card.Rows {
			want := status.OK
			if r.Label == broken {
				want = status.Err
			}
			if r.State != want {
				t.Errorf("without %s: %s state %v, want %v (%q)", file, r.Label, r.State, want, r.Value)
			}
		}
	}
}

// A host without /proc — the developer's Mac — errs in every row instead of crashing.
func TestTheVitalsWithoutProcErrInEveryRow(t *testing.T) {
	card, err := vitals(filepath.Join(t.TempDir(), "no-proc")).Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(card.Rows) != 3 || card.State() != status.Err {
		t.Errorf("without /proc: %+v", card)
	}
	for _, r := range card.Rows {
		if r.State != status.Err {
			t.Errorf("%s = %+v, want err", r.Label, r)
		}
	}
}

func disks(proc string, statfs func(path string) (total, free uint64, err error)) status.Disks {
	return status.Disks{ProcRoot: proc, Statfs: statfs}
}

func fakeStatfs(path string) (total, free uint64, err error) {
	switch path {
	case "/":
		return 100 * gib, 40 * gib, nil
	case "/srv":
		return 1 << 40, 1 << 39, nil
	case "/boot/efi":
		return 300 << 20, 240 << 20, nil
	}
	return 0, 0, errors.New("no such mount")
}

func TestTheDisksAreTheRealFilesystems(t *testing.T) {
	card, err := disks(fakeProc(t), fakeStatfs).Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []status.Row{
		{Label: "/", Value: "60%", Detail: "40 GiB free of 100 GiB", Used: 0.6},
		{Label: "/srv", Value: "50%", Detail: "512 GiB free of 1.0 TiB", Used: 0.5},
		{Label: "/boot/efi", Value: "20%", Detail: "240 MiB free of 300 MiB", Used: 0.2}, // a UEFI system partition is under a GiB
	}
	if len(card.Rows) != len(want) {
		t.Fatalf("rows = %+v, want %+v; pseudo filesystems are not disks", card.Rows, want)
	}
	for i := range want {
		got := card.Rows[i]
		if got.Label != want[i].Label || got.Value != want[i].Value || got.Detail != want[i].Detail || got.Used != want[i].Used {
			t.Errorf("row %d = %+v, want %+v", i, got, want[i])
		}
	}
}

// A disk warns from 80% used and errs from 90%.
func TestADiskStateFollowsItsUsage(t *testing.T) {
	tests := []struct {
		freeGiB uint64
		want    status.State
	}{
		{21, status.OK},
		{20, status.Warn},
		{11, status.Warn},
		{10, status.Err},
	}
	for _, test := range tests {
		d := disks(fakeProc(t), func(string) (total, free uint64, err error) { return 100 * gib, test.freeGiB * gib, nil })
		card, _ := d.Read(context.Background())
		if got := row(t, card, "/").State; got != test.want {
			t.Errorf("%d GiB free of 100: state %v, want %v", test.freeGiB, got, test.want)
		}
	}
}

func TestAnUnreadableDiskIsAnError(t *testing.T) {
	d := disks(fakeProc(t), func(string) (total, free uint64, err error) { return 0, 0, errors.New("stale handle") })
	card, _ := d.Read(context.Background())
	if got := row(t, card, "/"); got.State != status.Err || !strings.Contains(got.Value, "stale handle") {
		t.Errorf("/ = %+v, want err", got)
	}

	proc := fakeProc(t)
	if err := os.Remove(filepath.Join(proc, "mounts")); err != nil {
		t.Fatal(err)
	}
	if _, err := disks(proc, fakeStatfs).Read(context.Background()); err == nil {
		t.Error("without /proc/mounts: Read = nil error")
	}
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

// nginx and every php<ver>-fpm unit any site declares, through systemctl is-active; a unit
// that is not active is an error.
func TestTheServicesAskSystemdForNginxAndEachPHPFPM(t *testing.T) {
	sites := []config.Site{
		{Name: "a", Kind: config.Vhost, Domain: "a.example.com", PHPFPMVersions: []string{"8.4"}},
		{Name: "b", Kind: config.Vhost, Domain: "b.example.com", PHPFPMVersions: []string{"8.4", "8.2"}},
	}
	s := status.Services{PathDir: fakeSystemctl(t, "nginx", "php8.4-fpm"), Sites: sites}
	card, err := s.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := strings.Join(labels(card), ","); got != "nginx,php8.2-fpm,php8.4-fpm" {
		t.Errorf("units = %s, want nginx and each PHP version once", got)
	}
	for label, want := range map[string]status.Row{
		"nginx":      {Value: "active", State: status.OK},
		"php8.4-fpm": {Value: "active", State: status.OK},
		"php8.2-fpm": {Value: "inactive", State: status.Err},
	} {
		if got := row(t, card, label); got.Value != want.Value || got.State != want.State {
			t.Errorf("%s = %+v, want %+v", label, got, want)
		}
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
func TestTheServicesReadNginxStatusWhenItAnswers(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("Active connections: 3 \nserver accepts handled requests\n 100 100 250 \nReading: 0 Writing: 1 Waiting: 2 \n"))
	}))
	defer served.Close()
	s := status.Services{PathDir: fakeSystemctl(t, "nginx"), StatusURL: served.URL + "/nginx_status"}
	card, _ := s.Read(context.Background())
	if got := rows(card)["nginx connections"]; got != "3 active, 250 requests served" {
		t.Errorf("nginx connections = %q", got)
	}

	silent := httptest.NewServer(http.NotFoundHandler())
	defer silent.Close()
	s.StatusURL = silent.URL + "/nginx_status"
	card, err := s.Read(context.Background())
	if err != nil {
		t.Fatalf("a 404 from nginx_status failed the card: %v", err)
	}
	if _, found := rows(card)["nginx connections"]; found {
		t.Error("a 404 from nginx_status still produced a row")
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
	for i := range card.Logs {
		switch card.Logs[i].Label {
		case access:
			tail = &card.Logs[i]
		case unreadable:
			denied = &card.Logs[i]
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

// The host's vitals, services and disks come first, then one card per site in the order given, a vhost's and a
// location's each from the source that reads it.
func TestSourcesFollowTheSitesGiven(t *testing.T) {
	cfg := &config.Config{Host: "host-a", Domain: "host-a.example.com", BasePath: "/"}
	sites := []config.Site{
		{Name: "munin", Kind: config.Location, Path: "/munin"},
		{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com"},
	}
	sources := status.Sources(cfg, sites, status.Production)
	if len(sources) != 5 {
		t.Fatalf("got %d sources, want 5", len(sources))
	}
	if v, ok := sources[0].(status.Vitals); !ok || v.ProcRoot != "/proc" {
		t.Errorf("the first source is %#v, want Vitals", sources[0])
	}
	if s, ok := sources[1].(status.Services); !ok || len(s.Sites) != 2 {
		t.Errorf("the second source is %#v, want Services over the sites", sources[1])
	}
	if _, ok := sources[2].(status.Disks); !ok {
		t.Errorf("the third source is %T, want Disks", sources[2])
	}
	if l, ok := sources[3].(status.Location); !ok || l.Site.Name != "munin" || l.Domain != "host-a.example.com" || l.TLSPort != "443" {
		t.Errorf("the fourth source is %#v, want munin's Location with the host's domain", sources[3])
	}
	if v, ok := sources[4].(status.Vhost); !ok || v.Site.Name != "shop" || v.LiveDir != "/etc/letsencrypt/live" {
		t.Errorf("the fifth source is %#v, want shop's Vhost", sources[4])
	}
}

// ---- row state and meters (v2 plan step 1) ----

func row(t *testing.T, c status.Card, label string) status.Row {
	t.Helper()
	for _, r := range c.Rows {
		if r.Label == label {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", label, c.Rows)
	return status.Row{}
}

// A certificate warns under 21 days left and errs under 7; one that cannot be read errs.
func TestACertificateStateFollowsTheDaysLeft(t *testing.T) {
	tests := []struct {
		days int
		want status.State
	}{
		{21, status.OK},
		{20, status.Warn},
		{7, status.Warn},
		{6, status.Err},
		{-1, status.Err},
	}
	for _, test := range tests {
		live := t.TempDir()
		writeCertificate(t, live, "shop.example.com", now.Add(time.Duration(test.days)*24*time.Hour+time.Hour))
		v := status.Vhost{
			Site:    config.Site{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com", CertDomain: "shop.example.com"},
			LiveDir: live,
			Now:     func() time.Time { return now },
		}
		card, _ := v.Read(context.Background())
		if got := row(t, card, "Certificate").State; got != test.want {
			t.Errorf("%d days left: state %v, want %v", test.days, got, test.want)
		}
	}

	v := status.Vhost{
		Site:    config.Site{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com", CertDomain: "shop.example.com"},
		LiveDir: t.TempDir(),
	}
	card, _ := v.Read(context.Background())
	if got := row(t, card, "Certificate").State; got != status.Err {
		t.Errorf("a missing certificate: state %v, want err", got)
	}
}

func TestAnUnreadableLogWarns(t *testing.T) {
	logDir := t.TempDir()
	readable := filepath.Join(logDir, "access.log")
	if err := os.WriteFile(readable, []byte("a line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(logDir, "error.log")
	v := status.Vhost{Site: config.Site{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com", Logs: []string{readable, missing}}}
	card, _ := v.Read(context.Background())
	logs := status.Card{Rows: card.Logs}
	if got := row(t, logs, readable).State; got != status.OK {
		t.Errorf("a readable log: state %v, want ok", got)
	}
	if got := row(t, logs, missing).State; got != status.Warn {
		t.Errorf("a missing log: state %v, want warn", got)
	}
}

// Any answer is a reachable location — 401 is basic auth doing its job — except a 5xx, which
// is nginx saying the backend behind it is down; no answer at all is an error too.
func TestALocationStateFollowsItsProbe(t *testing.T) {
	site := config.Site{Name: "munin", Kind: config.Location, Path: "/munin"}
	tests := []struct {
		name   string
		status int // 0: nothing listens
		want   status.State
	}{
		{"200", http.StatusOK, status.OK},
		{"401", http.StatusUnauthorized, status.OK},
		{"499", 499, status.OK},
		{"500", http.StatusInternalServerError, status.Err},
		{"502", http.StatusBadGateway, status.Err},
		{"nothing listens", 0, status.Err},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			l := status.Location{Site: site, TLSPort: closedPort(t), PlainPort: closedPort(t)}
			if test.status != 0 {
				s := tlsServer(t, test.status)
				defer s.Close()
				l.TLSPort = port(t, s)
			}
			card, _ := l.Read(context.Background())
			if got := row(t, card, "Loopback").State; got != test.want {
				t.Errorf("state %v, want %v", got, test.want)
			}
		})
	}
}

// A card is as bad as its worst row, and a source that failed is an error whatever it read.
func TestACardIsAsBadAsItsWorstRow(t *testing.T) {
	tests := []struct {
		name string
		card status.Card
		want status.State
	}{
		{"no rows", status.Card{}, status.OK},
		{"all ok", status.Card{Rows: []status.Row{{State: status.OK}, {State: status.OK}}}, status.OK},
		{"one warn", status.Card{Rows: []status.Row{{State: status.OK}, {State: status.Warn}}}, status.Warn},
		{"warn and err", status.Card{Rows: []status.Row{{State: status.Err}, {State: status.Warn}}}, status.Err},
		{"a failed source", status.Card{Err: "the well is dry", Rows: []status.Row{{State: status.OK}}}, status.Err},
	}
	for _, test := range tests {
		if got := test.card.State(); got != test.want {
			t.Errorf("%s: state %v, want %v", test.name, got, test.want)
		}
	}
}

// Needs attention lists every row that is not ok and every failed source, in page order.
func TestNeedsAttentionListsWhatIsNotOK(t *testing.T) {
	cards := []status.Card{
		{Title: "Host", Rows: []status.Row{
			{Label: "Uptime", Value: "1d"},
			{Label: "Disk /", Value: "85% used", State: status.Warn},
			{Label: "nginx", Value: "failed", State: status.Err},
		}},
		{Title: "Munin", Rows: []status.Row{{Label: "Loopback", Value: "answers (200)"}}},
		{Title: "Broken", Source: "a well", Err: "the well is dry"},
	}
	got := status.NeedsAttention(cards)
	want := []status.Notice{
		{Card: "Host", Row: status.Row{Label: "Disk /", Value: "85% used", State: status.Warn}},
		{Card: "Host", Row: status.Row{Label: "nginx", Value: "failed", State: status.Err}},
		{Card: "Broken", Row: status.Row{Label: "a well", Value: "the well is dry", State: status.Err}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Card != want[i].Card || got[i].Row.Label != want[i].Row.Label || got[i].Row.Value != want[i].Row.Value || got[i].Row.State != want[i].Row.State {
			t.Errorf("notice %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if none := status.NeedsAttention(cards[1:2]); len(none) != 0 {
		t.Errorf("a healthy page needs attention: %+v", none)
	}
}

// The template prints a state as its CSS class.
func TestAStatePrintsAsItsName(t *testing.T) {
	for state, want := range map[status.State]string{status.OK: "ok", status.Warn: "warn", status.Err: "err"} {
		if got := state.String(); got != want {
			t.Errorf("%d prints %q, want %q", state, got, want)
		}
	}
}

// ---- the page (v2 plan step 3) ----

// A page holds the host's three cards apart from the sites', so the overview can lay them out.
func TestReadPageSplitsTheHostFromTheSites(t *testing.T) {
	cfg := &config.Config{Host: "host-a", Domain: "host-a.example.com", BasePath: "/"}
	sites := []config.Site{
		{Name: "munin", Kind: config.Location, Path: "/munin"},
		{Name: "shop", Kind: config.Vhost, Domain: "shop.example.com"},
	}
	d := status.Defaults{ProcRoot: fakeProc(t), LiveDir: t.TempDir(), TLSPort: closedPort(t), PlainPort: closedPort(t)}
	page := status.ReadPage(context.Background(), cfg, sites, d, status.Options{Now: func() time.Time { return now }})
	if page.Vitals.Title != "Host" || page.Services.Title != "Services" || page.Disks.Title != "Disks" {
		t.Errorf("host cards = %q, %q, %q", page.Vitals.Title, page.Services.Title, page.Disks.Title)
	}
	if len(page.Sites) != 2 || page.Sites[0].Title != "munin" || page.Sites[1].Title != "shop.example.com" {
		t.Errorf("site cards = %+v", page.Sites)
	}
	if !page.ReadAt.Equal(now) {
		t.Errorf("ReadAt = %v", page.ReadAt)
	}
	if got := len(page.Cards()); got != 5 {
		t.Errorf("Cards() holds %d, want 5", got)
	}
}

// The tiles are the vitals, then the fullest disk that could be read.
func TestTheTilesAreTheVitalsAndTheFullestDisk(t *testing.T) {
	page := status.Page{
		Vitals: status.Card{Rows: []status.Row{{Label: "Uptime"}, {Label: "Load"}, {Label: "Memory"}}},
		Disks: status.Card{Rows: []status.Row{
			{Label: "/", Value: "60%", Used: 0.6},
			{Label: "/lost", Value: "unreadable", State: status.Err},
			{Label: "/srv", Value: "85%", Detail: "77 GiB free of 512 GiB", Used: 0.85, State: status.Warn},
		}},
	}
	tiles := page.Tiles()
	if got := strings.Join(func() []string {
		var out []string
		for _, r := range tiles {
			out = append(out, r.Label)
		}
		return out
	}(), ","); got != "Uptime,Load,Memory,Fullest disk · /srv" {
		t.Fatalf("tiles = %s", got)
	}
	if disk := tiles[3]; disk.Value != "85%" || disk.Detail != "77 GiB free of 512 GiB" || disk.Used != 0.85 || disk.State != status.Warn {
		t.Errorf("the disk tile = %+v", disk)
	}
}

func TestTheDiskTileWithoutADiskSaysWhy(t *testing.T) {
	failed := status.Page{Disks: status.Card{Err: "open /proc/mounts: no such file"}}
	if tile := failed.Tiles()[0]; tile.Label != "Fullest disk" || tile.State != status.Err || !strings.Contains(tile.Value, "no such file") {
		t.Errorf("with Disks failed: %+v", tile)
	}
	none := status.Page{Disks: status.Card{Rows: []status.Row{{Label: "/", Value: "unreadable", State: status.Err}}}}
	if tile := none.Tiles()[0]; tile.Label != "Fullest disk" || tile.State != status.Err || tile.Value != "none readable" {
		t.Errorf("with no readable disk: %+v", tile)
	}
}

// A card's logs count towards its state and the attention list like its rows.
func TestLogsCountLikeRows(t *testing.T) {
	card := status.Card{Title: "shop.example.com", Logs: []status.Row{{Label: "/var/log/x.log", Value: "permission denied", State: status.Warn}}}
	if card.State() != status.Warn {
		t.Errorf("state %v, want warn", card.State())
	}
	if got := status.NeedsAttention([]status.Card{card}); len(got) != 1 || got[0].Row.Label != "/var/log/x.log" {
		t.Errorf("attention = %+v", got)
	}
}
