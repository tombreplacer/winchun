package main

import (
	"log"
	"net"
	"sync"
)

// Resolver resolves domain names to IP addresses and caches the results.
type Resolver struct {
	mu      sync.RWMutex
	// domain → set of IPs
	cache   map[string]map[string]bool
	// IP → domain (reverse lookup for logging)
	reverse map[string]string
}

// NewResolver creates a new DNS resolver.
func NewResolver() *Resolver {
	return &Resolver{
		cache:   make(map[string]map[string]bool),
		reverse: make(map[string]string),
	}
}

// ResolveDomains resolves a list of domains and returns newly discovered IPs.
func (r *Resolver) ResolveDomains(domains []string) (added []string, removed []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Track all current IPs before refresh
	oldIPs := make(map[string]bool)
	for _, ips := range r.cache {
		for ip := range ips {
			oldIPs[ip] = true
		}
	}

	// Resolve all domains
	newAllIPs := make(map[string]bool)
	for _, domain := range domains {
		ips, err := net.LookupHost(domain)
		if err != nil {
			log.Printf("  ⚠ DNS lookup failed for %s: %v", domain, err)
			// Keep existing cache for this domain
			if existing, ok := r.cache[domain]; ok {
				for ip := range existing {
					newAllIPs[ip] = true
				}
			}
			continue
		}

		ipSet := make(map[string]bool)
		for _, ip := range ips {
			// Only use IPv4 for route management simplicity
			parsed := net.ParseIP(ip)
			if parsed != nil && parsed.To4() != nil {
				ipSet[ip] = true
				newAllIPs[ip] = true
				r.reverse[ip] = domain
			}
		}

		if len(ipSet) > 0 {
			r.cache[domain] = ipSet
			ipList := make([]string, 0, len(ipSet))
			for ip := range ipSet {
				ipList = append(ipList, ip)
			}
			log.Printf("  ✓ %s → %v", domain, ipList)
		}
	}

	// Find added IPs (in new but not in old)
	for ip := range newAllIPs {
		if !oldIPs[ip] {
			added = append(added, ip)
		}
	}

	// Find removed IPs (in old but not in new)
	for ip := range oldIPs {
		if !newAllIPs[ip] {
			removed = append(removed, ip)
		}
	}

	return added, removed
}

// AllIPs returns all currently cached IPs.
func (r *Resolver) AllIPs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	seen := make(map[string]bool)
	var result []string
	for _, ips := range r.cache {
		for ip := range ips {
			if !seen[ip] {
				seen[ip] = true
				result = append(result, ip)
			}
		}
	}
	return result
}

// DomainForIP returns the domain associated with an IP (for logging).
func (r *Resolver) DomainForIP(ip string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.reverse[ip]
}
