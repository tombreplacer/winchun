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
	"strings"
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

	// Initialize WFP to block proxy loop
	wfp, err := NewWFPManager(cfg)
	if err != nil {
		log.Printf("  ⚠ WFP Init failed (needs Administrator?): %v", err)
	} else {
		// Extract port from SOCKS5
		port := cfg.SOCKS5
		idx := strings.LastIndex(port, ":")
		if idx != -1 {
			port = port[idx+1:]
		}

		log.Printf("━━━ WFP Anti-Loop ━━━")
		log.Printf("  Finding proxy process listening on port %s...", port)
		proxyExe := FindProcessByPort(port)
		if proxyExe != "" {
			log.Printf("  Found proxy process: %s", proxyExe)
			if err := wfp.BlockProcessOnTUN(proxyExe); err != nil {
				log.Printf("  ⚠ WFP block failed: %v", err)
			}
		} else {
			log.Printf("  ⚠ Proxy process not found on port %s. WFP loop block skipped.", port)
		}
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
	if wfp != nil {
		wfp.Close()
	}

	log.Println("✓ WinChun stopped. Bye!")
}

// addAntiLoopRoute ensures the SOCKS proxy itself and any upstream IPs
// are reachable directly (not routed through TUN, which would cause an infinite loop).
func addAntiLoopRoute(cfg *Config) {
	defRoute, err := GetDefaultRoute()
	if err != nil {
		log.Printf("  ⚠ Failed to get default route for anti-loop: %v", err)
		return
	}
	log.Printf("  ✓ Default internet route via %s (gw: %s)", defRoute.InterfaceAlias, defRoute.NextHop)

	// 1. Check SOCKS proxy IP itself
	host := cfg.SOCKS5
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			host = host[:i]
			break
		}
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		log.Printf("  Adding bypass route for SOCKS proxy %s...", host)
		if err := AddBypassRoute(host, defRoute); err != nil {
			log.Printf("  ⚠ Failed to add bypass route for SOCKS %s: %v", host, err)
		}
	} else {
		log.Printf("  SOCKS on localhost — no anti-loop route needed for SOCKS itself")
	}

	// 2. Check explicit Upstream IPs
	for _, ip := range cfg.UpstreamIPs {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		log.Printf("  Adding bypass route for upstream IP %s...", ip)
		if err := AddBypassRoute(ip, defRoute); err != nil {
			log.Printf("  ⚠ Failed to add bypass route for %s: %v", ip, err)
		}
	}
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
