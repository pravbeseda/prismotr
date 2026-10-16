package status

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
)

// Location shows a location's link and whether its path answers over loopback. The probe
// goes to port 443 with the host's domain as the server name and no certificate check — it
// measures reachability, not identity — and repeats over plain HTTP only when 443 refuses the
// connection, as it does on a host without TLS. The literal 127.0.0.1, never localhost: on a
// TLS host the port-80 server redirects every path and answers ::1 with a bare OK.
type Location struct {
	Site      config.Site
	Domain    string // the host's own domain, sent as the TLS server name
	TLSPort   string // 443
	PlainPort string // 80
}

// Name says where the card was read from.
func (Location) Name() string { return "a request over loopback" }

const probeTimeout = 3 * time.Second

func (l Location) Read(ctx context.Context) (Card, error) {
	card := Card{Title: cmp.Or(l.Site.Title, l.Site.Name)}
	card.Rows = append(card.Rows, Row{Label: "Link", Value: l.Site.Path})
	card.Rows = append(card.Rows, Row{Label: "Loopback", Value: l.probe(ctx)})
	return card, nil
}

func (l Location) probe(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	tlsClient := &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{ServerName: l.Domain, InsecureSkipVerify: true}}, //nolint:gosec // reachability, not identity: loopback, and nginx holds the real check
		CheckRedirect: noRedirect,
	}
	answer, err := l.request(ctx, tlsClient, "https", cmp.Or(l.TLSPort, "443"))
	if err == nil {
		return answer
	}
	if !refused(err) {
		return "does not answer: " + err.Error()
	}
	plainClient := &http.Client{Transport: &http.Transport{}, CheckRedirect: noRedirect}
	answer, err = l.request(ctx, plainClient, "http", cmp.Or(l.PlainPort, "80"))
	if err != nil {
		return "does not answer: " + err.Error()
	}
	return answer
}

func (l Location) request(ctx context.Context, client *http.Client, scheme, port string) (string, error) {
	url := fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort("127.0.0.1", port), l.Site.Path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	request.Host = l.Domain
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	_ = response.Body.Close()
	return fmt.Sprintf("answers (%d) on port %s", response.StatusCode, port), nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// refused is a connection nobody accepted: the port is closed, so the other one is tried.
func refused(err error) bool {
	var operation *net.OpError
	return errors.As(err, &operation) && operation.Op == "dial"
}
