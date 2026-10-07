package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBlockedHTTPBrowserFamilyFallback(t *testing.T) {
	a := testApp(t)
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("<html><title>Access Denied</title><body>Access denied</body></html>")), Header: make(http.Header), Request: r}, nil
	})
	var targets atomic.Int32
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, map[string]string{"Browser": "test"}) })
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) {
		targets.Add(1)
		jsonReply(w, map[string]string{"id": "one", "webSocketDebuggerUrl": "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools"})
	})
	mux.HandleFunc("/json/close/one", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, true) })
	mux.Handle("/devtools", websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		isBYO := false
		selected := "cpu-one"
		for {
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if websocket.JSON.Receive(ws, &req) != nil {
				return
			}
			if req.Method == "Page.navigate" {
				isBYO = true
				_ = websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{}})
				continue
			}
			expr, _ := req.Params["expression"].(string)
			var result string
			if expr == browserHTMLExpr {
				if !isBYO {
					result = `<html><div class="card-deck-item"><button data-oc="a_fixed_1">Compare</button><span class="sale-price">$950</span><button>Add to Cart</button></div><a href="https://www.dell.com/en-us/shop/test/a_reg_1">Customize</a>` + strings.Repeat(" ", 1100) + `</html>`
				} else {
					price := 1000
					if selected == "cpu-two" {
						price = 1250
					}
					result = fmt.Sprintf(`<html><div class="hero-section"><span class="sale-price">$%d</span></div><div id="add-to-cart-stack"><button>Add to Cart</button></div><div data-module-id="CPU" role="group" aria-label="Processor"><div data-option-id="cpu-one" data-is-selected="%v"><span data-test-id="option-title">CPU One</span></div><div data-option-id="cpu-two" data-is-selected="%v"><span data-test-id="option-title">CPU Two</span></div></div>%s</html>`, price, selected == "cpu-one", selected == "cpu-two", strings.Repeat(" ", 1100))
				}
			} else {
				m := regexp.MustCompile(`dataset\.optionId===("[^"]+")`).FindStringSubmatch(expr)
				if len(m) != 2 {
					t.Errorf("unknown test expression: %s", expr)
					return
				}
				var option string
				_ = json.Unmarshal([]byte(m[1]), &option)
				result = "selected"
				if option != selected {
					selected = option
					result = "clicked"
				}
			}
			if websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": result}}}) != nil {
				return
			}
		}
	}))
	srv = httptest.NewServer(mux)
	defer srv.Close()
	a.browser.sessions["dell"] = &browserSession{port: strings.TrimPrefix(srv.URL, "http://127.0.0.1:")}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	p := Product{URL: "https://www.dell.com/en-us/shop/test", DellFamilyScan: true}
	o, err := a.fetchObservation(ctx, p, func(string) {})
	if err != nil || o.Partial || len(o.Results) != 2 {
		t.Fatalf("fallback must scan fixed + BYO: rows=%d partial=%v err=%v", len(o.Results), o.Partial, err)
	}
	if targets.Load() != 1 {
		t.Fatal("browser callback attempted a second locked browser read")
	}
	prices := map[float64]bool{}
	for _, r := range o.Results {
		prices[r.Price] = true
	}
	if !prices[950] || !prices[1000] {
		t.Fatalf("configuration prices lost: %v", prices)
	}
}

func TestFailedScanInvalidatesLegacyMatches(t *testing.T) {
	a := testApp(t)
	p := product("old")
	p.DellFamilyScan = true
	p.LastPrice = 2299.99
	p.LastStock = "固定0 / Deal0 / BYO40 / 符合40"
	for i := 0; i < 40; i++ {
		p.DellResults = append(p.DellResults, DellResult{OfferID: fmt.Sprint(i), Price: 2299.99, Confirmed: true, Matched: true, Stock: stockIn})
	}
	a.store.Products = append(a.store.Products, p)
	a.fetch = func(context.Context, Product, func(string)) (Observation, error) {
		return Observation{}, fmt.Errorf("网页访问被限制（access denied）")
	}
	a.schedule(p.ID)
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if len(p.DellResults) != 40 || p.LastPrice != 2299.99 || p.NeedsAction == "" {
		t.Fatal("old rows/price must remain and require manual verification")
	}
	for _, r := range p.DellResults {
		if !r.Stale || r.Matched {
			t.Fatal("a failed scan advertised an old configuration as a current match")
		}
	}
	if len(a.alerts) != 0 {
		t.Fatal("failed scan generated alert")
	}
}

func TestCurrentBYOKeptWhenBrowserCannotStart(t *testing.T) {
	a := testApp(t)
	t.Setenv("MONITOR_BROWSER", filepath.Join(t.TempDir(), "missing-browser"))
	body := `<html><div class="hero-section"><span class="sale-price">$1200</span></div><div id="add-to-cart-stack"><button>Add to Cart</button></div><div data-module-id="CPU" role="group" aria-label="Processor"><div data-option-id="cpu-a" data-is-selected="true"><span data-test-id="option-title">CPU A</span></div></div></html>`
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: r, Header: make(http.Header)}, nil
	})
	o, err := a.fetchObservation(context.Background(), Product{URL: "https://www.dell.com/en-us/shop/test/a_reg_1", DellFamilyScan: true}, func(string) {})
	if err != nil || o.Price != 1200 || o.Partial || len(o.Results) != 1 || !o.Results[0].Custom {
		t.Fatalf("current price discarded: %+v %v", o, err)
	}
	if !strings.Contains(o.Results[0].Note, "不代表全部升级组合") {
		t.Fatal("a current configuration was presented as fully enumerated")
	}
}

func TestBrowserDeadSessionDiscardedAndEarlyExitReported(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("shell executable fixture is Linux-only")
	}
	d := t.TempDir()
	exe := filepath.Join(d, "browser-test")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONITOR_BROWSER", exe)
	b := newBrowserManager(d)
	dead := make(chan struct{})
	close(dead)
	b.sessions["dell"] = &browserSession{port: "1", done: dead}
	start := time.Now()
	_, err := b.session(context.Background(), "dell", false)
	if err == nil || !strings.Contains(err.Error(), "启动后退出") || time.Since(start) > 2*time.Second {
		t.Fatalf("browser startup failed silently or waited full timeout: %v", err)
	}
	if b.sessions["dell"] != nil {
		t.Fatal("dead session retained")
	}
}
