package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed third_party/tun2socks-v3.exe
var tun2socksBin []byte

//go:embed third_party/wintun.dll
var wintunBin []byte

// ExtractDependencies unpacks embedded binaries to a temporary directory
// and returns the path to tun2socks.exe.
func ExtractDependencies() (string, error) {
	tempDir := filepath.Join(os.TempDir(), "winchun_bin")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", fmt.Errorf("create temp dir: %v", err)
	}

	tun2socksPath := filepath.Join(tempDir, "tun2socks.exe")
	wintunPath := filepath.Join(tempDir, "wintun.dll")

	// Write tun2socks.exe if not exists or size changed
	if stat, err := os.Stat(tun2socksPath); err != nil || stat.Size() != int64(len(tun2socksBin)) {
		if err := os.WriteFile(tun2socksPath, tun2socksBin, 0755); err != nil {
			// Ignore errors if the file is locked by a running instance
		}
	}

	// Write wintun.dll if not exists or size changed
	if stat, err := os.Stat(wintunPath); err != nil || stat.Size() != int64(len(wintunBin)) {
		if err := os.WriteFile(wintunPath, wintunBin, 0755); err != nil {
			// Ignore errors if the file is locked by a running instance
		}
	}

	return tun2socksPath, nil
}
