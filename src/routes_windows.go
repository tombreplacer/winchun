package main

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
)

// RouteManager manages Windows routing table entries for intercepted domains.
type RouteManager struct {
	mu        sync.Mutex
	ifaceName string
	routes    map[string]bool // currently active routes (IP → true)
}

// NewRouteManager creates a new route manager.
func NewRouteManager(ifaceName string) *RouteManager {
	return &RouteManager{
		ifaceName: ifaceName,
		routes:    make(map[string]bool),
	}
}

// AddRoute adds a route for a specific IP through the TUN adapter.
func (rm *RouteManager) AddRoute(ip string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rm.routes[ip] {
		return nil // already added
	}

	// netsh interface ipv4 add route <IP>/32 "winchun0" metric=1 store=active
	cmd := exec.Command("netsh", "interface", "ipv4", "add", "route",
		ip+"/32", rm.ifaceName, "metric=1", "store=active")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("route add %s: %s: %w", ip, strings.TrimSpace(string(output)), err)
	}

	rm.routes[ip] = true
	return nil
}

// RemoveRoute removes a route for a specific IP.
func (rm *RouteManager) RemoveRoute(ip string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if !rm.routes[ip] {
		return nil
	}

	// netsh interface ipv4 delete route <IP>/32 "winchun0"
	cmd := exec.Command("netsh", "interface", "ipv4", "delete", "route",
		ip+"/32", rm.ifaceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(output))
		// If the interface went down (e.g. tun2socks exited), Windows auto-deletes the routes.
		// "Element not found" means it's already gone, which is fine.
		if strings.Contains(outStr, "Element not found") {
			delete(rm.routes, ip)
			return nil
		}
		return fmt.Errorf("route delete %s: %s: %w", ip, outStr, err)
	}

	delete(rm.routes, ip)
	return nil
}

// AddRoutes adds routes for multiple IPs.
func (rm *RouteManager) AddRoutes(ips []string) {
	for _, ip := range ips {
		if err := rm.AddRoute(ip); err != nil {
			log.Printf("  ⚠ Failed to add route for %s: %v", ip, err)
		} else {
			log.Printf("  + Route added: %s → TUN", ip)
		}
	}
}

// RemoveRoutes removes routes for multiple IPs.
func (rm *RouteManager) RemoveRoutes(ips []string) {
	for _, ip := range ips {
		if err := rm.RemoveRoute(ip); err != nil {
			log.Printf("  ⚠ Failed to remove route for %s: %v", ip, err)
		} else {
			log.Printf("  - Route removed: %s", ip)
		}
	}
}

// Cleanup removes all managed routes.
func (rm *RouteManager) Cleanup() {
	rm.mu.Lock()
	ips := make([]string, 0, len(rm.routes))
	for ip := range rm.routes {
		ips = append(ips, ip)
	}
	rm.mu.Unlock()

	for _, ip := range ips {
		if err := rm.RemoveRoute(ip); err != nil {
			log.Printf("  ⚠ Cleanup failed for route %s: %v", ip, err)
		}
	}
	log.Printf("  ✓ Cleaned up %d routes", len(ips))
}
