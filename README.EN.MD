<p align="center">
  <img src="winchun.png" alt="WinChun Logo" width="150">
</p>

<h1 align="center">🥋 WinChun ☯️</h1>

<p align="center">
  <a href="https://github.com/tombreplacer/winchun/actions/workflows/release.yml"><img src="https://github.com/tombreplacer/winchun/actions/workflows/release.yml/badge.svg" alt="Release Status"></a>
  <a href="https://github.com/tombreplacer/winchun/releases"><img src="https://img.shields.io/github/v/release/tombreplacer/winchun?logo=github&color=blue" alt="GitHub Release"></a>
</p>


This program is designed to bypass regional restrictions by redirecting selected domains to a SOCKS proxy **without enabling a global tunnel mode** on the VPN client itself.

**Motivation:** On Linux, such targeted policy routing (or process-based routing) is fairly simple to implement using `iptables` and `ip rule`. However, there wasn't a good, stable, and free alternative for Windows that works at the system level without clunky workarounds. WinChun solves this problem elegantly and works out of the box.

WinChun works system-wide: it intercepts traffic destined for the selected websites from any application (browsers, games, CLI tools, etc.) and wraps it into your SOCKS5 proxy, leaving all other traffic to go directly through your ISP.

## How it works

```text
[Any Application] → [Windows Routing Table]
    ├── Domains from list   → [TUN adapter] → [tun2socks] → [SOCKS5] → Internet
    └── All other traffic   → [Default Gateway] → Direct Connection
```

1. The program launches `tun2socks` and creates a virtual network adapter (TUN) named `winchun0`.
2. Domains from the configuration file are resolved into IP addresses.
3. Routes for these IPs are added to the Windows routing table to point to the TUN adapter.
4. A **built-in WFP (Windows Filtering Platform) filter** prevents routing loops by blocking the proxy client from sending traffic back into the TUN adapter.
5. Upon exiting (via `Ctrl+C`), all routes and WFP rules are cleanly removed, leaving the system pristine.

## Requirements

- Windows 10/11
- Administrator privileges (required to create the TUN adapter and WFP rules)
- A working SOCKS5 proxy (e.g., Xray, v2rayN, Hiddify, etc.)

## Installation and Usage

Only two files in the same folder are required to run the program: `winchun.exe` and `config.json`.
The `wintun.dll` library and `tun2socks.exe` utility are already bundled inside the executable using `go:embed`.

### 1. Configuration

Create or copy `config.json` (there is a `config.json.example` available in the repository):

```json
{
  "socks5": "127.0.0.1:10810",
  "tun_name": "winchun0",
  "tun_addr": "10.0.85.2",
  "tun_gw": "10.0.85.1",
  "tun_mask": "255.255.255.0",
  "dns_refresh_seconds": 120,
  "upstream_ips": [],
  "domains": [
    "chatgpt.com",
    "*.ipinfo.io"
  ]
}
```

- Specify the correct port for your `socks5` proxy.
- Add the required domains to the `domains` array.
- In `upstream_ips`, you can list the IP addresses of your VPS servers (optional, for additional protection against blocking).

### 2. How to run and test

1. Launch your proxy client (Xray/v2ray/etc.). Make sure the SOCKS5 proxy is running.
   * *How to check your SOCKS5:* Run the following command in PowerShell:
     `curl -x socks5://127.0.0.1:10810 https://ipinfo.io` (replace with your actual port). If it returns the IP of your VPN, the proxy is working.
2. Open a console as **Administrator**.
3. Run `winchun.exe`.
4. Check if the program is working using `ipinfo.io` as an example:
   * **WITHOUT WinChun** (or if `ipinfo.io` is not in the list): Visit `https://ipinfo.io` in your browser. You should see your real home IP address.
   * **WITH WinChun running** (and `*.ipinfo.io` in `config.json`): Visit `https://ipinfo.io` again. You should now see the IP address of your VPN server.

## Stopping the program

Press `Ctrl+C` in the console. The utility will:
1. Delete all added routes.
2. Stop `tun2socks` and remove the virtual adapter.
3. Automatically clean up the WFP rules.

## Building from source

1. Install Go.
2. Run the `build.bat` script (it executes `go build -o winchun.exe ./src`).
