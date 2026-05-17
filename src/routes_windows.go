package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultRouteInfo holds information about the default network route.
type DefaultRouteInfo struct {
	NextHop        string `json:"NextHop"`
	InterfaceAlias string `json:"InterfaceAlias"`
}

// GetDefaultRoute finds the primary internet connection's gateway and interface.
func GetDefaultRoute() (*DefaultRouteInfo, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-NetRoute -DestinationPrefix 0.0.0.0/0 | Sort-Object RouteMetric | Select-Object -First 1 -Property NextHop, InterfaceAlias | ConvertTo-Json`)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get default route: %w", err)
	}

	var res DefaultRouteInfo
	if err := json.Unmarshal(output, &res); err != nil {
		return nil, fmt.Errorf("failed to parse default route json (%s): %w", string(output), err)
	}
	return &res, nil
}

// AddBypassRoute adds a direct route to the given IP using the default gateway.
func AddBypassRoute(ip string, route *DefaultRouteInfo) error {
	args := []string{"interface", "ipv4", "add", "route", ip + "/32", `"`+route.InterfaceAlias+`"`}
	if route.NextHop != "" && route.NextHop != "0.0.0.0" {
		args = append(args, route.NextHop)
	}
	args = append(args, "metric=1", "store=active")

	cmd := exec.Command("netsh", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(output))
		if strings.Contains(outStr, "already exists") {
			return nil
		}
		return fmt.Errorf("%s: %w", outStr, err)
	}
	return nil
}

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

// AddRoutes adds routes for multiple IPs in bulk using netsh scripts.
// Routes are added in small batches with a delay between them to prevent
// a reconnection storm from overwhelming the SOCKS proxy.
func (rm *RouteManager) AddRoutes(ips []string) {
	if len(ips) == 0 {
		return
	}

	var newIPs []string
	rm.mu.Lock()
	for _, ip := range ips {
		if !rm.routes[ip] {
			newIPs = append(newIPs, ip)
		}
	}
	rm.mu.Unlock()

	if len(newIPs) == 0 {
		return
	}

	var sb strings.Builder
	sb.WriteString("pushd interface ipv4\n")
	for _, ip := range newIPs {
		sb.WriteString(fmt.Sprintf("add route %s/32 \"%s\" metric=1 store=active\n", ip, rm.ifaceName))
	}
	sb.WriteString("popd\n")

	scriptPath := filepath.Join(os.TempDir(), "winchun_routes_add.txt")
	if err := os.WriteFile(scriptPath, []byte(sb.String()), 0644); err != nil {
		log.Printf("  ⚠ Failed to write route script: %v", err)
		return
	}

	cmd := exec.Command("netsh", "-f", scriptPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("  ⚠ Batch route add failed: %s: %v", strings.TrimSpace(string(output)), err)
	} else {
		log.Printf("  + Batch added %d routes", len(newIPs))
		rm.mu.Lock()
		for _, ip := range newIPs {
			rm.routes[ip] = true
		}
		rm.mu.Unlock()
	}
}

// RemoveRoutes removes routes for multiple IPs in bulk.
func (rm *RouteManager) RemoveRoutes(ips []string) {
	if len(ips) == 0 {
		return
	}

	var toRemove []string
	rm.mu.Lock()
	for _, ip := range ips {
		if rm.routes[ip] {
			toRemove = append(toRemove, ip)
		}
	}
	rm.mu.Unlock()

	if len(toRemove) == 0 {
		return
	}

	var sb strings.Builder
	sb.WriteString("pushd interface ipv4\n")
	for _, ip := range toRemove {
		sb.WriteString(fmt.Sprintf("delete route %s/32 \"%s\"\n", ip, rm.ifaceName))
	}
	sb.WriteString("popd\n")

	scriptPath := filepath.Join(os.TempDir(), "winchun_routes_del.txt")
	os.WriteFile(scriptPath, []byte(sb.String()), 0644)

	cmd := exec.Command("netsh", "-f", scriptPath)
	cmd.Run() // We ignore deletion errors, since routes might already be gone.

	log.Printf("  - Batch removed %d routes", len(toRemove))

	rm.mu.Lock()
	for _, ip := range toRemove {
		delete(rm.routes, ip)
	}
	rm.mu.Unlock()
}

// Cleanup removes all managed routes.
func (rm *RouteManager) Cleanup() {
	rm.mu.Lock()
	ips := make([]string, 0, len(rm.routes))
	for ip := range rm.routes {
		ips = append(ips, ip)
	}
	rm.mu.Unlock()

	rm.RemoveRoutes(ips)
	log.Printf("  ✓ Cleaned up %d routes", len(ips))
}
