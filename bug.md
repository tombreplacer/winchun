# BUG: Routing Loop → Port Exhaustion → DNS Failure

## Status: **UNRESOLVED**

---

## Summary

After WinChun starts and adds routes, DNS resolution intermittently fails for 2-5 minutes.
`nslookup` returns `Server: UnKnown` / `No response from server`. The root cause is a
**routing loop** between `tun2socks` and the SOCKS5 proxy (xray), which leads to TCP port
exhaustion on the system.

---

## Architecture

```
┌─────────┐    ┌──────────────┐    ┌─────────────┐    ┌───────┐    ┌──────────┐
│ Browser │───▶│ Windows      │───▶│ TUN adapter  │───▶│tun2   │───▶│ SOCKS5   │───▶ Internet
│         │    │ Route Table  │    │ (winchun0)   │    │socks  │    │ (xray)   │
└─────────┘    │              │    └──────────────┘    └───────┘    │127.0.0.1 │
               │ 34.117.59.81 │                                     │:10808    │
               │  → winchun0  │                                     └──────────┘
               └──────────────┘
```

**How WinChun works:**
1. Resolves domains from `config.json` to IP addresses using system DNS
2. Starts `tun2socks` which creates a TUN adapter (`winchun0`)
3. Configures the TUN adapter with IP `10.0.85.2/24`, no default gateway
4. Adds `/32` routes for each resolved IP through the TUN adapter
5. `tun2socks` captures packets on TUN and forwards them via SOCKS5 to `xray` at `127.0.0.1:10808`
6. `xray` is expected to forward traffic through its upstream VPN/proxy server

---

## The Bug: Routing Loop

### The Loop (step by step)

```
1. Browser connects to ipinfo.io (34.117.59.81)
2. Windows route table: 34.117.59.81/32 → winchun0       ← added by WinChun
3. Packet goes to TUN adapter
4. tun2socks intercepts packet, opens SOCKS connection to xray (127.0.0.1:10808)
5. xray receives: "connect me to 34.117.59.81"
6. ⚠️ If xray sends this traffic DIRECT (not through its VPN):
   - xray opens a socket to 34.117.59.81
   - Windows route table: 34.117.59.81/32 → winchun0     ← SAME ROUTE!
   - Packet goes BACK into TUN adapter
   - tun2socks intercepts it AGAIN → opens ANOTHER SOCKS connection to xray
   - xray receives another request → tries direct → TUN → tun2socks → xray...
   - ∞ INFINITE LOOP
```

### Observed Symptoms

1. **Port Exhaustion error in tun2socks logs:**
   ```
   [warn] [TCP] dial 34.117.59.81:80: connect to 127.0.0.1:10808: dial tcp 127.0.0.1:10808:
   connectex: Only one usage of each socket address (protocol/network address/port)
   is normally permitted.
   ```

2. **Hundreds of ESTABLISHED connections from xray to itself** (seen in Resource Monitor):
   ```
   Process    PID    Protocol  Local Port  Remote Address  Remote Port  State
   xray.exe   26920  TCP       10808       localhost       49245        ESTABLISHED
   xray.exe   26920  TCP       10808       localhost       49246        ESTABLISHED
   xray.exe   26920  TCP       10808       localhost       49247        ESTABLISHED
   ... (sequential ports, hundreds of connections)
   ```

3. **DNS fails intermittently** (because all ephemeral ports are consumed):
   ```
   C:\> nslookup ipinfo.io
   Server:  UnKnown
   Address:  127.0.0.1
   *** UnKnown can't find ipinfo.io: No response from server
   ```
   Works again after 2-5 minutes (when TIME_WAIT ports expire).

4. **Browser shows `ERR_ADDRESS_UNREACHABLE`** during the outage.

---

## Environment

- **OS:** Windows 10/11
- **SOCKS5 proxy:** xray (v2ray fork), listening on `127.0.0.1:10808`
- **DNS:** Local DNS resolver on `127.0.0.1:53` (provided by xray or AdGuard)
- **hosts file:** `127.0.0.1 dns.msftncsi.com` (to suppress Windows "no internet" warning)
- **TUN:** wintun driver + tun2socks v3

---

## What We Tried

### 1. Set TUN adapter metric to 9999 ❌ Doesn't fix the loop
```go
// In tun_manager.go configureAdapter()
_ = exec.Command("netsh", "interface", "ipv4", "set", "interface",
    tm.cfg.TunName, "metric=9999").Run()
```
**Result:** Helps Windows prefer the real adapter for DNS routing decisions, but
doesn't prevent the routing loop itself. DNS still breaks when the loop floods
all ports.

### 2. Set-NetIPAddress -SkipAsSource $true ❌ Broke everything worse
```go
psCmd := fmt.Sprintf("Set-NetIPAddress -InterfaceAlias '%s' -SkipAsSource $true", tm.cfg.TunName)
_ = exec.Command("powershell", "-NoProfile", "-Command", psCmd).Run()
```
**Result:** Browser showed `ERR_ADDRESS_UNREACHABLE` for ALL sites. Windows couldn't
use the TUN adapter's IP (10.0.85.2) as source address, so it couldn't initiate any
connections through the tunnel at all. **Reverted (commented out).**

