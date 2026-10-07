package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// Diagnostic observations never supply a quote, configuration or availability.
// UIA must still expose the unique document and fresh product regions.
func withNativeContentProbe(call nativeCaller, probe func(context.Context, nativePage, nativeRequest) (string, error)) nativeCaller {
	reads, probes := 0, 0
	return func(ctx context.Context, r nativeRequest) (nativePage, error) {
		p, err := call(ctx, r)
		if err != nil || !nativeContentPending(p) || !nativeRegionIdentity(p, r) || ctx.Err() != nil {
			return p, err
		}
		if r.Action != "open" && r.Action != "snapshot" && r.Action != "validate" {
			return p, err // The requested operation did not run.
		}
		// Observe once initially and once after eight still-pending snapshots.
		// Routine identity validation does not trigger repeated DOM inspection.
		if r.Action != "validate" {
			reads++
		}
		if probes > 0 && (probes >= 2 || reads < 9 || r.Action == "validate") {
			return p, err
		}
		probes++
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		status, probeErr := probe(probeCtx, p, r)
		cancel()
		if probeErr != nil {
			log.Printf("owned content diagnostic unavailable: model=%s handle=%d error=%v; no quote accepted", dellPlatform(r.URL), p.Handle, probeErr)
		} else {
			log.Printf("owned content diagnostic: model=%s handle=%d %s; diagnostic only, no quote accepted", dellPlatform(r.URL), p.Handle, status)
		}
		return p, err
	}
}

//go:embed owned_content_status.js
var ownedContentStatusScript string

func ownedContentStatusExpression(p nativePage) string {
	url, _ := json.Marshal(p.URL)
	return "(" + ownedContentStatusScript + ")(" + string(url) + ")"
}

func verifyOwnedContentStatus(p nativePage, raw string) (string, error) {
	var status struct {
		URL     string `json:"url"`
		State   string `json:"readyState"`
		Title   string `json:"title"`
		Blocked bool   `json:"blocked"`
		Regions map[string]struct {
			Present bool `json:"present"`
			Visible bool `json:"visible"`
		} `json:"regions"`
	}
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &status) != nil || status.URL != p.URL {
		return "", fmt.Errorf("后台商品加载诊断未对应当前网址")
	}
	// Marshal only the diagnostic schema, discarding any unexpected fields.
	clean, _ := json.Marshal(status)
	return string(clean), nil
}
