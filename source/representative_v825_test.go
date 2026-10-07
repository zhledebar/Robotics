package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRepresentativeScanRecordsCoupledActualStateAndBoundsSelections(t *testing.T) {
	m := &rejectedRAMModel{cpu: "CPU C", ram: "32GB", ssd: "512GB", screen: "LED", baseline: true, mutateCPU: true}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.selectionStallBudget = time.Millisecond
	selects := 0
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "select" {
			selects++
		}
		return m.call(ctx, r)
	}
	// Start at the 64GB target. Accepting it changes the CPU and RAM in this
	// fixture; record what Dell actually selected instead of restoring CPU C.
	rows, _, cursor, err := b.scanRepresentativeCore(context.Background(), m.page(), nativeRequest{Handle: 99, URL: liveXPSURL, Family: true}, 7, func(string) {})
	if selects > representativeSelectionLimit || cursor <= 7 || len(rows) > representativeSelectionLimit+1 {
		t.Fatalf("unbounded scan: selects=%d rows=%d cursor=%d err=%v", selects, len(rows), cursor, err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.OfferID] {
			t.Fatal("duplicate actual state")
		}
		seen[row.OfferID] = true
		cpu := nativeOptionKey(row.Options["moduleCPU"])
		ram := nativeOptionKey(row.Options["moduleRAM"])
		if cpu >= "CPU C" && ram == "16GB" {
			t.Fatal("rejected RAM falsely accepted")
		}
		if ram == "64GB" {
			t.Fatal("requested RAM mislabeled after coupled CPU reset")
		}
	}
}

func TestRepresentativeCursorChangesNextPassTargets(t *testing.T) {
	var first []string
	for pass := 0; pass < 2; pass++ {
		m := &rejectedRAMModel{cpu: "CPU A", ram: "16GB", ssd: "512GB", screen: "LED", baseline: true}
		b := newNormalBrowser()
		b.pollDelay = time.Millisecond
		b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
			if r.Action == "select" && len(first) == pass {
				first = append(first, r.GroupID+"|"+nativeOptionKey(r.OptionName))
			}
			return m.call(ctx, r)
		}
		_, _, _, _ = b.scanRepresentativeCore(context.Background(), m.page(), nativeRequest{Handle: 99, URL: liveXPSURL, Family: true}, pass*6, func(string) {})
	}
	if len(first) != 2 || first[0] == first[1] {
		t.Fatalf("same targets repeated: %v", first)
	}
}

func TestAllQuotesCommitBeforeSlowCorePassStarts(t *testing.T) {
	a := testApp(t)
	a.normal.available = true
	a.normal.scanCore = true
	xps := product("xps")
	xps.URL = liveXPSURL
	xps.DellFamilyScan = true
	pro := product("pro")
	pro.URL = "https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250/pc16250_fixed_22"
	pro.DellFamilyScan = true
	a.store.Products = append(a.store.Products, xps, pro)
	started := make(chan string, 2)
	releaseQuotes := make(chan struct{})
	releaseCore := make(chan struct{})
	coreStarted := make(chan string, 2)
	defer close(releaseCore)
	var mu sync.Mutex
	var sequence []string
	a.fetch = func(ctx context.Context, p Product, progress func(string)) (Observation, error) {
		phase := "quote"
		if p.corePhase {
			phase = "core"
		}
		mu.Lock()
		sequence = append(sequence, p.ID+":"+phase)
		mu.Unlock()
		if p.quoteOnly {
			started <- p.ID
			select {
			case <-releaseQuotes:
			case <-ctx.Done():
				return Observation{}, ctx.Err()
			}
		}
		if p.corePhase {
			a.mu.RLock()
			quotesReady := xps.LastSuccess != "" && pro.LastSuccess != "" && pro.LastPrice == 1500
			a.mu.RUnlock()
			if !quotesReady {
				t.Error("core monopolized collector before quotes were committed")
			}
			coreStarted <- p.ID
			select {
			case <-releaseCore:
			case <-ctx.Done():
				return Observation{}, ctx.Err()
			}
		}
		price := 2500.0
		if p.ID == "pro" {
			price = 1500
		}
		return Observation{Price: price, Stock: stockIn, ScanScope: phase, Results: []DellResult{{OfferID: p.ID + "-fixed", Price: price, Confirmed: true, Stock: stockIn}}}, nil
	}
	if !a.schedule(xps.ID) || !a.schedule(pro.ID) {
		t.Fatal("quotes not scheduled")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("quote phase never started")
		}
	}
	close(releaseQuotes)
	select {
	case <-coreStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("core phase never started")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sequence) < 3 || sequence[0][len(sequence[0])-5:] != "quote" || sequence[1][len(sequence[1])-5:] != "quote" {
		t.Fatalf("unexpected phase order: %v", sequence)
	}
}
