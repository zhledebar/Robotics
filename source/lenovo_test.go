package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

const vipP1Family = "https://www.lenovo.com/us/vipmembers/perksoffer/en/p/laptops/thinkpad/thinkpadp/thinkpad-p1-gen-9-16-inch-intel-mobile-workstation/len101t0180#models"

func observedLenovoCards(t *testing.T) []byte {
	t.Helper()
	dir := os.Getenv("MONITOR_FIXTURES")
	if dir == "" {
		dir = "testdata"
	}
	b, err := os.ReadFile(filepath.Join(dir, "lenovo_vip_p1_rendered.html"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestObservedLenovoVIPCards(t *testing.T) {
	b := observedLenovoCards(t)
	o, err := parseLenovo(string(b), vipP1Family)
	if err != nil || len(o.Results) != 2 || o.Price != 4779 || o.Original != 7219 || o.Stock != stockIn {
		t.Fatalf("member models: %+v err=%v", o, err)
	}
	byPart := map[string]DellResult{}
	for _, r := range o.Results {
		byPart[r.OfferID] = r
	}
	cto := byPart["21UECTO1WWUS1"]
	fixed := byPart["21UE0015US"]
	if cto.Price != 3514 || cto.Confirmed || cto.Stock != stockUnknown || cto.Original != 0 {
		t.Fatalf("CTO starting price became a confirmed member offer: %+v", cto)
	}
	if fixed.Price != 4779 || fixed.Original != 7219 || !fixed.Confirmed || fixed.Stock != stockIn || !strings.Contains(fixed.Memory, "64 GB") || !strings.Contains(fixed.Storage, "2 TB") {
		t.Fatalf("fixed model mis-bound: %+v", fixed)
	}
	if _, err = parseLenovo(string(b), strings.Replace(vipP1Family, "len101t0180#models", "21UE9999US", 1)); err == nil {
		t.Fatal("unlisted SKU borrowed another SKU's price")
	}
	single, err := parseLenovo(string(b), strings.Replace(vipP1Family, "len101t0180#models", "21UE0015US", 1))
	if err != nil || len(single.Results) != 1 || single.Results[0].OfferID != "21UE0015US" {
		t.Fatalf("specific SKU not isolated: %+v %v", single, err)
	}
	t.Logf("observed model page: fixed %s $%.2f / %s; CTO %s $%.2f / %s", fixed.OfferID, fixed.Price, fixed.Stock, cto.OfferID, cto.Price, cto.Stock)
}

func TestLenovoMemberCardCannotBorrowPriceOrAddonStock(t *testing.T) {
	card := func(sku, price, button, addon string) string {
		return `<div class="dlp-product-card"><div class="price-stack price-stack_` + sku + `">` + price + `</div>` + button + addon + `<div>Part Number ` + sku + `</div></div>`
	}
	eligible := card("21UE0015US", `<div>Exclusive Price:</div><span class="price-title">$4779</span><del>$7219</del>`, `<button class="product_cta_button">Add To Cart</button>`, "")
	other := card("21UECTO1WWUS1", `<span class="price-title">$3514</span>`, `<button class="product_cta_button cto">Build Your PC</button>`, `<p>Discounted Add-Ons $27</p><button>Add To Cart</button>`)
	o, err := parseLenovo(other+eligible, vipP1Family)
	if err != nil || o.Price != 4779 || o.Results[0].Confirmed || o.Results[0].Stock != stockUnknown {
		t.Fatalf("member label or addon purchase leaked across SKU: %+v %v", o, err)
	}
	if _, err = parseLenovo(other, vipP1Family); err == nil {
		t.Fatal("CTO-only page became verified member stock")
	}
	blocked := strings.Replace(eligible, `class="product_cta_button"`, `class="product_cta_button" disabled`, 1)
	if _, err = parseLenovo(blocked+`<button>Buy Now</button>`, vipP1Family); err == nil {
		t.Fatal("unrelated active button confirmed disabled target stock")
	}
	if _, err = parseLenovo(`<div class="product-price">Exclusive Price: $999</div><button>Add To Cart</button><script type="application/ld+json">{"@type":"Product","sku":"len101t0180","offers":{"@type":"Offer","price":"999","availability":"https://schema.org/InStock"}}</script>`, vipP1Family); err == nil {
		t.Fatal("unloaded family model list accepted a generic or recommended-product price")
	}
}

func TestLenovoBrowserFailureIsNotMembershipLogin(t *testing.T) {
	a := testApp(t)
	p := product("browser-unavailable")
	p.URL = vipP1Family
	p.LenovoResults = []DellResult{{OfferID: "21UE0015US", Price: 4779, Stock: stockIn, Confirmed: true, Matched: true}}
	a.store.Products = append(a.store.Products, p)
	a.fetch = func(context.Context, Product, func(string)) (Observation, error) {
		return Observation{}, fmt.Errorf("页面解析：Lenovo VIP 未读到当前售价；采集浏览器：采集浏览器启动后退出（signal: segmentation fault）")
	}
	a.schedule(p.ID)
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if p.NeedsAction != "采集浏览器不可用；自动检查已暂停" || !p.LenovoResults[0].Stale || p.LenovoResults[0].Matched || len(a.alerts) != 0 {
		t.Fatalf("browser crash mislabeled as membership login or kept current matches: %+v", p)
	}
}

func TestLenovoModelHistoryAlertsAndDataPreservation(t *testing.T) {
	a := testApp(t)
	p := product("vip")
	p.URL = vipP1Family
	p.TargetPrice = 4000
	a.mu.Lock()
	defer a.mu.Unlock()
	initial := Observation{Price: 4779, Stock: stockIn, Results: []DellResult{{OfferID: "21UE0015US", Price: 4779, Stock: stockIn, Confirmed: true}, {OfferID: "21UECTO1WWUS1", Price: 3514, Stock: stockUnknown, Confirmed: false}}}
	a.applyLocked(p, initial)
	if p.LastPrice != 4779 || len(p.LenovoResults) != 2 || len(p.DellResults) != 0 || len(a.alerts) != 0 {
		t.Fatal("CTO starting price triggered threshold or model fields mis-stored")
	}
	a.applyLocked(p, initial)
	if len(p.History) != 1 {
		t.Fatal("unchanged per-SKU snapshot duplicated history")
	}
	newModel := Observation{Results: []DellResult{{OfferID: "21UE7777US", Price: 4200, Stock: stockIn, Confirmed: true}}}
	a.applyLocked(p, newModel)
	if len(a.alerts) != 0 {
		t.Fatal("changing cheapest model generated a price-drop alert for another SKU")
	}
	newModel.Results[0].Price = 3900
	a.applyLocked(p, newModel)
	if len(a.alerts) != 1 || !strings.Contains(a.alerts[0].Message, "21UE7777US") {
		t.Fatal("same-SKU drop or member threshold failed")
	}
	markStale(p)
	for _, r := range p.LenovoResults {
		if !r.Stale || r.Matched {
			t.Fatal("stale Lenovo matches remain eligible")
		}
	}
}

func TestLenovoModelsThroughAPIAndRestart(t *testing.T) {
	body := observedLenovoCards(t)
	a := testApp(t)
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != vipP1Family && r.URL.String() != p1CTOURL {
			t.Errorf("member URL changed: %s", r.URL)
		}
		if r.URL.String() == p1CTOURL {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(liveCTOFixture(t)))}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	post := func(path string, value any) {
		t.Helper()
		b, _ := json.Marshal(value)
		r, e := http.Post(srv.URL+path, "application/json", bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("POST %s: %d %s", path, r.StatusCode, b)
		}
	}
	get := func(path string, value any) {
		t.Helper()
		r, e := http.Get(srv.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("GET %s: %d", path, r.StatusCode)
		}
		if e = json.NewDecoder(r.Body).Decode(value); e != nil {
			t.Fatal(e)
		}
	}
	p := Product{Name: "P1 VIP integration", URL: vipP1Family, IntervalMin: 10, CooldownMin: 1000, TargetPrice: 5000, MinDiscount: 30, AlertOnPriceDrop: true, AlertOnRestock: true, Active: true}
	post("/api/save", p)
	var products []Product
	get("/api/products", &products)
	p.ID = products[0].ID
	post("/api/check?id="+p.ID, nil)
	waitChecked(t, a, p.ID)
	get("/api/products", &products)
	if len(products) != 1 || products[0].LastPrice != 4779 || products[0].Stale || !products[0].LastTrusted || len(products[0].LenovoResults) != 2 || len(products[0].DellResults) != 0 {
		t.Fatalf("API snapshot mis-bound: %+v", products)
	}
	var rows []DellResult
	get("/api/model-results?id="+p.ID, &rows)
	if len(rows) != 2 || rows[0].Matched || !rows[1].Matched || rows[1].OfferID != "21UE0015US" {
		t.Fatalf("model details/threshold mismatch: %+v", rows)
	}
	var alerts []Alert
	get("/api/alerts", &alerts)
	if len(alerts) != 1 || !strings.Contains(alerts[0].Message, "21UE0015US") || strings.Contains(alerts[0].Message, "21UECTO1WWUS1") {
		t.Fatalf("CTO/other SKU alerted: %+v", alerts)
	}
	p.TargetPrice = 4000
	post("/api/save", p)
	get("/api/model-results?id="+p.ID, &rows)
	if len(rows) != 2 || rows[0].Matched || rows[1].Matched {
		t.Fatal("threshold edit retained old match or removed models")
	}
	post("/api/check?id="+p.ID, nil)
	waitChecked(t, a, p.ID)
	var history []HistoryPoint
	get("/api/history?id="+p.ID, &history)
	if len(history) != 1 {
		t.Fatal("same observed cards created duplicate history")
	}
	a.shutdown()
	loaded, err := newApp(filepath.Dir(a.file), true)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.shutdown()
	q := loaded.store.Products[0]
	if q.URL != vipP1Family || q.LastPrice != 4779 || len(q.LenovoResults) != 2 || len(q.History) != 1 || q.TargetPrice != 4000 {
		t.Fatalf("model/settings persistence lost: %+v", q)
	}
	for _, r := range q.LenovoResults {
		if !r.Stale || r.Matched {
			t.Fatal("restart presents old model as currently matching")
		}
	}
	t.Log("observed-card integration passed: save, check, model details, per-SKU alert, threshold edit, history, persistence/restart")
}

func TestLenovoDynamicBrowserCardLoading(t *testing.T) {
	body := observedLenovoCards(t)
	static := `<html><title>ThinkPad P1 Gen 9</title><div>Models loading</div></html>`
	a := testApp(t)
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Request: r, Body: io.NopCloser(strings.NewReader(static))}, nil
	})
	var reads atomic.Int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, map[string]string{"Browser": "protocol-test"})
	})
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, map[string]string{"id": "models", "webSocketDebuggerUrl": "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools"})
	})
	mux.HandleFunc("/json/close/models", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, true) })
	mux.Handle("/devtools", websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		for {
			var req struct {
				ID     int            `json:"id"`
				Params map[string]any `json:"params"`
			}
			if websocket.JSON.Receive(ws, &req) != nil {
				return
			}
			v := static
			if req.Params["expression"] == "location.href" {
				v = vipP1Family
			} else if reads.Add(1) > 1 {
				v = string(body)
			}
			if websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": v}}}) != nil {
				return
			}
		}
	}))
	srv = httptest.NewServer(mux)
	defer srv.Close()
	a.browser.sessions["lenovo"] = &browserSession{port: strings.TrimPrefix(srv.URL, "http://127.0.0.1:")}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	o, err := a.fetchObservation(ctx, Product{URL: vipP1Family}, func(string) {})
	if err != nil || o.Price != 4779 || o.Stock != stockIn || len(o.Results) != 2 || reads.Load() < 3 {
		t.Fatalf("dynamic member cards not loaded and settled: %+v reads=%d err=%v", o, reads.Load(), err)
	}
	t.Log("browser protocol fixture waited for observed model cards; this is not a native Chrome/Windows runtime test")
}
