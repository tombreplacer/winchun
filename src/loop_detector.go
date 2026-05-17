package main

import (
	"log"
	"sync"
	"time"
)

const (
	loopThreshold = 30               // connections per IP in window = loop
	loopWindow    = 3 * time.Second  // sliding window for counting
	loopCooldown  = 60 * time.Second // how long to keep route removed
)

// LoopDetector monitors per-IP connection rates to detect routing loops.
// When tun2socks creates too many connections to the same destination IP
// in a short time, it means xray is sending that traffic back into the TUN
// (routing loop). The detector removes the offending route to break the loop.
type LoopDetector struct {
	mu        sync.Mutex
	counts    map[string][]time.Time // IP → recent connection timestamps
	blacklist map[string]time.Time   // IP → when blacklisted
	onLoop    func(ip string)        // callback to remove route
}

// NewLoopDetector creates a detector that calls onLoop when a loop is found.
func NewLoopDetector(onLoop func(ip string)) *LoopDetector {
	ld := &LoopDetector{
		counts:    make(map[string][]time.Time),
		blacklist: make(map[string]time.Time),
		onLoop:    onLoop,
	}
	go ld.cleanup()
	return ld
}

// RecordConnection tracks a connection to the given IP.
func (ld *LoopDetector) RecordConnection(ip string) {
	ld.mu.Lock()
	defer ld.mu.Unlock()

	if _, ok := ld.blacklist[ip]; ok {
		return // already blacklisted
	}

	now := time.Now()
	ld.counts[ip] = append(ld.counts[ip], now)

	// Trim timestamps outside window
	cutoff := now.Add(-loopWindow)
	ts := ld.counts[ip]
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	ld.counts[ip] = ts[i:]

	if len(ld.counts[ip]) >= loopThreshold {
		ld.triggerLoop(ip, len(ld.counts[ip]))
	}
}

// RecordPortExhaustion handles "connectex" errors — immediate loop signal.
func (ld *LoopDetector) RecordPortExhaustion(ip string) {
	ld.mu.Lock()
	defer ld.mu.Unlock()
	if _, ok := ld.blacklist[ip]; !ok {
		ld.triggerLoop(ip, -1)
	}
}

// IsBlacklisted returns true if the IP route should NOT be added.
func (ld *LoopDetector) IsBlacklisted(ip string) bool {
	ld.mu.Lock()
	defer ld.mu.Unlock()
	if t, ok := ld.blacklist[ip]; ok {
		if time.Since(t) < loopCooldown {
			return true
		}
		delete(ld.blacklist, ip)
	}
	return false
}

// triggerLoop blacklists an IP and calls the removal callback. Must hold ld.mu.
func (ld *LoopDetector) triggerLoop(ip string, count int) {
	ld.blacklist[ip] = time.Now()
	delete(ld.counts, ip)
	if count > 0 {
		log.Printf("  🔄 LOOP DETECTED: %s (%d conn in %v) — route removed for %v", ip, count, loopWindow, loopCooldown)
	} else {
		log.Printf("  🔄 PORT EXHAUSTION: %s — route removed for %v", ip, loopCooldown)
	}
	if ld.onLoop != nil {
		go ld.onLoop(ip)
	}
}

// cleanup removes expired entries periodically.
func (ld *LoopDetector) cleanup() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		ld.mu.Lock()
		now := time.Now()
		for ip, t := range ld.blacklist {
			if now.Sub(t) >= loopCooldown {
				delete(ld.blacklist, ip)
				log.Printf("  ⟳ Loop cooldown expired for %s", ip)
			}
		}
		cutoff := now.Add(-loopWindow)
		for ip, ts := range ld.counts {
			i := 0
			for i < len(ts) && ts[i].Before(cutoff) {
				i++
			}
			if i == len(ts) {
				delete(ld.counts, ip)
			} else {
				ld.counts[ip] = ts[i:]
			}
		}
		ld.mu.Unlock()
	}
}
