package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Production Go traversal, DOM scripts, CDP mouse input and popup confirmation
// run together in Chromium. Windows UIAutomation is replaced by a documented
// local DOM adapter; this does not claim a live Dell or Windows test.
func TestChromiumCoupledRepresentativeTraversal(t *testing.T) {
	if os.Getenv("MONITOR_BROWSER_TEST") != "1" {
		t.Skip("set MONITOR_BROWSER_TEST=1 with Chromium and Python Playwright")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python", "../verification/serve_coupled_browser.py", liveXPSURL)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = stdin.Write([]byte("stop\n"))
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("fixture browser: %v %s", err, stderr.String())
		}
	}()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("browser startup: %v %s", err, stderr.String())
	}
	port := strings.TrimSpace(line)
	c, err := ownedDOMTarget(ctx, port, liveXPSURL, true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.ws.Close()
	r := nativeRequest{Handle: 99, Owner: "local-browser-fixture", URL: liveXPSURL, Family: true}
	read := func(readCtx context.Context) (nativePage, error) {
		raw, e := c.eval(readCtx, ownedDOMExpression(r, "snapshot"))
		if e != nil {
			return nativePage{}, e
		}
		var quote struct {
			Price   float64
			OfferID string
		}
		if e = json.Unmarshal([]byte(raw), &quote); e != nil {
			return nativePage{}, e
		}
		p := nativePage{Handle: 99, URL: liveXPSURL, CanOrdinary: true, Regions: map[string]*nativeNode{
			"hero-section":      {Kind: "Group", Name: fmt.Sprintf("Dell Price $%.2f Offer ID %s", quote.Price, quote.OfferID)},
			"add-to-cart-stack": {Kind: "Group", Children: []*nativeNode{{Kind: "Button", Name: "Add to Cart", Enabled: true}}},
		}}
		return applyOwnedDOMSnapshot(p, r, raw)
	}
	b := newNormalBrowser()
	b.pollDelay = 10 * time.Millisecond
	b.selectionStallBudget = time.Second
	b.call = func(callCtx context.Context, req nativeRequest) (nativePage, error) {
		if req.Action == "select" || req.Action == "expand" || req.Action == "collapse" {
			if e := ownedDOMOperation(callCtx, c, req); e != nil {
				return nativePage{}, e
			}
		}
		if req.Action == "select" {
			req.ConfirmAction = "select"
		}
		if req.ConfirmAction != "" {
			confirmation := req
			confirmation.Action = req.ConfirmAction
			if _, e := ownedDOMConfirmation(callCtx, c, confirmation); e != nil {
				return nativePage{}, e
			}
		}
		return read(callCtx)
	}
	initial, err := read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, partial, cursor, more, err := b.scanRepresentativeCore(ctx, initial, r, 0, func(string) {})
	if err != nil || partial || len(rows) != 2 || cursor == 0 || !more {
		t.Fatalf("rows=%d partial=%v cursor=%d more=%t err=%v", len(rows), partial, cursor, more, err)
	}
	raw, err := c.eval(ctx, `JSON.stringify({selections:window.selectionClicks,confirmations:window.confirmationClicks,coupled:window.coupledChanges})`)
	if err != nil {
		t.Fatal(err)
	}
	var stats struct{ Selections, Confirmations, Coupled int }
	if err = json.Unmarshal([]byte(raw), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Selections != 1 || stats.Confirmations != stats.Selections || stats.Coupled == 0 {
		t.Fatalf("popup handling failed: %s", raw)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.OfferID] || row.Price <= 0 || !row.Confirmed || row.CPU == "" || row.Memory == "" {
			t.Fatalf("invalid actual quote: %+v", row)
		}
		seen[row.OfferID] = true
	}
	t.Logf("production traversal + Chromium: actual_rows=%d selected=%d accepted_popups=%d coupled_changes=%d pending_more=%t", len(rows), stats.Selections, stats.Confirmations, stats.Coupled, more)
}
