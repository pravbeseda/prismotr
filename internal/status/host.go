package status

import (
	"context"
	"errors"
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

// Vitals reads the host's vital signs from /proc: uptime and the boot time, load against the
// core count, memory and swap. A file that cannot be read errs in its own row and leaves the
// others.
type Vitals struct {
	ProcRoot string // /proc; a test points it at a directory of its own
	Now      func() time.Time
}

// Name says where the card was read from.
func (Vitals) Name() string { return "/proc" }

func (v Vitals) Read(context.Context) (Card, error) {
	return Card{Title: "Host", Rows: []Row{v.uptime(), v.load(), v.memory()}}, nil
}

func readProc(root, name string) (string, error) {
	body, err := os.ReadFile(filepath.Join(root, name))
	return string(body), err
}

func failed(label string, err error) Row {
	return Row{Label: label, Value: err.Error(), State: Err}
}

func (v Vitals) uptime() Row {
	body, err := readProc(v.ProcRoot, "uptime")
	if err != nil {
		return failed("Uptime", err)
	}
	seconds, err := strconv.ParseFloat(firstField(body), 64)
	if err != nil {
		return failed("Uptime", fmt.Errorf("uptime: %w", err))
	}
	d := time.Duration(seconds) * time.Second
	now := v.Now
	if now == nil {
		now = time.Now
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	return Row{
		Label:  "Uptime",
		Value:  fmt.Sprintf("%dd %dh %dm", days, hours, minutes),
		Detail: "since " + now().Add(-d).UTC().Format("2006-01-02 15:04") + " UTC",
	}
}

func firstField(s string) string {
	if fields := strings.Fields(s); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// load shows the 5-minute average, and warns when it is above the core count.
func (v Vitals) load() Row {
	body, err := readProc(v.ProcRoot, "loadavg")
	if err != nil {
		return failed("Load", err)
	}
	fields := strings.Fields(body)
	if len(fields) < 3 {
		return failed("Load", fmt.Errorf("loadavg: %q", body))
	}
	five, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return failed("Load", fmt.Errorf("loadavg: %w", err))
	}
	row := Row{Label: "Load", Value: fields[1], Detail: fmt.Sprintf("1m %s · 15m %s", fields[0], fields[2])}
	cores, err := v.cores()
	if err != nil {
		row.Detail += " · cores unknown: " + err.Error()
		row.State = Err
		return row
	}
	row.Detail += fmt.Sprintf(" · %d cores", cores)
	if five > float64(cores) {
		row.State = Warn
	}
	return row
}

// cores counts the processor lines of /proc/cpuinfo.
func (v Vitals) cores() (int, error) {
	body, err := readProc(v.ProcRoot, "cpuinfo")
	if err != nil {
		return 0, err
	}
	n := 0
	for line := range strings.Lines(body) {
		if key, _, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "processor" {
			n++
		}
	}
	if n == 0 {
		return 0, errors.New("cpuinfo lists no processor")
	}
	return n, nil
}

// memory is the share not available to new work, MemTotal less MemAvailable, with swap
// beside it.
func (v Vitals) memory() Row {
	body, err := readProc(v.ProcRoot, "meminfo")
	if err != nil {
		return failed("Memory", err)
	}
	bytes := map[string]uint64{}
	for line := range strings.Lines(body) {
		key, rest, _ := strings.Cut(line, ":")
		if kib, err := strconv.ParseUint(firstField(rest), 10, 64); err == nil {
			bytes[key] = kib << 10
		}
	}
	total, hasTotal := bytes["MemTotal"]
	available, hasAvailable := bytes["MemAvailable"]
	if !hasTotal || !hasAvailable || total == 0 {
		return failed("Memory", errors.New("meminfo lacks MemTotal or MemAvailable"))
	}
	detail := fmt.Sprintf("%s available of %s", size(available), size(total))
	if swap := bytes["SwapTotal"]; swap > 0 {
		detail += fmt.Sprintf(" · swap %d%% of %s", (swap-bytes["SwapFree"])*100/swap, size(swap))
	} else {
		detail += " · no swap"
	}
	used := float64(total-available) / float64(total)
	return Row{
		Label:  "Memory",
		Value:  fmt.Sprintf("%d%%", (total-available)*100/total),
		Detail: detail,
		State:  usage(used),
		Used:   used,
	}
}

// Services reads the state of nginx and every php-fpm unit the sites declare from systemd,
// and nginx's stub status over loopback.
type Services struct {
	PathDir   string // where systemctl is; empty for the one on PATH
	StatusURL string // http://127.0.0.1/nginx_status; empty skips it
	Sites     []config.Site
}

// Name says where the card was read from.
func (Services) Name() string { return "systemctl is-active, /nginx_status" }

func (s Services) Read(ctx context.Context) (Card, error) {
	card := Card{Title: "Services"}
	for _, unit := range s.units() {
		answer := s.isActive(ctx, unit)
		state := OK
		if answer != "active" {
			state = Err
		}
		card.Rows = append(card.Rows, Row{Label: unit, Value: answer, State: state})
	}
	if connections, ok := s.nginxStatus(ctx); ok {
		card.Rows = append(card.Rows, Row{Label: "nginx connections", Value: connections})
	}
	return card, nil
}

// Disks reads every real filesystem in /proc/mounts and how full it is.
type Disks struct {
	ProcRoot string                                            // /proc
	Statfs   func(path string) (total, free uint64, err error) // nil for the real call
}

// Name says where the card was read from.
func (Disks) Name() string { return "/proc/mounts, statfs" }

// diskFilesystems are the types that are a disk rather than a view of the kernel.
var diskFilesystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"vfat": true, "f2fs": true,
}

// usage is the state of a used fraction: a disk or memory warns from 80% and errs from 90%.
func usage(used float64) State {
	switch {
	case used >= 0.9:
		return Err
	case used >= 0.8:
		return Warn
	}
	return OK
}

// Read is one row per real filesystem, in mount order. A mount that cannot be read is a row
// saying so.
func (d Disks) Read(context.Context) (Card, error) {
	card := Card{Title: "Disks"}
	body, err := readProc(d.ProcRoot, "mounts")
	if err != nil {
		return card, err
	}
	statfs := d.Statfs
	if statfs == nil {
		statfs = realStatfs
	}
	seen := map[string]bool{}
	for line := range strings.Lines(body) {
		fields := strings.Fields(line)
		if len(fields) < 3 || !diskFilesystems[fields[2]] || seen[fields[1]] {
			continue
		}
		mount := fields[1]
		seen[mount] = true
		total, free, err := statfs(mount)
		if err != nil || total == 0 {
			card.Rows = append(card.Rows, Row{Label: mount, Value: fmt.Sprintf("unreadable: %v", err), State: Err})
			continue
		}
		used := float64(total-free) / float64(total)
		card.Rows = append(card.Rows, Row{
			Label:  mount,
			Value:  fmt.Sprintf("%d%%", (total-free)*100/total),
			Detail: fmt.Sprintf("%s free of %s", size(free), size(total)),
			State:  usage(used),
			Used:   used,
		})
	}
	return card, nil
}

func realStatfs(path string) (total, free uint64, err error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, 0, err
	}
	// Bsize and Blocks are signed on some platforms and unsigned on others.
	return uint64(s.Blocks) * uint64(s.Bsize), uint64(s.Bavail) * uint64(s.Bsize), nil //nolint:unconvert // platform widths differ
}

// size renders a size the way an operator reads a disk: whole MiB below a GiB — /boot/efi is
// a few hundred of them — whole GiB below a TiB, one decimal above.
func size(n uint64) string {
	const mib = 1 << 20
	const gib = 1 << 30
	const tib = 1 << 40
	switch {
	case n >= tib:
		return fmt.Sprintf("%.1f TiB", float64(n)/tib)
	case n >= gib:
		return fmt.Sprintf("%d GiB", n/gib)
	}
	return fmt.Sprintf("%d MiB", n/mib)
}

// units is nginx and every php<ver>-fpm the sites declare, each once.
func (s Services) units() []string {
	var versions []string
	for _, site := range s.Sites {
		for _, v := range site.PHPFPMVersions {
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
func (s Services) isActive(ctx context.Context, unit string) string {
	systemctl := "systemctl"
	if s.PathDir != "" {
		systemctl = filepath.Join(s.PathDir, "systemctl")
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
func (s Services) nginxStatus(ctx context.Context) (string, bool) {
	if s.StatusURL == "" {
		return "", false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.StatusURL, nil)
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
