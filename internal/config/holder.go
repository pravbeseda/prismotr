package config

import (
	"log/slog"
	"sync/atomic"
)

// Holder keeps the configuration in force and replaces it only with a whole file that
// passed every rule (docs/specs/config.md#reloading).
type Holder struct {
	path    string
	log     *slog.Logger
	current atomic.Pointer[Config]
}

// Open loads the file once; a file that fails is a startup error for the caller to report,
// and nothing is in force.
func Open(path string, log *slog.Logger) (*Holder, error) {
	cfg, warnings, err := Load(path)
	if err != nil {
		return nil, err
	}
	h := &Holder{path: path, log: log}
	h.take(cfg, warnings)
	return h, nil
}

// Current is the configuration in force. It is never nil once Open has returned.
func (h *Holder) Current() *Config { return h.current.Load() }

// Reload reads the file again. On failure the configuration in force stays, the error is
// logged and returned; on success unknown keys are logged and the new file takes over.
func (h *Holder) Reload() error {
	cfg, warnings, err := Load(h.path)
	if err != nil {
		h.log.Error("configuration not reloaded", "error", err)
		return err
	}
	h.take(cfg, warnings)
	return nil
}

func (h *Holder) take(cfg *Config, warnings []string) {
	for _, w := range warnings {
		h.log.Warn("configuration", "path", h.path, "warning", w)
	}
	h.current.Store(cfg)
}
