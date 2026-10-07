package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

const observedP14sFamily = "https://www.lenovo.com/us/vipmembers/perksoffer/en/p/laptops/thinkpad/thinkpadp/thinkpad-p14s-gen-6-14-inch-amd-mobile-workstation/len101t0118#models"
const observedP14sSKU = "https://www.lenovo.com/us/vipmembers/perksoffer/en/p/laptops/thinkpad/thinkpadp/thinkpad-p14s-gen-6-14-inch-amd-mobile-workstation/21ql0021us"

func TestObservedP14sDirectSKU(t *testing.T) {
	b, err := os.ReadFile("testdata/lenovo_vip_p14s_sku_rendered.html")
	if err != nil {
		t.Fatal(err)
	}
	o, err := parseLenovo(string(b), observedP14sSKU)
	if err != nil || o.Price != 2259 || o.Stock != stockIn || o.Discount != 0 || len(o.Results) != 1 {
		t.Fatalf("direct SKU purchase component not parsed: %+v %v", o, err)
	}
	c := o.Results[0]
	if c.OfferID != "21QL0021US" || !c.Confirmed || !strings.Contains(c.CPU, "Ryzen") || !strings.Contains(c.Memory, "16 GB") || !strings.Contains(c.Storage, "512 GB") || !strings.Contains(c.Display, "WUXGA") {
		t.Fatalf("SKU price/specs not bound: %+v", c)
	}
	for name, data := range map[string]string{
		"wrong displayed SKU":  strings.ReplaceAll(string(b), ": 21QL0021US", ": 21QL9999US"),
		"missing member label": strings.ReplaceAll(string(b), "Welcome Perks at Work Members", "Welcome"),
		"other CTA wrapper":    strings.ReplaceAll(string(b), "pc-cta-wrapper_21QL0021US", "pc-cta-wrapper_21QL9999US"),
		"missing own price":    strings.ReplaceAll(string(b), "single_pdp_price_container", "price_component_pending"),
		"disabled CTA":         strings.ReplaceAll(string(b), `class="button-primary blue style-auto-gaming cta-button product_cta_button mtm"`, `disabled class="button-primary blue style-auto-gaming cta-button product_cta_button mtm"`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLenovo(data, observedP14sSKU); err == nil {
				t.Fatal("unverified own SKU quote became a current member offer")
			}
		})
	}
	a := testApp(t)
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != observedP14sSKU {
			t.Errorf("monitor URL changed: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(bytes.NewReader(b))}, nil
	})
	p := product("P14s-member-sku")
	p.URL = observedP14sSKU
	p.TargetPrice = 2600
	p.MinDiscount = 29
	a.store.Products = append(a.store.Products, p)
	a.schedule(p.ID)
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	if p.LastPrice != 2259 || p.LastStock != stockIn || p.NeedsAction != "" || len(p.LenovoResults) != 1 || len(a.alerts) != 0 || p.LenovoResults[0].Matched {
		t.Fatalf("no displayed discount was invented to meet the 29%% condition: %+v", p)
	}
	a.mu.RUnlock()
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:38840/api/model-results?id="+p.ID, nil))
	var rows []DellResult
	if err := json.Unmarshal(w.Body.Bytes(), &rows); w.Code != 200 || err != nil || len(rows) != 1 || rows[0].OfferID != "21QL0021US" {
		t.Fatalf("direct SKU absent from model details: %d %s", w.Code, w.Body.String())
	}
	t.Log("actual direct SKU captured from verified member URL: purchase price, primary CTA, CPU/memory/storage/display, AND threshold and model-results API passed")
}

func TestObservedP14sUnlabelledMemberPrice(t *testing.T) {
	b, err := os.ReadFile("testdata/lenovo_vip_p14s_family_rendered.html")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("Exclusive Price:")) {
		t.Fatal("observed P14s card unexpectedly contains a price label")
	}
	o, err := parseLenovo(string(b), observedP14sFamily)
	if err != nil || o.Price != 2259 || o.Stock != stockIn || o.Original != 0 || o.Discount != 0 || len(o.Results) != 1 || !o.Results[0].Confirmed || o.Results[0].OfferID != "21QL0021US" || o.Results[0].Source != "VIP 商店现价" {
		t.Fatalf("observed fixed member price not accepted safely: %+v %v", o, err)
	}
	// These synthetic URLs test SKU binding; they are never navigated to.
	target := strings.Replace(observedP14sFamily, "len101t0118#models", "21QL0021US", 1)
	if _, err := parseLenovo(string(b), target); err != nil {
		t.Fatal("correct SKU did not retain its own storefront price:", err)
	}
	wrong := strings.Replace(target, "21QL0021US", "21QL9999US", 1)
	if _, err := parseLenovo(string(b), wrong); err == nil {
		t.Fatal("another SKU's member price was accepted")
	}
	for name, data := range map[string]string{
		"no member label":         strings.ReplaceAll(string(b), "Welcome Perks at Work Members", "Welcome"),
		"public store title":      strings.ReplaceAll(string(b), "Lenovo USAffinity Store", "Lenovo US"),
		"disabled primary button": strings.ReplaceAll(string(b), `class="button-primary blue style-auto-gaming cta-button product_cta_button mtm"`, `disabled class="button-primary blue style-auto-gaming cta-button product_cta_button mtm"`),
		"CTO starting price":      strings.ReplaceAll(string(b), "Add To Cart", "Build Your PC"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLenovo(data, observedP14sFamily); err == nil {
				t.Fatal("unverified member offer became trusted")
			}
		})
	}
	t.Log("actual P14s member-store card: fixed SKU 21QL0021US, $2259, active primary purchase button, no exclusive-discount label")
}

