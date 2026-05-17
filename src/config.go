package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config holds the application configuration.
type Config struct {
	// SOCKS5 proxy upstream address (e.g. "127.0.0.1:1080")
	SOCKS5 string `json:"socks5"`

	// TUN adapter settings
	TunName string `json:"tun_name"`
	TunAddr string `json:"tun_addr"`
	TunGW   string `json:"tun_gw"`
	TunMask string `json:"tun_mask"`

	// How often to re-resolve domain DNS (seconds)
	DNSRefreshSec int `json:"dns_refresh_seconds"`

	// Path to tun2socks executable (auto-detected if empty)
	Tun2SocksPath string `json:"tun2socks_path"`

	// Explicit IPs to route through the default gateway (bypass TUN)
	UpstreamIPs []string `json:"upstream_ips"`

	// Logging settings
	LogStdout *bool  `json:"log_stdout"` // Log to console (default: true)
	LogFile   string `json:"log_file"`   // Log to file path (empty = disabled)

	// Domains to route through SOCKS
	Domains []string `json:"domains"`

	// Parsed rules
	exactDomains  map[string]bool
	suffixDomains []string
}

// LoadConfig reads and parses the config file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Defaults
	if cfg.SOCKS5 == "" {
		return nil, fmt.Errorf("socks5 address is required")
	}
	if cfg.TunName == "" {
		cfg.TunName = "winchun0"
	}
	if cfg.TunAddr == "" {
		cfg.TunAddr = "10.0.85.2"
	}
	if cfg.TunGW == "" {
		cfg.TunGW = "10.0.85.1"
	}
	if cfg.TunMask == "" {
		cfg.TunMask = "255.255.255.0"
	}
	if cfg.DNSRefreshSec <= 0 {
		cfg.DNSRefreshSec = 120
	}
	if cfg.Tun2SocksPath == "" {
		cfg.Tun2SocksPath = "tun2socks.exe"
	}
	if len(cfg.Domains) == 0 {
		return nil, fmt.Errorf("at least one domain is required")
	}

	// Parse domain rules
	cfg.exactDomains = make(map[string]bool)
	for _, d := range cfg.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if strings.HasPrefix(d, "*.") {
			suffix := d[1:] // ".example.com"
			cfg.suffixDomains = append(cfg.suffixDomains, suffix)
			cfg.exactDomains[d[2:]] = true
		} else {
			cfg.exactDomains[d] = true
		}
	}

	return &cfg, nil
}

// MatchDomain checks if a hostname should be routed through SOCKS.
func (c *Config) MatchDomain(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if c.exactDomains[hostname] {
		return true
	}
	for _, suffix := range c.suffixDomains {
		if strings.HasSuffix(hostname, suffix) {
			return true
		}
	}
	return false
}

// AllDomains returns deduplicated list of base domains to resolve.
func (c *Config) AllDomains() []string {
	seen := make(map[string]bool)
	var result []string
	for _, d := range c.Domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimPrefix(d, "*.")
		if !seen[d] {
			seen[d] = true
			result = append(result, d)
		}
	}
	return result
}
