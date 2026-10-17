package status

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"

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

func (l Location) Read(ctx context.Context) (Card, error) {
	card := Card{Title: cmp.Or(l.Site.Title, l.Site.Name)}
	card.Rows = append(card.Rows, Row{Label: "Link", Value: l.Site.Path})
	answer, state := l.probe(ctx)
	card.Rows = append(card.Rows, Row{Label: "Loopback", Value: answer, State: state})
	return card, nil
}

// probe is bounded by the per-source timeout Collect sets, like every other source. Any
// answer is a reachable location — a 401 is basic auth doing its job — except a 5xx, nginx
// saying the backend behind it is down.
func (l Location) probe(ctx context.Context) (string, State) {
	tlsClient := &http.Client{
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{ServerName: l.Domain, InsecureSkipVerify: true}}, //nolint:gosec // reachability, not identity: loopback, and nginx holds the real check
		CheckRedirect: noRedirect,
	}
	port := cmp.Or(l.TLSPort, "443")
	code, err := l.request(ctx, tlsClient, "https", port)
	if err != nil && refused(err) {
		plainClient := &http.Client{Transport: &http.Transport{}, CheckRedirect: noRedirect}
		port = cmp.Or(l.PlainPort, "80")
		code, err = l.request(ctx, plainClient, "http", port)
	}
	if err != nil {
		return "does not answer: " + err.Error(), Err
	}
	answer := fmt.Sprintf("answers (%d) on port %s", code, port)
	if code >= http.StatusInternalServerError {
		return answer, Err
	}
	return answer, OK
}

func (l Location) request(ctx context.Context, client *http.Client, scheme, port string) (int, error) {
	url := fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort("127.0.0.1", port), l.Site.Path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	request.Host = l.Domain
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// refused is a connection nobody accepted: the port is closed, so the other one is tried.
func refused(err error) bool {
	var operation *net.OpError
	return errors.As(err, &operation) && operation.Op == "dial"
}
