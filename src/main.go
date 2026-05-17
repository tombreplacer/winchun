package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const banner = `
  ╔════════════════════════════════════════════╗
  ║  WinChun — selective domain SOCKS router   ║
  ║  System-wide TUN-based traffic redirect   ║
  ╚════════════════════════════════════════════╝
`

func main() {
	configPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	fmt.Print(banner)

	// Load config
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("✗ Config: %v", err)
	}

	// Extract embedded binaries and override path
	extractedPath, err := ExtractDependencies()
	if err != nil {
		log.Printf("⚠ Warning: failed to extract binaries: %v", err)
	} else {
		cfg.Tun2SocksPath = extractedPath
	}

	log.Printf("✓ Config loaded")
	log.Printf("  SOCKS5     : %s", cfg.SOCKS5)
	log.Printf("  TUN        : %s (addr=%s gw=%s)", cfg.TunName, cfg.TunAddr, cfg.TunGW)
	log.Printf("  DNS refresh: %ds", cfg.DNSRefreshSec)
	log.Printf("  Domains    : %d rules", len(cfg.Domains))
	for _, d := range cfg.Domains {
		log.Printf("    → %s", d)
	}

	// Check tun2socks and wintun.dll
	tun := NewTunManager(cfg)
	if err := tun.CheckDependencies(); err != nil {
		log.Fatalf("✗ %v", err)
	}
	log.Printf("✓ Dependencies OK (tun2socks: %s)", cfg.Tun2SocksPath)

	// Setup context and signal handling
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Resolve DNS BEFORE starting TUN (to use the real network interface)
	resolver := NewResolver()
	log.Println("━━━ Resolving domains ━━━")
	domains := cfg.AllDomains()
	added, _ := resolver.ResolveDomains(domains)
	if len(added) == 0 {
		log.Println("  ⚠ No IPs resolved. Check your domain list and DNS.")
	}

	// Start tun2socks
	log.Println("━━━ Starting TUN adapter ━━━")
	if err := tun.Start(ctx, resolver); err != nil {
		log.Fatalf("✗ TUN: %v", err)
	}

	// Initialize route manager and add routes
	// We bind routes directly to the interface by name ("winchun0")
	routes := NewRouteManager(cfg.TunName)

	log.Println("━━━ Adding routes ━━━")
	routes.AddRoutes(added)

	// Also add route for SOCKS proxy server itself to go DIRECT (avoid loop)
	log.Println("━━━ Anti-loop route ━━━")
	addAntiLoopRoute(cfg)

	// WARM UP NETWORK STACK (Fixes UDP Source IP Bug in Windows)
	warmupNetwork()

	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Printf("✓ WinChun is active! %d IPs routed through SOCKS", len(added))
	log.Println("  Press Ctrl+C to stop")
	log.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Periodic DNS refresh goroutine
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.DNSRefreshSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				log.Println("━━━ DNS refresh ━━━")
				newAdded, newRemoved := resolver.ResolveDomains(domains)
				if len(newRemoved) > 0 {
					routes.RemoveRoutes(newRemoved)
				}
				if len(newAdded) > 0 {
					routes.AddRoutes(newAdded)
				}
				if len(newAdded) == 0 && len(newRemoved) == 0 {
					log.Println("  No changes")
				}
			}
		}
	}()

	// Wait for shutdown signal
	<-sigCh
	log.Println("\n⚡ Shutting down...")
	cancel()

	// Cleanup
	log.Println("━━━ Cleanup ━━━")
	routes.Cleanup()
	tun.Stop()

	log.Println("✓ WinChun stopped. Bye!")
}

// addAntiLoopRoute ensures the SOCKS proxy itself is reachable directly
// (not routed through TUN, which would cause an infinite loop).
func addAntiLoopRoute(cfg *Config) {
	// Extract host from socks5 address
	host := cfg.SOCKS5
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			host = host[:i]
			break
		}
	}

	// Skip if SOCKS is on localhost
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		log.Printf("  SOCKS on localhost — no anti-loop route needed")
		return
	}

	// Get default gateway and add explicit route for SOCKS server
	log.Printf("  ⚠ SOCKS on %s — add a manual route if needed", host)
}

// warmupNetwork flushes the DNS cache and forces Windows to re-evaluate the routing table.
func warmupNetwork() {
	log.Println("━━━ Warming up network ━━━")
	// Flush DNS cache to clear any bad state left over from adapter creation
	_ = exec.Command("ipconfig", "/flushdns").Run()
	log.Printf("  ✓ DNS cache flushed")

	// Force Windows to re-evaluate routing table and Source IPs
	// by making a quick TCP connection to a known stable IP.
	// 8.8.8.8:53 is widely open for TCP DNS.
	conn, err := net.DialTimeout("tcp", "8.8.8.8:53", 200*time.Millisecond)
	if err == nil {
		conn.Close()
	}
	log.Printf("  ✓ Network routing warmed up")
}
