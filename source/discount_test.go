package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func liveCTOFixture(t *testing.T) string {
	t.Helper()
	b, e := os.ReadFile("testdata/lenovo_vip_p1_cto_rendered.html")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestLiveCTOHasNoWholeDeviceDiscount(t *testing.T) {
	body := liveCTOFixture(t)
	o, e := parseLenovo(body, p1CTOURL)
	if e != nil || len(o.Results) != 1 {
		t.Fatalf("actual CTO parsing: %+v %v", o, e)
	}
	row := o.Results[0]
	if o.Price != 3514 || row.DiscountConfirmed || o.Discount != 0 || o.Original != 0 || row.Confirmed || row.Stock != stockUnknown || !row.Custom || !strings.Contains(row.Memory, "16 GB") || !strings.Contains(row.Storage, "512 GB") {
		t.Fatalf("borrowed option discount or stock: %+v", o)
	}
	wrong := strings.Replace(p1CTOURL, "21UECTO1WWUS1", "21ZZCTO1WWUS1", 1)
	if _, e = parseLenovo(body, wrong); e == nil {
		t.Fatal("wrong configurator SKU accepted")
	}
	noMember := strings.ReplaceAll(body, "Welcome Perks at Work Members", "Welcome Guests")
	if _, e = parseLenovo(noMember, p1CTOURL); e == nil {
		t.Fatal("unverified member store accepted")
	}
	hidden := strings.Replace(body, `class="headerPrice"`, `class="headerPrice" style="display:none"`, 1)
	if _, e = parseLenovo(hidden, p1CTOURL); e == nil {
		t.Fatal("hidden whole-device quote accepted")
	}
}

func TestCTODiscountBoundToCurrentConfiguration(t *testing.T) {
	body := liveCTOFixture(t)
	body = strings.Replace(body, `<div class="headerPrice"`, `<div class="headerPrice"`, 1)
	discounted := strings.Replace(body, `<div class="formatPrice"`, `<del class="web-price">$7,028.00</del><div class="formatPrice"`, 1)
	o, e := parseLenovo(discounted, p1CTOURL)
	if e != nil || o.Discount != 50 || o.Original != 7028 || !o.Results[0].DiscountConfirmed {
		t.Fatalf("same-device original: %+v %v", o, e)
	}
	exact := strings.Replace(body, `<div class="formatPrice"`, `<span>40% off</span><div class="formatPrice"`, 1)
	q, e := parseLenovo(exact, p1CTOURL)
	if e != nil || q.Discount != 40 || q.Original != 0 || !q.Results[0].DiscountConfirmed {
		t.Fatalf("explicit same-device discount: %+v %v", q, e)
	}
	zero := strings.Replace(body, `<div class="formatPrice"`, `<del>$3,514.00</del><div class="formatPrice"`, 1)
	z, e := parseLenovo(zero, p1CTOURL)
	if e != nil || z.Discount != 0 || !z.Results[0].DiscountConfirmed {
		t.Fatal("verified zero discount confused with missing discount")
	}
	changed := strings.Replace(body, `value="16GB_LP5X_8533_CAMM2"`, `value="32GB_LP5X_8533_CAMM2"`, 1)
	r, e := parseLenovo(changed, p1CTOURL)
	if e != nil || r.Results[0].OfferID == o.Results[0].OfferID {
		t.Fatal("different selected configuration reused identity")
	}
}

func TestCustomDiscountAlertsDoNotRequireStockOrPriceLimit(t *testing.T) {
	a := testApp(t)
	p := product("discount")
	p.URL = vipP1Family
	p.CustomDiscountOnly = true
	p.TargetPrice = 1000
	p.MinDiscount = 40
	p.AlertOnDiscountIncrease = true
	custom := DellResult{OfferID: "CTO-one", Price: 6000, Original: 10000, Discount: 40, Stock: stockUnknown, Custom: true, DiscountConfirmed: true}
	fixed := DellResult{OfferID: "fixed", Price: 500, Original: 2000, Discount: 75, Stock: stockIn, Confirmed: true}
	a.applyLocked(p, Observation{Results: []DellResult{fixed, custom}})
	if p.LastPrice != 500 || p.LastDiscount != 75 || len(a.alerts) != 1 || !p.LenovoResults[1].Matched || !p.LenovoResults[0].Matched {
		t.Fatalf("custom discount mode ignored: %+v alerts=%+v", p, a.alerts)
	}
	if strings.Contains(a.alerts[0].Message, "补货") || strings.Contains(a.alerts[0].Message, "降价") {
		t.Fatal("stock/price-only reason in discount mode")
	}
	p.MinDiscount = 0
	custom.Discount = 45
	a.applyLocked(p, Observation{Results: []DellResult{custom}})
	if len(a.alerts) != 2 || !strings.Contains(a.alerts[1].Message, "折扣提高") {
		t.Fatal("same configuration discount improvement not alerted")
	}
	custom.OfferID = "CTO-other"
	custom.Discount = 60
	a.applyLocked(p, Observation{Results: []DellResult{custom}})
	if len(a.alerts) != 2 {
		t.Fatal("different configuration mistaken for a discount increase")
	}
	custom.DiscountConfirmed = false
	a.applyLocked(p, Observation{Results: []DellResult{custom}})
	if len(a.alerts) != 2 {
		t.Fatal("unverified discount alerted")
	}
	p.CustomDiscountOnly = false
	p.MinDiscount = 30
	p.TargetPrice = 10000
	if matches(p, custom) {
		t.Fatal("ordinary stock gate removed")
	}
}

type quoteTransport func(*http.Request) (*http.Response, error)

func (f quoteTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestFamilyReadsOnlyOwnDefaultCTOAndKeepsUserURL(t *testing.T) {
	a := testApp(t)
	family := string(observedLenovoCards(t))
	cto := liveCTOFixture(t)
	calls := 0
	a.client.Transport = quoteTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		b := family
		if r.URL.String() == p1CTOURL {
			b = cto
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(b)), Request: r, Header: http.Header{}}, nil
	})
	p := product("p1")
	p.URL = vipP1Family
	p.CustomDiscountOnly = true
	p.MinDiscount = 30
	o, e := a.fetchObservation(context.Background(), *p, func(string) {})
	if e != nil || o.Partial || calls != 2 || len(o.Results) != 2 {
		t.Fatalf("family+single CTO read: %+v %v calls=%d", o, e, calls)
	}
	a.applyLocked(p, o)
	if p.URL != vipP1Family || p.LastPrice != 4779 || p.LastDiscount != 33.8 || len(a.alerts) != 1 {
		t.Fatalf("fixed quote used as custom discount: %+v", p)
	}
}

