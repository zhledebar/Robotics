//go:build windows

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ownedDOMProfile(r nativeRequest) string {
	// Nonce from our window slot isolates this browser from all previous runs.
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "PriceStockMonitor", "background_browser_v826", r.Owner, strings.TrimSuffix(filepath.Base(r.EXE), ".exe"))
}

func runNativeReader(ctx context.Context, r nativeRequest) (nativePage, error) {
	if r.Action == "close" || r.Action == "cleanup" {
		page, err := runUIAReader(ctx, r)
		if err == nil && page.Error == "" && page.Handle == 0 && len(r.Owner) == 24 {
			if _, decodeErr := hex.DecodeString(r.Owner); decodeErr == nil {
				// Finalized closure has confirmed the owned processes have exited.
				// Remove only this application's nonce-scoped temporary profile.
				dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "PriceStockMonitor", "background_browser_v826", r.Owner)
				if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
					log.Printf("temporary collector profile cleanup: %v", cleanupErr)
				}
			}
		}
		return page, err
	}
	operation := r.Action == "select" || r.Action == "expand" || r.Action == "collapse"
	check := r
	if operation {
		check.Action = "validate"
	}
	reader := withNativeContentProbe(runUIAReader, readOwnedContentStatus)
	page, err := readNativeRegions(ctx, check, reader, 750*time.Millisecond)
	if err != nil || page.Error != "" || page.ResetVerified {
		return page, err
	}
	raw, err := os.ReadFile(filepath.Join(ownedDOMProfile(r), "DevToolsActivePort"))
	if err != nil {
		return page, fmt.Errorf("后台网页配置接口未启动：%w", err)
	}
	port := strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
	bridgeCtx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	c, err := ownedDOMTarget(bridgeCtx, port, r.URL, r.Family)
	if err != nil {
		return page, err
	}
	defer c.ws.Close()
	if r.Action == "select" || r.Action == "ordinary" || r.Action == "custom" {
		r.ConfirmAction = r.Action
	}
	confirmed := false
	confirm := func(readCtx context.Context) error {
		if r.ConfirmAction == "" {
			return nil
		}
		confirmation := r
		confirmation.Action = r.ConfirmAction
		clicked, confirmErr := ownedDOMConfirmation(readCtx, c, confirmation)
		if confirmErr != nil {
			return confirmErr
		}
		if clicked {
			confirmed = true
			return pauseContext(readCtx, 800*time.Millisecond)
		}
		return nil
	}
	if operation {
		if err = ownedDOMOperation(bridgeCtx, c, r); err != nil {
			return page, err
		}
		if err = pauseContext(ctx, 800*time.Millisecond); err != nil {
			return page, err
		}
		check.Action = "snapshot"
	}
	if err = confirm(bridgeCtx); err != nil {
		return page, err
	}
	if operation || confirmed {
		check.Action, check.Handle = "snapshot", page.Handle
		page, err = readNativeRegions(ctx, check, reader, 750*time.Millisecond)
		if err != nil || page.Error != "" || page.ResetVerified {
			return page, err
		}
	}
	waiting := false
	lastReason := ""
	page, err = waitOwnedDOM(bridgeCtx, page, func(readCtx context.Context, current nativePage) (nativePage, error) {
		if confirmErr := confirm(readCtx); confirmErr != nil {
			return current, confirmErr
		}
		next, readErr := enrichOwnedDOM(readCtx, c, current, r)
		if pending, ok := readErr.(*ownedDOMPending); ok && pending.Reason != lastReason {
			waiting = true
			lastReason = pending.Reason
			log.Printf("owned DOM awaiting current quote: %v; scoped price evidence=%s", readErr, pending.Diagnostic)
		}
		return next, readErr
	}, func(readCtx context.Context, current nativePage) (nativePage, error) {
		recheck := r
		recheck.Action, recheck.Handle = "snapshot", current.Handle
		next, readErr := readNativeRegions(readCtx, recheck, reader, 750*time.Millisecond)
		if readErr == nil {
			readErr = normalPageError(next)
		}
		return next, readErr
	}, 750*time.Millisecond)
	if waiting {
		if err != nil {
			log.Printf("owned DOM quote wait ended: %v", err)
		} else {
			log.Printf("owned DOM current quote ready; continuing configuration verification")
		}
	}
	return page, err
}