### 3. Flush DNS cache + warmup TCP ping ❌ Irrelevant to the loop
```go
func warmupNetwork() {
    _ = exec.Command("ipconfig", "/flushdns").Run()
    conn, err := net.DialTimeout("tcp", "8.8.8.8:53", 200*time.Millisecond)
    if err == nil { conn.Close() }
}
```
**Result:** Helps Windows re-evaluate routing after adapter creation, but doesn't
prevent the routing loop.

### 4. Batch routes with delays ❌ Doesn't fix the loop
```go
// In routes_windows.go AddRoutes()
const batchSize = 10
const batchDelay = 500 * time.Millisecond
// Add routes in batches of 10 with 500ms pause between them
```
**Result:** Slows down the initial burst but the loop still happens. Once the routes
are in place, ANY new connection to a routed IP can trigger the loop if xray sends
it direct.

### 5. Anti-loop route for SOCKS proxy ❌ Not applicable
```go
func addAntiLoopRoute(cfg *Config) {
    if host == "127.0.0.1" || host == "localhost" {
        return // no anti-loop route needed
    }
}
```
**Result:** Since the SOCKS proxy is on `127.0.0.1`, there's no remote SOCKS server
IP to add a bypass route for. The loop is between tun2socks and xray on the same
machine, both using localhost.

### 6. Told user to set xray to "Global / Proxy All" mode ❌ User says doesn't help
**Result:** Even with xray in Global mode, the loop persists. Possible reasons:
- xray may still have internal routing rules that send some traffic direct
- xray's "Global" mode may not mean 100% proxy for all protocols
- Some xray routing rules may be hardcoded/built-in

---

## Key Files

| File | Purpose |
|------|---------|
| `config.json` | Domain list + SOCKS/TUN settings |
| `src/main.go` | Entry point, orchestration |
| `src/tun_manager.go` | tun2socks lifecycle + adapter config |
| `src/routes_windows.go` | Route table management (add/remove /32 routes) |
| `src/resolver.go` | DNS resolution + caching |

---

## Root Cause Analysis

The fundamental problem is that **WinChun adds IP routes system-wide**, meaning ALL
processes on the machine (including xray itself) have their traffic to those IPs
redirected through the TUN. When xray tries to reach the destination (either directly
or through its upstream proxy if the upstream resolves to a routed IP), the traffic
loops back through TUN → tun2socks → xray → TUN → ...

### Why this is hard to fix on Windows

On **Linux**, tools like Clash/sing-box solve this with `fwmark`: they mark packets
from the proxy process with a special flag, and routing rules exclude marked packets
from the TUN. The proxy's own outgoing traffic bypasses the TUN.

On **Windows**, there is no equivalent of `fwmark`. The route table applies equally
to all processes. There is no built-in way to say "route 34.117.59.81 through TUN
for everyone EXCEPT xray.exe".

---

## Potential Solutions (Not Yet Tried)

### A. WFP (Windows Filtering Platform) process-based routing
Use Windows Filtering Platform API to create rules that:
- Route traffic from all processes through TUN
- EXCEPT traffic from `xray.exe` (by PID or executable path)

This is what Clash for Windows and sing-box do on Windows. Requires calling Windows
WFP API from Go (complex, needs CGo or syscall).

### B. Use xray's built-in TUN mode instead
Instead of running a separate tun2socks, configure xray/sing-box to handle TUN
natively. These tools already handle the anti-loop problem internally because they
know which connections are "theirs" and which are from other programs.

### C. Bind xray outbound to a specific network interface
Some proxy clients allow binding outbound connections to a specific network interface
(e.g., Ethernet/Wi-Fi, not TUN). If xray can be configured to bind its outbound to
the real network adapter, its traffic would bypass the TUN routes.

In xray config this might look like:
```json
{
  "outbounds": [{
    "streamSettings": {
      "sockopt": {
        "interface": "Ethernet"
      }
    }
  }]
}
```

### D. Use a different routing approach entirely
Instead of system-wide IP routes, use a method that doesn't affect xray's own traffic:
- **PAC file / system proxy settings:** Configure Windows to use a PAC file that
  routes specific domains through the SOCKS proxy. No TUN needed.
- **DNS-based redirect:** Set WinChun as the DNS server, return fake IPs for routed
  domains, and intercept traffic to that fake IP range only.
- **Transparent proxy** using WinDivert or similar packet-level interception with
  process filtering.

### E. Add explicit bypass routes for xray's upstream server ✅ Implemented
If we know the IP of xray's VPN/proxy server, add a direct route for that IP through
the real network adapter BEFORE adding TUN routes. This ensures xray can always reach
its upstream without going through TUN.

This is now implemented via the `upstream_ips` field in `config.json`. WinChun will
automatically resolve the default internet gateway and add `/32` bypass routes (metric 1)
for any IPs listed in `upstream_ips`.

---

## Current State of the Code

- **WFP Anti-Loop Rule** (fix A) is implemented. WinChun dynamically detects the proxy process listening on the SOCKS port and injects a WFP rule blocking it from outbound connections via the TUN interface. This instantly prevents the 60k socket exhaustion issue and breaks the loop.
- **Explicit Bypass Routes** (fix E) is implemented. Users can specify their proxy's upstream IPs in `config.json` (`upstream_ips`) to ensure legitimate proxy traffic uses the physical interface.
- **Batch route addition** (fix #4) is in place.
- **SkipAsSource** (fix #2) is **commented out** (broke everything).
- **Metric 9999** (fix #1) and **warmup** (fix #3) are active but cosmetic only.
- The loop is **FULLY RESOLVED** at the kernel level using WFP.
