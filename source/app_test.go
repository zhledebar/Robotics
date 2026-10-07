package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	a, e := newApp(t.TempDir(), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.shutdown)
	// Ownership/view regressions use a read-only collector. Core traversal has
	// dedicated stateful tests that explicitly enable it.
	a.normal.scanCore = false
	return a
}
func product(id string) *Product {
	return &Product{ID: id, Name: "Test " + id, URL: "https://www.dell.com/en-us/shop/test/a_fixed_1", IntervalMin: 10, CooldownMin: 0, Active: true, AlertOnPriceDrop: true, AlertOnRestock: true, revision: 1}
}
func waitChecked(t *testing.T, a *App, id string) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		a.mu.RLock()
		p := a.findLocked(id)
		done := p == nil || !p.Checking
		a.mu.RUnlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("check did not finish")
}
func TestFailureKeepsTrustedData(t *testing.T) {
	a := testApp(t)
	p := product("a")
	a.store.Products = append(a.store.Products, p)
	a.mu.Lock()
	a.applyLocked(p, Observation{Price: 999, Stock: stockIn, Parser: "fixture"})
	a.mu.Unlock()
	a.fetch = func(context.Context, Product, func(string)) (Observation, error) {
		return Observation{}, errors.New("HTTP 502")
	}
	a.schedule(p.ID)
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if p.LastPrice != 999 || p.LastStock != stockIn || len(p.History) != 1 || !p.Stale || p.LastError == "" {
		t.Fatalf("failure overwrote trusted data: %+v", p)
	}
}
func TestMeaningfulHistoryOnlyAndANDThreshold(t *testing.T) {
	a := testApp(t)
	p := product("a")
	p.TargetPrice = 1000
	p.MinDiscount = 30
	o := Observation{Price: 900, Original: 1000, Discount: 10, Stock: stockIn, Parser: "one"}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applyLocked(p, o)
	o.Parser = "different transport"
	a.applyLocked(p, o)
	if len(p.History) != 1 || len(a.alerts) != 0 {
		t.Error("duplicate history or OR threshold alert")
	}
	o.Discount = 35
	o.Original = 1384.62
	a.applyLocked(p, o)
	if len(a.alerts) != 1 {
		t.Error("AND threshold did not trigger")
	}
	o.Stock = stockUnknown
	o.Price = 800
	a.applyLocked(p, o)
	if len(a.alerts) != 1 {
		t.Error("unknown stock triggered an alert")
	}
}
func TestRestockAndSameConfigurationPrice(t *testing.T) {
	a := testApp(t)
	p := product("a")
	p.DellFamilyScan = true
	o := Observation{Price: 1000, Stock: stockIn, Results: []DellResult{{OfferID: "one", Price: 1000, Stock: stockIn, Confirmed: true}, {OfferID: "two", Price: 700, Stock: stockOut, Confirmed: true}}}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applyLocked(p, o)
	o.Results = o.Results[:1]
	o.Results[0] = DellResult{OfferID: "three", Price: 600, Stock: stockIn, Confirmed: true}
	a.applyLocked(p, o)
	if len(a.alerts) != 0 {
		t.Error("another cheaper configuration is not a price drop")
	}
	o.Results[0].Stock = stockOut
	a.applyLocked(p, o)
	o.Results[0].Stock = stockIn
	a.applyLocked(p, o)
	if len(a.alerts) != 1 {
		t.Error("same configuration restock not alerted")
	}
}
func TestNonBlockingAPIAndConcurrency(t *testing.T) {
	a := testApp(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		a.store.Products = append(a.store.Products, product(id))
	}
	release := make(chan struct{})
	var active, max atomic.Int32
	a.fetch = func(ctx context.Context, p Product, f func(string)) (Observation, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			m := max.Load()
			if n <= m || max.CompareAndSwap(m, n) {
				break
			}
		}
		select {
		case <-release:
			return Observation{Price: 500, Stock: stockIn}, nil
		case <-ctx.Done():
			return Observation{}, ctx.Err()
		}
	}
	for _, p := range a.store.Products {
		a.schedule(p.ID)
		if a.schedule(p.ID) {
			t.Fatal("duplicate scheduled")
		}
	}
	time.Sleep(25 * time.Millisecond)
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	start := time.Now()
	r, e := http.Get(srv.URL + "/api/products")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if time.Since(start) > time.Second {
		t.Fatal("API blocked by scan")
	}
	close(release)
	for _, p := range a.store.Products {
		waitChecked(t, a, p.ID)
	}
	if max.Load() > 2 {
		t.Fatalf("parallel=%d", max.Load())
	}
}
func TestEditedProductDiscardsLateResult(t *testing.T) {
	a := testApp(t)
	p := product("a")
	a.store.Products = append(a.store.Products, p)
	release := make(chan struct{})
	a.fetch = func(context.Context, Product, func(string)) (Observation, error) {
		<-release
		return Observation{Price: 123, Stock: stockIn}, nil
	}
	a.schedule("a")
	a.mu.Lock()
	p.revision++
	p.URL = "https://www.dell.com/test/b_fixed_2"
	p.Checking = false
	a.mu.Unlock()
	close(release)
	time.Sleep(30 * time.Millisecond)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if p.LastPrice != 0 {
		t.Error("late observation applied after editing")
	}
}
func TestLegacyDataBackupAndUnknownFields(t *testing.T) {
	d := t.TempDir()
	b := []byte(`{"products":[{"id":"legacy","name":"keep","url":"https://www.dell.com/test/a_fixed_1","active":false,"interval_min":10,"cooldown_min":0,"future_setting":{"x":1},"history":[{"price":999,"checked_at":"2026-09-01 01:00:00"}]}],"settings":{"global_paused":true},"external_metadata":{"keep":true}}`)
	if e := os.WriteFile(filepath.Join(d, "monitor_data.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	a, e := newApp(d, true)
	if e != nil {
		t.Fatal(e)
	}
	defer a.shutdown()
	if e = a.persist(); e != nil {
		t.Fatal(e)
	}
	written, _ := os.ReadFile(a.file)
	var v map[string]json.RawMessage
	json.Unmarshal(written, &v)
	if v["external_metadata"] == nil || !bytes.Contains(written, []byte("future_setting")) {
		t.Error("unknown fields lost")
	}
	p := a.store.Products[0]
	if p.Active || !a.store.Settings.GlobalPaused || p.CooldownMin != 0 || len(p.History) != 1 {
		t.Error("legacy settings lost")
	}
	backups, _ := filepath.Glob(filepath.Join(d, "monitor_data_before_v810_*.json"))
	if len(backups) != 1 {
		t.Error("missing backup")
	}
}
func TestMutationMethodsAndOrigin(t *testing.T) {
	a := testApp(t)
	h := a.handler()
	for _, tc := range []struct {
		method, path, origin string
		code                 int
	}{{"GET", "/api/checkall", "", 405}, {"POST", "/api/checkall", "https://malicious.example", 403}} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:38840"+tc.path, nil)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Errorf("status=%d want=%d", w.Code, tc.code)
		}
	}
}
