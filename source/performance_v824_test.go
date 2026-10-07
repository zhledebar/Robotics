package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStreamingDoesNotSuppressFinalDiscountIncreaseAlert(t *testing.T) {
	a := testApp(t)
	p := product("stream-alert")
	p.URL = liveXPSURL
	p.LastTrusted = true
	p.AlertOnDiscountIncrease = true
	p.TargetPrice = 0
	p.MinDiscount = 0
	old := DellResult{OfferID: "same-custom", Custom: true, Confirmed: true, Stock: stockIn, Price: 1000, Discount: 10, DiscountConfirmed: true}
	p.DellResults = []DellResult{old}
	a.store.Products = append(a.store.Products, p)
	newRow := old
	newRow.Discount = 20
	if !a.scheduleWith(p.ID, func(ctx context.Context, _ Product, _ func(string)) (Observation, error) {
		o := Observation{Price: 1000, Stock: stockIn, Results: []DellResult{newRow}, VerifiedNative: true}
		ctx.Value(scanObservationKey{}).(func(Observation))(o)
		return o, nil
	}) {
		t.Fatal("scan not scheduled")
	}
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(a.alerts) != 1 || !strings.Contains(a.alerts[0].Message, "定制折扣提高") {
		t.Fatalf("final alert suppressed: %+v", a.alerts)
	}
}

func TestOwnedDOMStockMustBindToCurrentOfferAndNotBorrowOldStock(t *testing.T) {
	for _, mode := range []string{"matching", "wrong-offer", "unconfirmed", "matching-lagging-uia"} {
		t.Run(mode, func(t *testing.T) {
			p, v := delayedUIAQuote(t)
			if mode != "matching-lagging-uia" {
				v["price"] = 5649.99
			}
			v["stock"], v["stockOfferID"] = stockIn, "da16260_reg_01"
			if mode == "wrong-offer" {
				v["stockOfferID"] = "da16260_fixed_71"
			}
			if mode == "unconfirmed" {
				v["stock"] = ""
			}
			raw, _ := json.Marshal(v)
			got, err := waitOwnedDOM(context.Background(), p, func(_ context.Context, current nativePage) (nativePage, error) {
				return applyOwnedDOMSnapshot(current, nativeRequest{URL: p.URL, Family: true}, string(raw))
			}, func(context.Context, nativePage) (nativePage, error) { return p, nil }, time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			want := mode == "matching" || mode == "matching-lagging-uia"
			if (got.DOMStock == stockIn) != want {
				t.Fatalf("unbound stock: %q", got.DOMStock)
			}
			if mode == "matching-lagging-uia" {
				o, err := nativeObservation(got, p.URL, true)
				if err != nil || !o.Results[0].Confirmed || o.Results[0].Stock != stockIn || o.Results[0].Original != 0 || o.Results[0].DiscountConfirmed {
					t.Fatalf("invalid fresh stock / old discount: %+v %v", o, err)
				}
			}
		})
	}
}

func TestReaderCacheRejectsUnknownChangedAndMissingAssemblies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader.dll")
	if err := os.WriteFile(path, []byte("compiled-test-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if readerAssemblyReady(path) {
		t.Fatal("unknown assembly trusted")
	}
	rememberReaderAssembly(path)
	if !readerAssemblyReady(path) {
		t.Fatal("unchanged recorded assembly rejected")
	}
	if err := os.WriteFile(path, []byte("changed-test-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if readerAssemblyReady(path) {
		t.Fatal("modified assembly trusted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if readerAssemblyReady(path) {
		t.Fatal("missing assembly trusted")
	}
}

func TestCoreTraversalPublishesFreshRowsBeforeReturning(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.call = m.call
	updates := 0
	finished := false
	ctx := context.WithValue(context.Background(), scanRowsKey{}, func(rows []DellResult) {
		if finished || len(rows) == 0 {
			t.Fatal("invalid progress")
		}
		updates++
	})
	rows, partial, err := b.scanCustomCore(ctx, m.page(), nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(string) {})
	finished = true
	if err != nil || partial || len(rows) != 7 || updates < 7 {
		t.Fatalf("rows=%d updates=%d partial=%v err=%v", len(rows), updates, partial, err)
	}
}

func TestFortyBaselineCombinationsAvoidScreenResetClicks(t *testing.T) {
	m := &rejectedRAMModel{cpu: "CPU A", ram: "16GB", ssd: "512GB", screen: "LED"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	screenClicks := 0
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "select" && r.GroupID == "moduleDISPLAY" {
			screenClicks++
		}
		return m.call(ctx, r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baselineRows := 0
	ctx = context.WithValue(ctx, scanRowsKey{}, func(rows []DellResult) { baselineRows = len(rows) })
	_, _, _ = b.scanCustomCore(ctx, m.page(), nativeRequest{URL: liveXPSURL, Family: true, Handle: 99}, func(s string) {
		if strings.Contains(s, "继续检查其他显卡及内存选项") {
			cancel()
		}
	})
	if baselineRows != 40 || screenClicks != 20 {
		t.Fatalf("baseline=%d screen-clicks=%d want=40/20", baselineRows, screenClicks)
	}
}

func TestUnchangedMemoryBranchStopsEarlyWithoutAcceptingOldSelection(t *testing.T) {
	m := &rejectedRAMModel{cpu: "CPU C", ram: "32GB", ssd: "512GB", screen: "LED"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.selectionStallBudget = time.Millisecond
	calls := 0
	b.call = func(context.Context, nativeRequest) (nativePage, error) { calls++; return m.page(), nil }
	p, err := b.stableWhen(context.Background(), m.page(), nativeRequest{Action: "select", GroupID: "moduleRAM", URL: liveXPSURL, Family: true, Handle: 99, OptionName: "16GB"}, func(p nativePage) bool {
		return nativeOptionKey(nativeOptions(p.Regions["configuration-section"])["moduleRAM"]) == "16GB"
	}, "核心选项切换未确认（内存）")
	if err == nil || calls >= 7 || nativeOptionKey(nativeOptions(p.Regions["configuration-section"])["moduleRAM"]) != "32GB" {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestScanProgressCannotOverwriteCanceledEditedOrFinishedProduct(t *testing.T) {
	for _, mode := range []string{"valid", "canceled", "edited", "finished", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t)
			p := product("progress")
			p.URL = liveXPSURL
			p.Checking = true
			p.revision = 4
			p.Stale = true
			p.DellResults = []DellResult{{OfferID: "old", Custom: true, Confirmed: true, Price: 900, Stock: stockIn}}
			a.store.Products = append(a.store.Products, p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "canceled":
				cancel()
			case "edited":
				p.revision++
			case "finished":
				p.Checking = false
			case "deleted":
				a.store.Products = nil
			}
			a.applyScanProgress(ctx, p.ID, 4, Observation{Results: []DellResult{{OfferID: "new", Custom: true, Confirmed: true, Price: 1000, Stock: stockIn}}})
			if mode == "valid" {
				if p.Stale || !p.ScanIncomplete || len(p.DellResults) != 2 || !p.DellResults[1].Stale || len(visibleModelResults(p, p.DellResults)) != 1 {
					t.Fatalf("invalid update: %+v", p)
				}
				if len(p.History) != 0 || len(a.alerts) != 0 {
					t.Fatal("progress generated history or alert")
				}
			} else if len(p.DellResults) != 1 || p.DellResults[0].OfferID != "old" {
				t.Fatal("obsolete progress accepted")
			}
		})
	}
}
