package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tailscale/wf"
)

// WFPManager manages Windows Filtering Platform rules to prevent loops.
type WFPManager struct {
	session *wf.Session
	cfg     *Config
}

// NewWFPManager initializes WFP session.
func NewWFPManager(cfg *Config) (*WFPManager, error) {
	session, err := wf.New(&wf.Options{
		Name:        "WinChun WFP",
		Description: "Blocks proxy from using TUN interface to prevent loops",
		Dynamic:     true, // Automatically clean up rules when WinChun exits
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize WFP session: %w", err)
	}

	return &WFPManager{
		session: session,
		cfg:     cfg,
	}, nil
}

// Close terminates the WFP session and cleans up rules.
func (w *WFPManager) Close() {
	if w.session != nil {
		w.session.Close()
	}
}

// BlockProcessOnTUN blocks the specified executable from routing through the TUN adapter.
func (w *WFPManager) BlockProcessOnTUN(exePath string) error {
	appID, err := wf.AppID(exePath)
	if err != nil {
		return fmt.Errorf("failed to get AppID for %s: %w", exePath, err)
	}

	tunIP := net.ParseIP(w.cfg.TunAddr).To4()
	if tunIP == nil {
		return fmt.Errorf("invalid TUN IPv4 address: %s", w.cfg.TunAddr)
	}

	rule := &wf.Rule{
		Name:        "Block " + filepath.Base(exePath) + " on TUN",
		Description: "Prevents routing loops by blocking proxy outbound via TUN",
		Layer:       wf.LayerALEAuthConnectV4,
		Action:      wf.ActionBlock,
		Weight:      1000,
		Conditions: []*wf.Match{
			{
				Field: wf.FieldALEAppID,
				Op:    wf.MatchTypeEqual,
				Value: appID,
			},
			{
				Field: wf.FieldIPLocalAddress,
				Op:    wf.MatchTypeEqual,
				Value: tunIP,
			},
		},
	}

	err = w.session.AddRule(rule)
	if err != nil {
		return fmt.Errorf("failed to add WFP rule: %w", err)
	}

	log.Printf("  ✓ WFP: Blocked %s from using TUN", filepath.Base(exePath))
	return nil
}

// FindProcessByPort tries to find the executable path of the process listening on the given TCP port.
func FindProcessByPort(port string) string {
	// netstat -ano | findstr :PORT
	cmd := exec.Command("cmd", "/c", fmt.Sprintf("netstat -ano | findstr :%s", port))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}

	var pid string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "LISTENING") {
			parts := strings.Fields(line)
			if len(parts) >= 5 {
				pid = parts[len(parts)-1]
				break
			}
		}
	}

	if pid == "" || pid == "0" {
		return ""
	}

	// Get executable path using wmic
	cmd = exec.Command("wmic", "process", "where", "processid="+pid, "get", "ExecutablePath")
	out, err = cmd.CombinedOutput()
	if err != nil {
		return ""
	}

	lines = strings.Split(string(out), "\n")
	if len(lines) >= 2 {
		exePath := strings.TrimSpace(lines[1])
		if exePath != "" {
			return exePath
		}
	}

	return ""
}
