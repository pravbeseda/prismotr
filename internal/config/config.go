// Package config reads the file ansible-hosts writes and holds the configuration in force.
// Every rule here is docs/specs/config.md.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kind is what a site is: a path on the host's default vhost, or a vhost of its own.
type Kind string

// The two kinds the contract names; anything else refuses the file.
const (
	Location Kind = "location"
	Vhost    Kind = "vhost"
)

// Site is one entry of the menu, as the file lists it.
type Site struct {
	Name           string   `yaml:"name"`
	Kind           Kind     `yaml:"kind"`
	Title          string   `yaml:"title"`
	Path           string   `yaml:"path"`
	Domain         string   `yaml:"domain"`
	Aliases        []string `yaml:"aliases"`
	CertDomain     string   `yaml:"cert_domain"`
	Logs           []string `yaml:"logs"`
	PHPFPMVersions []string `yaml:"php_fpm_versions"`
	Order          int      `yaml:"order"`
}

// Config is the whole file once it has passed every rule of the spec.
type Config struct {
	Host     string `yaml:"host"`
	Domain   string `yaml:"domain"`
	BasePath string `yaml:"base_path"`
	HubURL   string `yaml:"hub_url"`
	Sites    []Site `yaml:"sites"`
}

// The keys this version knows. Anything else in the file is reported, not refused, so a
// newer ansible-hosts never takes down an older daemon.
var (
	knownTopKeys  = keySet("host", "domain", "base_path", "hub_url", "sites")
	knownSiteKeys = keySet("name", "kind", "title", "path", "domain", "aliases",
		"cert_domain", "logs", "php_fpm_versions", "order")
)

// Load reads and validates the file at path. The warnings name the keys it did not know.
func Load(path string) (*Config, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, unknownKeys(raw), nil
}

func (c *Config) validate() error {
	if c.Host == "" {
		return errors.New("host is required")
	}
	switch {
	case c.BasePath == "":
		return errors.New("base_path is required")
	case !strings.HasPrefix(c.BasePath, "/"):
		return fmt.Errorf("base_path %q must start with /", c.BasePath)
	case c.BasePath != "/" && strings.HasSuffix(c.BasePath, "/"):
		return fmt.Errorf("base_path %q must not end with /", c.BasePath)
	}
	for i, s := range c.Sites {
		if s.Name == "" {
			return fmt.Errorf("site %d has no name", i+1)
		}
		switch s.Kind {
		case Location:
			if s.Path == "" {
				return fmt.Errorf("site %s: a location needs a path", s.Name)
			}
		case Vhost:
			if s.Domain == "" {
				return fmt.Errorf("site %s: a vhost needs a domain", s.Name)
			}
		default:
			return fmt.Errorf("site %s: unknown kind %q", s.Name, s.Kind)
		}
	}
	return nil
}

// unknownKeys walks the document a second time, as a tree, to name what the struct
// decoding silently dropped. A file that reaches here has already parsed and validated.
func unknownKeys(raw []byte) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return nil
	}
	var warnings []string
	top := doc.Content[0]
	for key, value := range mappingEntries(top) {
		if _, known := knownTopKeys[key]; !known {
			warnings = append(warnings, fmt.Sprintf("unknown key %q", key))
		}
		if key != "sites" || value.Kind != yaml.SequenceNode {
			continue
		}
		for i, site := range value.Content {
			name := fmt.Sprintf("site %d", i+1)
			if n, ok := mappingEntries(site)["name"]; ok {
				name = "site " + n.Value
			}
			for siteKey := range mappingEntries(site) {
				if _, known := knownSiteKeys[siteKey]; !known {
					warnings = append(warnings, fmt.Sprintf("%s: unknown key %q", name, siteKey))
				}
			}
		}
	}
	return warnings
}

// mappingEntries returns a mapping node's keys and values; nil for any other node.
func mappingEntries(n *yaml.Node) map[string]*yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	entries := make(map[string]*yaml.Node, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		entries[n.Content[i].Value] = n.Content[i+1]
	}
	return entries
}

func keySet(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	return set
}
