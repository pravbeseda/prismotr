package status

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pravbeseda/prismotr/internal/config"
)

// Host reads uptime, load and disks from /proc, the state of nginx and every php-fpm unit
// the sites declare from systemd, and nginx's stub status over loopback.
type Host struct {
	ProcRoot  string // /proc; a test points it at a directory of its own
	PathDir   string // where systemctl is; empty for the one on PATH
	StatusURL string // http://127.0.0.1/nginx_status; empty skips it
	Sites     []config.Site
	Statfs    func(path string) (total, free uint64, err error) // nil for the real call
}

// Name says where the card was read from.
func (Host) Name() string { return "/proc, systemctl is-active, /nginx_status" }

// diskFilesystems are the types that are a disk rather than a view of the kernel.
var diskFilesystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"vfat": true, "f2fs": true,
}

func (h Host) Read(ctx context.Context) (Card, error) {
	card := Card{Title: "Host"}
	uptime, err := h.uptime()
	if err != nil {
		return card, err
	}
	load, err := h.loadavg()
	if err != nil {
		return card, err
	}
	card.Rows = append(card.Rows, Row{Label: "Uptime", Value: uptime}, Row{Label: "Load", Value: load})
	card.Rows = append(card.Rows, h.disks()...)
	for _, unit := range h.units() {
		card.Rows = append(card.Rows, Row{Label: unit, Value: h.isActive(ctx, unit)})
	}
	if connections, ok := h.nginxStatus(ctx); ok {
		card.Rows = append(card.Rows, Row{Label: "nginx connections", Value: connections})
	}
	return card, nil
}

func (h Host) proc(name string) (string, error) {
	body, err := os.ReadFile(filepath.Join(h.ProcRoot, name))
	return string(body), err
}

func (h Host) uptime() (string, error) {
	body, err := h.proc("uptime")
	if err != nil {
		return "", err
	}
	seconds, err := strconv.ParseFloat(strings.Fields(body)[0], 64)
	if err != nil {
		return "", fmt.Errorf("uptime: %w", err)
	}
	d := time.Duration(seconds) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dd %dh %dm", days, hours, minutes), nil
}

func (h Host) loadavg() (string, error) {
	body, err := h.proc("loadavg")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(body)
	if len(fields) < 3 {
		return "", fmt.Errorf("loadavg: %q", body)
	}
	return strings.Join(fields[:3], " "), nil
}

// disks is one row per real filesystem in /proc/mounts, in mount order. A mount that cannot
// be read is one row saying so.
func (h Host) disks() []Row {
	body, err := h.proc("mounts")
	if err != nil {
		return []Row{{Label: "Disks", Value: err.Error()}}
	}
	statfs := h.Statfs
	if statfs == nil {
		statfs = realStatfs
	}
	var rows []Row
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !diskFilesystems[fields[2]] || seen[fields[1]] {
			continue
		}
		seen[fields[1]] = true
		label := "Disk " + fields[1]
		total, free, err := statfs(fields[1])
		if err != nil || total == 0 {
			rows = append(rows, Row{Label: label, Value: fmt.Sprintf("unreadable: %v", err)})
			continue
		}
		used := (total - free) * 100 / total
		rows = append(rows, Row{Label: label, Value: fmt.Sprintf("%d%% used, %s free of %s", used, size(free), size(total))})
	}
	return rows
}

func realStatfs(path string) (total, free uint64, err error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, 0, err
	}
	// Bsize and Blocks are signed on some platforms and unsigned on others.
	return uint64(s.Blocks) * uint64(s.Bsize), uint64(s.Bavail) * uint64(s.Bsize), nil //nolint:unconvert // platform widths differ
}

// size renders a size the way an operator reads a disk: whole GiB below a TiB, one decimal
// above.
func size(n uint64) string {
	const gib = 1 << 30
	const tib = 1 << 40
	if n >= tib {
		return fmt.Sprintf("%.1f TiB", float64(n)/tib)
	}
	return fmt.Sprintf("%d GiB", n/gib)
}

// units is nginx and every php<ver>-fpm the sites declare, each once.
func (h Host) units() []string {
	var versions []string
	for _, s := range h.Sites {
		for _, v := range s.PHPFPMVersions {
			if !slices.Contains(versions, v) {
				versions = append(versions, v)
			}
		}
	}
	slices.Sort(versions)
	units := []string{"nginx"}
	for _, v := range versions {
		units = append(units, "php"+v+"-fpm")
	}
	return units
}

// isActive is what systemctl is-active prints: active, inactive, failed — or why it could
// not be asked. The exit status is not consulted: an inactive unit is an answer, not a fault.
func (h Host) isActive(ctx context.Context, unit string) string {
	systemctl := "systemctl"
	if h.PathDir != "" {
		systemctl = filepath.Join(h.PathDir, "systemctl")
	}
	out, err := exec.CommandContext(ctx, systemctl, "is-active", unit).Output()
	if answer := strings.TrimSpace(string(out)); answer != "" {
		return answer
	}
	if err != nil {
		return err.Error()
	}
	return "unknown"
}

// nginxStatus reads the stub status page when it answers 200, and reports nothing otherwise:
// not every host serves it.
func (h Host) nginxStatus(ctx context.Context) (string, bool) {
	if h.StatusURL == "" {
		return "", false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, h.StatusURL, nil)
	if err != nil {
		return "", false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(body), "\n")
	if len(lines) < 3 {
		return "", false
	}
	active := strings.TrimSpace(strings.TrimPrefix(lines[0], "Active connections:"))
	counters := strings.Fields(lines[2])
	if active == "" || len(counters) < 3 {
		return "", false
	}
	return fmt.Sprintf("%s active, %s requests served", active, counters[2]), true
}
