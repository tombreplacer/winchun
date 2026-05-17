package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TunManager manages the tun2socks subprocess and TUN adapter configuration.
type TunManager struct {
	cfg *Config
	cmd *exec.Cmd
}

// NewTunManager creates a new TUN manager.
func NewTunManager(cfg *Config) *TunManager {
	return &TunManager{cfg: cfg}
}

// CheckDependencies verifies that tun2socks.exe and wintun.dll are available.
func (tm *TunManager) CheckDependencies() error {
	// Check tun2socks.exe — first try current directory, then PATH
	path := tm.cfg.Tun2SocksPath
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Try PATH lookup
		found, lookErr := exec.LookPath(path)
		if lookErr != nil {
			return fmt.Errorf("tun2socks.exe not found at %q.\n"+
				"  Download from: https://github.com/xjasonlyu/tun2socks/releases\n"+
				"  Place tun2socks.exe next to winchun.exe or set tun2socks_path in config", path)
		}
		path = found
	}
	// Make path absolute
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	tm.cfg.Tun2SocksPath = path

	// Check wintun.dll (required by tun2socks)
	dir := filepath.Dir(path)
	wintunPath := filepath.Join(dir, "wintun.dll")
	if _, err := os.Stat("wintun.dll"); os.IsNotExist(err) {
		if _, err := os.Stat(wintunPath); os.IsNotExist(err) {
			return fmt.Errorf("wintun.dll not found.\n" +
				"  Download from: https://www.wintun.net/\n" +
				"  Place wintun.dll next to tun2socks.exe")
		}
	}

	return nil
}

// Start launches tun2socks and configures the TUN adapter.
func (tm *TunManager) Start(ctx context.Context, resolver *Resolver) error {
	// Start tun2socks process with info log level to capture traffic.
	// -tcp-auto-tuning: enables TCP receive buffer auto-tuning for better throughput
	// -udp-timeout 30s: reduces UDP session lifetime from default (5m) to 30s,
	//   so DNS-related SOCKS connections close faster and don't pile up in TIME_WAIT.
	tm.cmd = exec.CommandContext(ctx, tm.cfg.Tun2SocksPath,
		"-device", "tun://"+tm.cfg.TunName,
		"-proxy", "socks5://"+tm.cfg.SOCKS5,
		"-loglevel", "info",
		"-tcp-auto-tuning",
		"-udp-timeout", "30s",
	)

	stdout, err := tm.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	tm.cmd.Stderr = tm.cmd.Stdout // Merge stderr into stdout

	go tm.monitorLogs(stdout, resolver)

	log.Printf("  Starting: %s %s", tm.cfg.Tun2SocksPath, strings.Join(tm.cmd.Args[1:], " "))

	if err := tm.cmd.Start(); err != nil {
		return fmt.Errorf("start tun2socks: %w", err)
	}

	// Wait for TUN adapter to appear
	log.Printf("  Waiting for TUN adapter '%s' to come up...", tm.cfg.TunName)
	if err := tm.waitForAdapter(15 * time.Second); err != nil {
		tm.Stop()
		return err
	}

	// Configure the TUN adapter IP address
	if err := tm.configureAdapter(); err != nil {
		tm.Stop()
		return fmt.Errorf("configure adapter: %w", err)
	}

	return nil
}

