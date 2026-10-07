package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func availableCustom(id string) DellResult {
	return DellResult{OfferID: id, URL: liveXPSURL, Custom: true, Confirmed: true, Price: 6000, Original: 10000, Discount: 40, DiscountConfirmed: true, Stock: stockIn}
}

func TestDellCustomAvailabilityDisplayAndThreshold(t *testing.T) {
	p := product("availability")
	p.URL = liveXPSURL
	p.MinDiscount = 30
	p.TargetPrice = 1 // Custom discount rules still ignore total-price limits.
	for _, tc := range []struct {
		name             string
		change           func(*DellResult)
		visible, matched bool
	}{
		{"in stock", func(*DellResult) {}, true, true},
		{"out of stock", func(r *DellResult) { r.Stock = stockOut }, false, false},
		{"unknown stock", func(r *DellResult) { r.Stock = stockUnknown }, false, false},
		{"empty stock", func(r *DellResult) { r.Stock = "" }, false, false},
		{"unconfirmed selection", func(r *DellResult) { r.Confirmed = false }, false, false},
		{"stale", func(r *DellResult) { r.Stale = true }, false, false},
		{"no quote", func(r *DellResult) { r.Price = 0 }, false, false},
		{"unpublished discount", func(r *DellResult) { r.DiscountConfirmed = false }, true, false},
		{"below threshold", func(r *DellResult) { r.Discount = 20 }, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := availableCustom(tc.name)
			tc.change(&r)
			if got := len(visibleModelResults(p, []DellResult{r})) == 1; got != tc.visible {
				t.Fatalf("visible=%v want=%v: %+v", got, tc.visible, r)
			}
			if got := matches(p, r); got != tc.matched {
				t.Fatalf("matched=%v want=%v: %+v", got, tc.matched, r)
			}
		})
	}
	p.Stale = true
	if len(visibleModelResults(p, []DellResult{availableCustom("old")})) != 0 {
		t.Fatal("previous successful stock shown as currently available after failed scan")
	}
}

func TestDellCustomAvailabilityAllResultAPIsPreserveInternalRecords(t *testing.T) {
	a := testApp(t)
	p := product("availability-api")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	in, out, unknown, old := availableCustom("in"), availableCustom("out"), availableCustom("unknown"), availableCustom("old")
	out.Stock, unknown.Stock, unknown.Confirmed, old.Stale = stockOut, stockUnknown, false, true
	fixed := DellResult{OfferID: "fixed-out", Stock: stockOut, Price: 300, Confirmed: true}
	p.DellResults = []DellResult{in, out, unknown, old, fixed}
	p.LastPrice, p.LastDiscount, p.LastStock = in.Price, in.Discount, in.Stock
	a.store.Products = append(a.store.Products, p)
	before, _ := json.Marshal(p)
	for _, route := range []string{"model-results?id=" + p.ID, "dell-results?id=" + p.ID, "products"} {
		t.Run(route, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.handler().ServeHTTP(w, httptest.NewRequest("GET", "http://localhost/api/"+route, nil))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var rows []DellResult
			if route == "products" {
				var products []Product
				if err := json.Unmarshal(w.Body.Bytes(), &products); err != nil {
					t.Fatal(err)
				}
				rows = products[0].DellResults
				if products[0].LastPrice != fixed.Price || products[0].LastDiscount != fixed.Discount || products[0].LastStock != fixed.Stock {
					t.Fatal("custom quote leaked into dashboard's preconfigured summary")
				}
			} else if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, row := range rows {
				ids = append(ids, row.OfferID)
			}
			if !reflect.DeepEqual(ids, []string{"in", "fixed-out"}) {
				t.Fatalf("displayed IDs=%v", ids)
			}
		})
	}
	after, _ := json.Marshal(p)
	if string(before) != string(after) {
		t.Fatal("display mutated internal history or unavailable records")
	}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newApp(strings.TrimSuffix(a.file, "/monitor_data.json"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.shutdown()
	if len(restarted.store.Products[0].DellResults) != 5 || len(visibleModelResults(restarted.store.Products[0], restarted.store.Products[0].DellResults)) != 1 {
		t.Fatal("restart lost internal records or exposed unavailable configurations")
	}
}

func TestDellUnavailableCustomDoesNotAlertForDiscountIncrease(t *testing.T) {
	for _, state := range []string{stockOut, stockUnknown, "unconfirmed"} {
		t.Run(state, func(t *testing.T) {
			a := testApp(t)
			p := product("alert")
			p.URL, p.MinDiscount, p.AlertOnDiscountIncrease = liveXPSURL, 30, true
			r := availableCustom("same")
			a.applyLocked(p, Observation{Results: []DellResult{r}})
			if len(a.alerts) != 1 {
				t.Fatal("available threshold suppressed")
			}
			r.Discount = 60
			if state == "unconfirmed" {
				r.Confirmed = false
			} else {
				r.Stock = state
			}
			a.applyLocked(p, Observation{Results: []DellResult{r}})
			if len(a.alerts) != 1 || p.DellResults[0].Matched {
				t.Fatal("unavailable custom discount triggered an alert")
			}
			p.MinDiscount = 0
			r.Stock, r.Confirmed, r.Discount = stockIn, true, 70
			a.applyLocked(p, Observation{Results: []DellResult{r}})
			if len(a.alerts) != 1 {
				t.Fatal("unknown or unavailable prior quote used for discount-increase/restock/price alert")
			}
			r.Discount = 75
			a.applyLocked(p, Observation{Results: []DellResult{r}})
			if len(a.alerts) != 2 || !strings.Contains(a.alerts[1].Message, "折扣提高") {
				t.Fatal("available same-configuration discount increase suppressed")
			}
		})
	}
}

func TestDellPartialScanHidesUncoveredCustomAndCanRestoreAvailableRow(t *testing.T) {
	a := testApp(t)
	p := product("partial")
	p.URL = liveXPSURL
	first, missing := availableCustom("first"), availableCustom("missing")
	a.applyLocked(p, Observation{Results: []DellResult{first, missing}})
	a.applyLocked(p, Observation{Partial: true, VerifiedNative: true, Results: []DellResult{first}})
	if len(p.DellResults) != 2 || !p.DellResults[1].Stale || len(visibleModelResults(p, p.DellResults)) != 1 || !p.ScanIncomplete {
		t.Fatal("uncovered custom displayed as available or internal partial evidence discarded")
	}
	a.applyLocked(p, Observation{Results: []DellResult{first, missing}})
	if len(visibleModelResults(p, p.DellResults)) != 2 || p.ScanIncomplete {
		t.Fatal("verified stocked configuration not restored")
	}
	q := productForDisplay(p)
	if q.LastPrice != 0 || q.LastDiscount != 0 || q.LastStock != "" || p.LastPrice == 0 {
		t.Fatal("custom quote leaked into preconfigured summary or internal quote lost")
	}
}

func TestLenovoCTODisplayAndAlertsRetainOwnStockPolicy(t *testing.T) {
	p := product("lenovo")
	p.URL, p.MinDiscount = vipP1Family, 30
	r := availableCustom("cto")
	r.Stock, r.Confirmed = stockUnknown, false
	if len(visibleModelResults(p, []DellResult{r})) != 1 || !matches(p, r) {
		t.Fatal("Dell availability gate changed Lenovo CTO policy")
	}
}