func TestCustomDiscountSettingsPersistThroughAPIAndRestart(t *testing.T) {
	a := testApp(t)
	p := product("p")
	p.URL = vipP1Family
	a.store.Products = append(a.store.Products, p)
	p.CustomDiscountOnly = true
	p.AlertOnDiscountIncrease = true
	p.MinDiscount = 45
	b, _ := json.Marshal(p)
	req := httptest.NewRequest("POST", "http://localhost/api/save", strings.NewReader(string(b)))
	req.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	restarted, e := newApp(strings.TrimSuffix(a.file, "/monitor_data.json"), true)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.shutdown()
	q := restarted.store.Products[0]
	if q.CustomDiscountOnly || !q.AlertOnDiscountIncrease || q.MinDiscount != 45 {
		t.Fatalf("lost mode on restart: %+v", q)
	}
}

func TestDellDiscountModeDoesNotEnumerateUpgrades(t *testing.T) {
	a := testApp(t)
	p := product("dell-discount")
	p.URL = "https://www.dell.com/en-us/shop/test/a_reg_1"
	p.DellFamilyScan = true
	p.CustomDiscountOnly = true
	p.MinDiscount = 40
	body := `<html><div class="hero-section"><span class="sale-price">$1000.00</span><del>$2000.00</del></div><div data-module-id="CPU" role="group" aria-label="Processor"><div data-option-id="CPU-a" data-is-selected="true" aria-label="Ultra 7">Ultra 7</div><div data-option-id="CPU-b" data-is-selected="false" aria-label="Ultra 9">Ultra 9</div></div></html>`
	calls := 0
	a.client.Transport = quoteTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Request: r, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	o, e := a.fetchObservation(context.Background(), *p, func(string) {})
	if e != nil || o.Partial || calls != 1 || len(o.Results) != 1 || !o.Results[0].Custom || !o.Results[0].DiscountConfirmed || o.Discount != 50 {
		t.Fatalf("discount mode traversed or lost actual default quote: %+v %v calls=%d", o, e, calls)
	}
	a.applyLocked(p, o)
	if len(a.alerts) != 0 || p.DellResults[0].Stock != stockUnknown || p.DellResults[0].Matched || len(visibleModelResults(p, p.DellResults)) != 0 {
		t.Fatalf("unknown-stock Dell custom configuration displayed or alerted: %+v", p)
	}
}