// waitForAdapter waits until the TUN adapter appears in the system.
func (tm *TunManager) waitForAdapter(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("netsh", "interface", "show", "interface", tm.cfg.TunName)
		if err := cmd.Run(); err == nil {
			log.Printf("  ✓ TUN adapter '%s' is up", tm.cfg.TunName)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("TUN adapter '%s' did not appear within %v", tm.cfg.TunName, timeout)
}

// configureAdapter sets the IP address on the TUN adapter without a default gateway.
// We only add specific host routes, not a default route through TUN.
func (tm *TunManager) configureAdapter() error {
	// netsh interface ip set address "winchun0" static 10.0.85.2 255.255.255.0
	// NOTE: No gateway! A gateway would create a default route and send ALL traffic through TUN.
	cmd := exec.Command("netsh", "interface", "ip", "set", "address",
		tm.cfg.TunName, "static", tm.cfg.TunAddr, tm.cfg.TunMask)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("set adapter IP: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// EXTREMELY IMPORTANT: Wintun defaults to metric 5, which makes Windows think
	// it's the best internet connection. This breaks DNS for 2-3 seconds until Windows
	// realizes it can't route DNS through the TUN.
	// We force the metric to 9999 to give it the lowest possible priority.
	_ = exec.Command("netsh", "interface", "ipv4", "set", "interface", tm.cfg.TunName, "metric=9999").Run()

	log.Printf("  ✓ TUN adapter configured: %s/%s (no default gateway)", tm.cfg.TunAddr, tm.cfg.TunMask)
	return nil
}

// Stop kills the tun2socks process.
func (tm *TunManager) Stop() {
	if tm.cmd != nil && tm.cmd.Process != nil {
		log.Printf("  Stopping tun2socks (PID %d)...", tm.cmd.Process.Pid)
		tm.cmd.Process.Kill()
		tm.cmd.Wait()
		log.Printf("  ✓ tun2socks stopped")
	}
}

// monitorLogs parses tun2socks output, filters noise, and prints traffic logs.
func (tm *TunManager) monitorLogs(r io.Reader, resolver *Resolver) {
	logFile, err := os.OpenFile("winchun_traffic.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Warning: failed to open winchun_traffic.log: %v", err)
	} else {
		defer logFile.Close()
	}

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()

		// Simple heuristics to filter out local broadcast UDP noise
		if strings.Contains(line, "[UDP]") {
			if strings.Contains(line, ":137 <->") || strings.Contains(line, ":138 <->") || strings.Contains(line, "255.255.255.255") || strings.Contains(line, tm.cfg.TunMask) || strings.Contains(line, tm.cfg.TunAddr[:strings.LastIndex(tm.cfg.TunAddr, ".")]+".255") {
				continue
			}
			// filter bind errors for broadcasts
			if strings.Contains(line, "10.0.85.255") {
				continue
			}
		}

		// Try to parse JSON log from tun2socks
		var logEntry struct {
			Level  string `json:"level"`
			Caller string `json:"caller"`
			Msg    string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &logEntry); err == nil {
			// Skip startup and internal info messages
			if strings.HasPrefix(logEntry.Msg, "[STACK]") || strings.Contains(logEntry.Msg, "listen packet") {
				continue
			}
			// Format connection info nicely
			if logEntry.Level == "info" {
				msg := strings.Replace(logEntry.Msg, "dial tcp", "dial", 1)

				// Try to extract destination IP from message (e.g. "[TCP] 10.0.85.2:1234 <-> 142.250.74.46:443")
				parts := strings.Split(msg, "<->")
				if len(parts) == 2 {
					destPart := strings.TrimSpace(parts[1])
					// Remove port
					destIP := destPart
					if idx := strings.LastIndex(destPart, ":"); idx != -1 {
						destIP = destPart[:idx]
					}

					// Lookup domain
					if domain := resolver.DomainForIP(destIP); domain != "" {
						msg = fmt.Sprintf("%s (%s)", msg, domain)
					}

				}

				fmt.Printf("  [redirect] %s\n", msg)

				// Write to log file
				if logFile != nil {
					logFile.WriteString(fmt.Sprintf("[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg))
				}
			} else if logEntry.Level == "warn" || logEntry.Level == "error" {
				// Don't show buffer space errors for UDP
				if !strings.Contains(logEntry.Msg, "buffer space") {
					fmt.Printf("  [warn] %s\n", logEntry.Msg)
				}
			}
		} else {
			// Fallback if not JSON
			if !strings.Contains(line, "buffer space") {
				fmt.Printf("  [tun2socks] %s\n", line)
			}
		}
	}
}
