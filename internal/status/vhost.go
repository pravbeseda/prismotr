package status

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
)

// Vhost reads a site's certificate, the tail of each log it lists, and the PHP versions it
// declares.
type Vhost struct {
	Site      config.Site
	LiveDir   string // /etc/letsencrypt/live
	TailLines int    // the default is 20
	Now       func() time.Time
}

// Name says where the card was read from.
func (v Vhost) Name() string {
	return filepath.Join(v.LiveDir, "<cert_domain>", "fullchain.pem") + ", the logs listed"
}

func (v Vhost) Read(_ context.Context) (Card, error) {
	card := Card{Title: v.Site.Domain}
	if v.Site.CertDomain != "" {
		card.Rows = append(card.Rows, Row{Label: "Certificate", Value: v.certificate()})
	}
	if len(v.Site.PHPFPMVersions) != 0 {
		card.Rows = append(card.Rows, Row{Label: "PHP", Value: strings.Join(v.Site.PHPFPMVersions, ", ")})
	}
	lines := v.TailLines
	if lines <= 0 {
		lines = 20
	}
	for _, log := range v.Site.Logs {
		tail, err := tailLines(log, lines)
		if err != nil {
			card.Rows = append(card.Rows, Row{Label: log, Value: err.Error()})
			continue
		}
		card.Rows = append(card.Rows, Row{Label: log, Lines: tail})
	}
	return card, nil
}

// certificate is the leaf's not-after and the days left, or why it could not be read.
func (v Vhost) certificate() string {
	body, err := os.ReadFile(filepath.Join(v.LiveDir, v.Site.CertDomain, "fullchain.pem"))
	if err != nil {
		return err.Error()
	}
	block, _ := pem.Decode(body)
	if block == nil {
		return "fullchain.pem holds no PEM block"
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Sprintf("fullchain.pem: %v", err)
	}
	now := v.Now
	if now == nil {
		now = time.Now
	}
	days := int(leaf.NotAfter.Sub(now()).Hours() / 24)
	return fmt.Sprintf("expires %s, in %d days", leaf.NotAfter.UTC().Format("2006-01-02"), days)
}

// tailLines is the last n lines of a file, read from its end so a large log costs little.
func tailLines(path string, n int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	const chunk = 64 << 10
	var collected []byte
	offset := info.Size()
	for offset > 0 && bytes.Count(collected, []byte{'\n'}) <= n {
		size := int64(chunk)
		if offset < size {
			size = offset
		}
		offset -= size
		piece := make([]byte, size)
		if _, err := file.ReadAt(piece, offset); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		collected = append(piece, collected...)
	}
	lines := strings.Split(strings.TrimRight(string(collected), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
