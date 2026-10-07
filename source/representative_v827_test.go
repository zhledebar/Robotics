package main

import (
	"context"
	"errors"
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
	rows, _, cursor, more, err := b.scanRepresentativeCore(context.Background(), m.page(), nativeRequest{Handle: 99, URL: liveXPSURL, Family: true}, 7, func(string) {})
	if selects > representativeSelectionLimit || selects != 1 || cursor <= 7 || len(rows) > representativeSelectionLimit+1 || !more {
		t.Fatalf("scan failed to yield after one transition: selects=%d rows=%d cursor=%d more=%t err=%v", selects, len(rows), cursor, more, err)
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
		_, _, _, _, _ = b.scanRepresentativeCore(context.Background(), m.page(), nativeRequest{Handle: 99, URL: liveXPSURL, Family: true}, pass, func(string) {})
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
		return Observation{Price: price, Stock: stockIn, ScanScope: phase, CorePending: p.corePhase, Results: []DellResult{{OfferID: p.ID + "-fixed", Price: price, Confirmed: true, Stock: stockIn}}}, nil
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
	// Make Pro due while the first slow configuration transition is running.
	// Completion must leave the pending XPS transition queued so the scheduler
	// can run Pro's quote first.
	a.mu.Lock()
	pro.nextCheck = time.Now().Add(-time.Second)
	a.mu.Unlock()
	close(releaseCore)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.RLock()
		finished := !xps.Checking
		pending := xps.CorePending
		a.mu.RUnlock()
		if finished && pending {
			break
		}
		time.Sleep(time.Millisecond)
	}
	a.mu.RLock()
	if !xps.CorePending {
		a.mu.RUnlock()
		t.Fatal("unfinished representative work was not queued")
	}
	a.mu.RUnlock()
	if !a.schedule(pro.ID) {
		t.Fatal("due Dell Pro quote was not scheduled")
	}
	select {
	case id := <-started:
		if id != pro.ID {
			t.Fatalf("unexpected quote scheduled before next core slice: %s", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dell Pro quote did not run between core slices")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sequence) < 4 || sequence[0][len(sequence[0])-5:] != "quote" || sequence[1][len(sequence[1])-5:] != "quote" || sequence[2] != "xps:core" || sequence[3] != "pro:quote" {
		t.Fatalf("unexpected phase order: %v", sequence)
	}
}

func TestPendingCoreProductsRotateSoProIsNotStarvedByXPS(t *testing.T) {
	a := testApp(t)
	a.normal.available = true
	xps := product("xps")
	pro := product("pro")
	xps.DellFamilyScan, pro.DellFamilyScan = true, true
	xps.nextCheck, pro.nextCheck = time.Now().Add(time.Hour), time.Now().Add(time.Hour)
	a.store.Products = []*Product{xps, pro}
	a.corePending[xps.ID], a.corePending[pro.ID] = xps.revision, pro.revision
	xps.CorePending, pro.CorePending = true, true
	visited := make(chan string, 4)
	a.fetch = func(ctx context.Context, p Product, progress func(string)) (Observation, error) {
		if !p.corePhase {
			return Observation{}, errors.New("expected a representative configuration pass")
		}
		visited <- p.ID
		return Observation{Price: 1000, Stock: stockIn, ScanScope: "representative", CorePending: true,
			Results: []DellResult{{OfferID: p.ID + "-config", Price: 1000, Confirmed: true, Stock: stockIn}}}, nil
	}
	a.dispatchPendingCore()
	for _, want := range []string{xps.ID, pro.ID} {
		select {
		case got := <-visited:
			if got != want {
				t.Fatalf("core scan order=%s, want %s", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for fair core scan of %s", want)
		}
	}
}

func TestRepresentativeCoreSliceSkipsOrdinaryView(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	b := newNormalBrowser()
	b.available = false
	b.pollDelay = time.Millisecond
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "ordinary" {
			t.Error("representative slice switched to the slower ordinary view")
		}
		return m.call(ctx, r)
	}
	p := product("core-only")
	p.URL, p.DellFamilyScan, p.corePhase = liveXPSURL, true, true
	o, err := b.collect(context.Background(), *p, func(string) {})
	if err != nil || !o.CoreOnly || o.ScanScope != "representative" || len(o.Results) < 2 {
		t.Fatalf("representative slice failed: core_only=%t scope=%s rows=%d err=%v", o.CoreOnly, o.ScanScope, len(o.Results), err)
	}
}

func TestCoreOnlyApplyPreservesOrdinaryQuoteAndRows(t *testing.T) {
	a := testApp(t)
	p := product("core-only-apply")
	p.URL = liveXPSURL
	p.LastPrice, p.LastOriginal, p.LastStock, p.LastParser = 1520, 1700, stockIn, "普通报价"
	p.DellResults = []DellResult{
		{OfferID: "pc16250_fixed_22", Price: 1520, Confirmed: true, Stock: stockIn},
		{OfferID: "old-BYO-config", Custom: true, Price: 2800, Confirmed: true, Stock: stockIn},
	}
	a.applyLocked(p, Observation{CoreOnly: true, VerifiedNative: true, ScanScope: "representative", Price: 3000, Stock: stockIn,
		Results: []DellResult{{OfferID: "new-BYO-config", Custom: true, Price: 3000, Confirmed: true, Stock: stockIn}}})
	if p.LastPrice != 1520 || p.LastOriginal != 1700 || p.LastStock != stockIn || p.LastParser != "普通报价" {
		t.Fatalf("core-only pass overwrote the current offer quote: %+v", p)
	}
	var ordinary, oldCustom, newCustom *DellResult
	for i := range p.DellResults {
		r := &p.DellResults[i]
		switch r.OfferID {
		case "pc16250_fixed_22":
			ordinary = r
		case "old-BYO-config":
			oldCustom = r
		case "new-BYO-config":
			newCustom = r
		}
	}
	if ordinary == nil || ordinary.Stale || oldCustom == nil || !oldCustom.Stale || newCustom == nil || newCustom.Stale {
		t.Fatalf("core-only merge did not preserve/age the right rows: %+v", p.DellResults)
	}
}
