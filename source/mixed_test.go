package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

const liveXPSURL = "https://www.dell.com/en-us/shop/dell-laptops/spd/xps16da16260/da16260_reg_01"

func xpsFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/dell_xps_" + name + "_20261005.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "[Truncated]") {
		t.Fatal("truncated live fixture")
	}
	return string(b)
}

func TestLiveXPSMixedCollection(t *testing.T) {
	a := testApp(t)
	byo, offers := xpsFixture(t, "byo"), xpsFixture(t, "offers")
	q, err := currentBYO(byo, liveXPSURL)
	if err != nil || q.Price != 2399.99 || !q.Custom || q.DiscountConfirmed || len(q.Options) < 10 {
		t.Fatalf("actual BYO quote: %+v %v", q, err)
	}
	var clicked atomic.Bool
	var traversals atomic.Int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, map[string]string{"id": "page", "webSocketDebuggerUrl": "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools"})
	})
	mux.Handle("/devtools", websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		for {
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if websocket.JSON.Receive(ws, &req) != nil {
				return
			}
			expr, _ := req.Params["expression"].(string)
			v := offers
			if strings.Contains(expr, "ViewAllConfigurations") {
				clicked.Store(true)
				v = "clicked"
			} else if expr != browserHTMLExpr {
				traversals.Add(1)
				v = "unexpected"
			}
			if websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"value": v}}}) != nil {
				return
			}
		}
	}))
	srv = httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := openTarget(ctx, &browserSession{port: strings.TrimPrefix(srv.URL, "http://127.0.0.1:")}, liveXPSURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.ws.Close()
	p := product("xps")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	p.TargetPrice = 3000
	o, err := parseDell(byo, p.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	o, err = a.completeDellMixed(ctx, *p, byo, o, c, func(string) {})
	if err != nil || o.Partial || len(o.Results) != 3 || !clicked.Load() || traversals.Load() != 0 {
		t.Fatalf("mixed collection: %+v %v", o, err)
	}
	a.applyLocked(p, o)
	if p.LastPrice != 2649.99 {
		t.Fatalf("custom total replaced ordinary quote: %+v", p)
	}
	byID := map[string]DellResult{}
	for _, r := range p.DellResults {
		byID[r.OfferID] = r
		if r.Custom && r.Matched {
			t.Fatal("unpublished custom discount matched")
		}
	}
	if !byID["da16260_fixed_71"].Matched || !byID["da16260_so_8"].Confirmed || byID["da16260_so_8"].Matched {
		t.Fatalf("ordinary offers lost/gates incorrect: %+v", byID)
	}
}

func TestCustomNeverGeneratesPriceOrRestockAlerts(t *testing.T) {
	a := testApp(t)
	p := product("both")
	p.TargetPrice = 500
	p.AlertOnPriceDrop = true
	p.AlertOnRestock = true
	p.AlertOnDiscountIncrease = true
	custom := DellResult{OfferID: "custom-one", Custom: true, Confirmed: true, DiscountConfirmed: true, Price: 900, Original: 1800, Discount: 50, Stock: stockOut}
	a.applyLocked(p, Observation{Results: []DellResult{custom}})
	custom.Price = 700
	custom.Original = 1400
	custom.Stock = stockIn
	a.applyLocked(p, Observation{Results: []DellResult{custom}})
	if len(a.alerts) != 0 {
		t.Fatalf("custom price/restock reason: %+v", a.alerts)
	}
	custom.Discount = 60
	fixed := DellResult{OfferID: "fixed", Confirmed: true, Stock: stockIn, Price: 400, Discount: 20}
	a.applyLocked(p, Observation{Results: []DellResult{fixed, custom}})
	if len(a.alerts) != 1 || !strings.Contains(a.alerts[0].Message, "折扣提高") || !strings.Contains(a.alerts[0].Message, "fixed 达到条件") {
		t.Fatalf("two types must alert together: %+v", a.alerts)
	}
	if p.LastPrice != 400 {
		t.Fatal("ordinary summary uses custom price")
	}
}

func TestReadCurrentDoesNotRefreshOrRetryBlockedRequest(t *testing.T) {
	a := testApp(t)
	var requests, mutations atomic.Int32
	a.client.Transport = quoteTransport(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, fmt.Errorf("should not request site")
	})
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, true) })
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, []map[string]string{{"id": "current", "type": "page", "url": liveXPSURL, "webSocketDebuggerUrl": "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools"}})
	})
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) { mutations.Add(1); http.Error(w, "must reuse", 500) })
	body := xpsFixture(t, "byo")
	var blocked atomic.Bool
	mux.Handle("/devtools", websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		for {
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if websocket.JSON.Receive(ws, &req) != nil {
				return
			}
			expr, _ := req.Params["expression"].(string)
			v := body
			if expr == "location.href" {
				v = liveXPSURL
			} else if req.Method != "Runtime.evaluate" || expr != browserHTMLExpr {
				mutations.Add(1)
			} else if blocked.Load() {
				v = "<html><title>Access Denied</title><body>Access Denied</body></html>"
			}
			if websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"value": v}}}) != nil {
				return
			}
		}
	}))
	srv = httptest.NewServer(mux)
	defer srv.Close()
	a.browser.sessions["dell"] = &browserSession{visible: true, port: strings.TrimPrefix(srv.URL, "http://127.0.0.1:")}
	p := product("recovery")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	p.NeedsAction = "等待网页验证"
	p.LastError = "网页访问被限制"
	a.store.Products = append(a.store.Products, p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	o, err := a.readCurrentObservation(ctx, *p, func(string) {})
	if err != nil || o.Partial || len(o.Results) != 1 || !o.Results[0].Custom {
		t.Fatalf("read loaded page: %+v %v", o, err)
	}
	a.applyLocked(p, o)
	if p.NeedsAction != "" || len(a.alerts) != 0 || requests.Load() != 0 || mutations.Load() != 0 {
		t.Fatalf("recovery retried or alerted: %+v", p)
	}
	blocked.Store(true)
	_, err = a.readCurrentObservation(ctx, *p, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "网页访问被限制") || mutations.Load() != 0 {
		t.Fatalf("blocked current page accepted/retried: %v", err)
	}
	if matchingCollectorURL("https://www.dell.com/en-us/shop/dell-laptops/spd/othermodel", liveXPSURL, true) {
		t.Fatal("unrelated family accepted")
	}
}
