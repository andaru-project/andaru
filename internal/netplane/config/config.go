// SPDX-License-Identifier: AGPL-3.0-or-later
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/andaru-project/andaru/internal/netplane/driver"
	"github.com/andaru-project/andaru/internal/netplane/model"
)

// ServerConfig configures the HTTP/SSE server and Home Assistant Ingress behavior.
type ServerConfig struct {
	Listen              string   `yaml:"listen"`
	DBPath              string   `yaml:"db_path"`
	Retention           string   `yaml:"retention"`
	TrustIngressProxy   bool     `yaml:"trust_ingress_proxy"`
	TrustedIngressCIDRs []string `yaml:"trusted_ingress_cidrs"`
	AllowedHosts        []string `yaml:"allowed_hosts"`
	TrustedOrigins      []string `yaml:"trusted_origins"`
	APITokenSecret      string   `yaml:"api_token_secret"`
	ResolvedAPIToken    string   `yaml:"-"`
}

// TargetYAML is the declarative YAML representation of one polling target.
type TargetYAML struct {
	ID           string            `yaml:"id"`
	Name         string            `yaml:"name"`
	Driver       string            `yaml:"driver"`
	Endpoint     string            `yaml:"endpoint"`
	Enabled      *bool             `yaml:"enabled"`
	PollInterval string            `yaml:"poll_interval"`
	Settings     map[string]any    `yaml:"settings"`
	Secrets      map[string]string `yaml:"secrets"` // map[logicalKey]ENV_VAR_NAME
}

// FileConfig is the top-level netplane YAML configuration document.
type FileConfig struct {
	Server  ServerConfig `yaml:"server"`
	Targets []TargetYAML `yaml:"targets"`
}

// Config is the validated runtime configuration with resolved secrets (never serialized).
type Config struct {
	Server    ServerConfig
	Retention time.Duration
	Targets   []driver.TargetConfig
}

// LoadFile reads and validates a YAML configuration file, resolving secret references from getenv.
func LoadFile(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(raw, os.LookupEnv)
}

// hostNameRe matches a bare DNS host name: dot-separated labels of letters, digits, '-'
// and '_', not starting or ending with '-'.
var hostNameRe = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*$`)

// canonicalOrigin returns o as a browser serializes an Origin (lowercase scheme and host, no
// default port), because trusted origins are compared to the Origin header byte for byte.
func canonicalOrigin(o string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(o))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("want scheme://host[:port] with an http or https scheme")
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}

// TokenEnv is the environment variable read for the API token when the config names none.
const TokenEnv = "NETPLANE_API_TOKEN"

// ApplyHAOptions exports the Home Assistant add-on options file (/data/options.json) to the
// environment: api_token as NETPLANE_API_TOKEN and each secrets entry under its own name, so
// config secret references (${NAME}) resolve inside the add-on.
func ApplyHAOptions(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var opts struct {
		APIToken string `json:"api_token"`
		Secrets  []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"secrets"`
	}
	if err := json.Unmarshal(raw, &opts); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if opts.APIToken != "" {
		if err := os.Setenv(TokenEnv, opts.APIToken); err != nil {
			return err
		}
	}
	for _, sec := range opts.Secrets {
		if sec.Name == "" {
			return fmt.Errorf("parse %s: secret with empty name", path)
		}
		if sec.Name == TokenEnv {
			return fmt.Errorf("parse %s: secret name %s is reserved; set api_token instead", path, TokenEnv)
		}
		if err := os.Setenv(sec.Name, sec.Value); err != nil {
			return err
		}
	}
	return nil
}

// Parse decodes YAML bytes and resolves secret references via lookupEnv.
func Parse(data []byte, lookupEnv func(string) (string, bool)) (*Config, error) {
	var fc FileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a misspelled key (say, api_token_secret) must not silently drop auth
	if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse YAML config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("parse YAML config: multiple YAML documents are not supported")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse YAML config: %w", err)
	}

	if fc.Server.Listen == "" {
		fc.Server.Listen = ":8099"
	}
	if fc.Server.DBPath == "" {
		fc.Server.DBPath = "netplane.db"
	}
	retention := 24 * time.Hour
	if fc.Server.Retention != "" {
		d, err := time.ParseDuration(fc.Server.Retention)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid server.retention %q", fc.Server.Retention)
		}
		retention = d
	}
	for i, h := range fc.Server.AllowedHosts {
		n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
		if !hostNameRe.MatchString(n) {
			return nil, fmt.Errorf("server.allowed_hosts[%d] %q: want a bare ASCII host name (punycode for IDNs; no scheme, port, path or wildcard)", i, h)
		}
		fc.Server.AllowedHosts[i] = n
	}
	for i, o := range fc.Server.TrustedOrigins {
		c, err := canonicalOrigin(o)
		if err != nil {
			return nil, fmt.Errorf("server.trusted_origins[%d] %q: %w", i, o, err)
		}
		fc.Server.TrustedOrigins[i] = c
	}
	if fc.Server.APITokenSecret != "" {
		envName := stripEnvSyntax(fc.Server.APITokenSecret)
		if val, ok := lookupEnv(envName); ok && val != "" {
			fc.Server.ResolvedAPIToken = val
		} else {
			return nil, fmt.Errorf("missing environment variable %q for server.api_token_secret", envName)
		}
	} else if val, ok := lookupEnv(TokenEnv); ok {
		fc.Server.ResolvedAPIToken = val
	}

	seen := make(map[string]bool, len(fc.Targets))
	targets := make([]driver.TargetConfig, 0, len(fc.Targets))
	for i, ty := range fc.Targets {
		if ty.ID == "" {
			return nil, fmt.Errorf("targets[%d]: id is required", i)
		}
		if seen[ty.ID] {
			return nil, fmt.Errorf("targets[%d]: duplicate target id %q", i, ty.ID)
		}
		seen[ty.ID] = true
		if ty.Driver == "" {
			return nil, fmt.Errorf("target %q: driver is required", ty.ID)
		}
		enabled := true
		if ty.Enabled != nil {
			enabled = *ty.Enabled
		}
		pollInterval := 15 * time.Second
		if ty.PollInterval != "" {
			d, err := time.ParseDuration(ty.PollInterval)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("target %q: invalid poll_interval %q", ty.ID, ty.PollInterval)
			}
			pollInterval = d
		}
		name := ty.Name
		if name == "" {
			name = ty.ID
		}
		secretRefs := make(map[string]model.SecretRef, len(ty.Secrets))
		resolved := make(map[string]string, len(ty.Secrets))
		for k, ref := range ty.Secrets {
			envName := stripEnvSyntax(ref)
			secretRefs[k] = model.SecretRef{Secret: envName}
			if val, ok := lookupEnv(envName); ok {
				resolved[k] = val
			} else if enabled {
				return nil, fmt.Errorf("target %q: missing env var %q for secret %q", ty.ID, envName, k)
			}
		}

		targets = append(targets, driver.TargetConfig{
			ID:              ty.ID,
			Name:            name,
			Driver:          ty.Driver,
			Endpoint:        ty.Endpoint,
			Enabled:         enabled,
			PollInterval:    pollInterval,
			Settings:        ty.Settings,
			SecretRefs:      secretRefs,
			ResolvedSecrets: resolved,
		})
	}

	return &Config{
		Server:    fc.Server,
		Retention: retention,
		Targets:   targets,
	}, nil
}

func stripEnvSyntax(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		return strings.TrimSpace(s[2 : len(s)-1])
	}
	return strings.TrimPrefix(s, "$")
}