func TestPartialBlockedScanPausesAndManualRecovery(t *testing.T) {
	a := testApp(t)
	p := product("partial-xps")
	p.DellFamilyScan = true
	p.TargetPrice = 2500
	for i := 0; i < 40; i++ {
		p.DellResults = append(p.DellResults, DellResult{OfferID: fmt.Sprintf("old-%d", i), Price: 2000, Confirmed: true, Matched: true, Stock: stockIn})
	}
	a.store.Products = append(a.store.Products, p)
	a.mu.Lock()
	a.applyLocked(p, Observation{Price: 2399.99, Stock: stockIn, Partial: true, Note: "全量配置未完成：网页访问被限制（access denied）", Results: []DellResult{{OfferID: "current", Price: 2399.99, Stock: stockIn, Confirmed: true}}})
	if !p.ScanIncomplete || p.NeedsAction != "等待网页验证；自动检查已暂停" || p.Stale || p.LastPrice != 2399.99 || len(p.DellResults) != 41 || len(a.alerts) != 0 {
		t.Fatalf("blocked partial quote presented as full running scan: %+v", p)
	}
	for _, c := range p.DellResults[1:] {
		if !c.Stale || c.Matched {
			t.Fatal("unvisited configuration stayed fresh or eligible for alerts")
		}
	}
	p.nextCheck = time.Now().Add(-time.Minute)
	a.mu.Unlock()
	var fetches atomic.Int32
	a.fetch = func(context.Context, Product, func(string)) (Observation, error) {
		fetches.Add(1)
		return Observation{Price: 2350, Stock: stockIn, Results: []DellResult{{OfferID: "current", Price: 2350, Stock: stockIn, Confirmed: true}}}, nil
	}
	a.start()
	time.Sleep(2300 * time.Millisecond)
	if fetches.Load() != 0 {
		t.Fatal("automatic scheduler retried a blocked scan")
	}
	if !a.schedule(p.ID) {
		t.Fatal("manual check could not resume after verification")
	}
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if p.ScanIncomplete || p.NeedsAction != "" || p.LastError != "" || p.LastPrice != 2350 || len(p.DellResults) != 1 || fetches.Load() != 1 || len(a.alerts) != 1 {
		t.Fatalf("completed manual scan did not clear pause and old results: %+v fetches=%d", p, fetches.Load())
	}
}

func TestPartialTransientFailureCanRetry(t *testing.T) {
	a := testApp(t)
	p := product("partial-network")
	p.DellFamilyScan = true
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applyLocked(p, Observation{Price: 2399.99, Stock: stockIn, Partial: true, Note: "全量配置未完成：连接超时", Results: []DellResult{{OfferID: "current", Price: 2399.99, Stock: stockIn, Confirmed: true}}})
	if !p.ScanIncomplete || p.NeedsAction != "" || len(a.alerts) != 0 {
		t.Fatal("transient network failure was turned into a verification pause")
	}
	a.applyLocked(p, Observation{Price: 2399.99, Stock: stockIn, Partial: true, Note: "采集浏览器启动失败：系统策略禁止", Results: []DellResult{{OfferID: "current", Price: 2399.99, Stock: stockIn, Confirmed: true}}})
	if p.NeedsAction != "采集浏览器不可用；自动检查已暂停" {
		t.Fatal("partial browser failure remained scheduled")
	}
}

func TestSavedV76PartialBlockIsMigratedBeforeScheduling(t *testing.T) {
	dir := t.TempDir()
	p := product("saved-xps")
	p.LastPrice = 2399.99
	p.LastError = "本轮扫描未完整完成；全量配置未完成：网页访问被限制（access denied）"
	p.NeedsAction = ""
	b, _ := json.Marshal(Store{Products: []*Product{p}, Schema: 76})
	if err := os.WriteFile(filepath.Join(dir, "monitor_data.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.shutdown()
	q := a.store.Products[0]
	if !q.ScanIncomplete || q.NeedsAction != "等待网页验证；自动检查已暂停" || q.LastPrice != p.LastPrice || q.URL != p.URL {
		t.Fatalf("old blocked state was not migrated or user URL was changed: %+v", q)
	}
}

func TestHTTPMemberRedirectCannotUsePublicPrice(t *testing.T) {
	a := testApp(t)
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		public := r.Clone(r.Context())
		public.URL, _ = url.Parse(strings.ReplaceAll(observedP14sFamily, "/vipmembers/perksoffer/en/", "/en/"))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: public, Body: io.NopCloser(strings.NewReader(`<div class="final-price">$999</div>`))}, nil
	})
	if _, err := a.getPage(context.Background(), observedP14sFamily); err == nil || !strings.Contains(err.Error(), "跳转到非会员") {
		t.Fatal("HTTP member request silently used the public destination:", err)
	}
}

func TestBrowserMemberRedirectStopsBeforePriceParsing(t *testing.T) {
	var evaluations atomic.Int32
	srv := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		for {
			var req struct {
				ID     int            `json:"id"`
				Params map[string]any `json:"params"`
			}
			if websocket.JSON.Receive(ws, &req) != nil {
				return
			}
			evaluations.Add(1)
			value := `<html><title>Lenovo US</title><div class="final-price">$999</div></html>`
			if req.Params["expression"] == "location.href" {
				value = "https://www.lenovo.com/us/en/p/test/21QL0021US"
			}
			if websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": value}}}) != nil {
				return
			}
		}
	}))
	defer srv.Close()
	ws, err := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), "", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = waitBrowserPage(ctx, &cdpClient{ws: ws}, observedP14sFamily)
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "跳转到非会员") || evaluations.Load() != 2 {
		t.Fatalf("browser redirect kept reading public prices: evaluations=%d err=%v", evaluations.Load(), err)
	}
}
