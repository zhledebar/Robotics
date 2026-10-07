package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/net/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeDOMCapturedFortyCombinationScan(t *testing.T) {
	raw, err := os.ReadFile("testdata/dom_snapshot_v815.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Configuration *nativeNode `json:"configuration"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	root := fixture.Configuration
	state := map[string]int{}
	page := func() nativePage {
		data, _ := json.Marshal(root)
		var copy nativeNode
		json.Unmarshal(data, &copy)
		price := 2399.99 + float64(state["modulePJ3K2R"]*100+state["moduleR75JGN"]*250+state["moduleW21P6T"]*150)
		return nativePage{Handle: 99, URL: liveXPSURL, CanOrdinary: true, Regions: map[string]*nativeNode{
			"configuration-section": &copy,
			"hero-section":          {Kind: "Group", Name: fmt.Sprintf("Dell Price $%.2f Offer ID da16260_reg_01", price)},
			"add-to-cart-stack":     {Kind: "Group", Children: []*nativeNode{{Kind: "Text", Name: fmt.Sprintf("Dell Price $%.2f", price)}, {Kind: "Button", Name: "Add to Cart", Enabled: true}}},
		}}
	}
	for _, g := range nativeCoreGroups(page()) {
		for i, o := range g.Options {
			if o.Selected {
				state[g.ID] = i
			}
		}
	}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "select" {
			for _, g := range nativeCoreGroups(page()) {
				if g.ID != r.GroupID {
					continue
				}
				found := -1
				for i, o := range g.Options {
					if o.Name == r.OptionName {
						found = i
					}
				}
				if found < 0 {
					return nativePage{}, fmt.Errorf("choice changed")
				}
				state[g.ID] = found
				walkNative(root, func(n *nativeNode) {
					if n.ID != g.ID {
						return
					}
					for i, o := range n.Children {
						o.Selected = i == found
						o.Name = nativeOptionKey(o.Name)
						if o.Selected {
							o.Name += ". Selected"
						} else {
							o.Name += ". + $100.00"
						}
					}
				})
			}
		}
		if r.Action == "expand" || r.Action == "collapse" {
			walkNative(root, func(n *nativeNode) {
				if n.ID == "label-"+r.GroupID {
					v := r.Action == "expand"
					n.Expanded = &v
				}
			})
		}
		return page(), nil
	}
	rows, partial, err := b.scanCustomCore(context.Background(), page(), nativeRequest{Handle: 99, Owner: "dom-40", URL: liveXPSURL, Family: true}, func(string) {})
	if err != nil || partial || len(rows) != 40 {
		t.Fatalf("rows=%d partial=%v err=%v", len(rows), partial, err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.ConfigUnbound || r.CPU == "" || r.Storage == "" || r.Display == "" || r.Price <= 0 || seen[r.OfferID] {
			t.Fatalf("invalid verified row: %+v", r)
		}
		seen[r.OfferID] = true
	}
}

func TestOwnedDOMLoadingWaitsForCurrentQuote(t *testing.T) {
	for _, mode := range []string{"loading", "price-sync"} {
		t.Run(mode, func(t *testing.T) {
			p := ownUnlabeledCustomPage()
			initialPrice, _ := nativeCustomPrice(p)
			finalPrice := initialPrice + 100
			r := nativeRequest{URL: p.URL, Family: true}
			reads, snapshots := 0, 0
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := waitOwnedDOM(ctx, p, func(_ context.Context, current nativePage) (nativePage, error) {
				reads++
				var v map[string]any
				if mode == "loading" && reads < 3 {
					v = map[string]any{"url": p.URL, "pending": true, "reason": "整机报价仍在加载；未接受旧报价"}
				} else {
					v = map[string]any{"url": p.URL, "price": finalPrice, "configuration": map[string]any{"id": "configuration-section", "name": "new selection"}}
				}
				raw, _ := json.Marshal(v)
				return applyOwnedDOMSnapshot(current, r, string(raw))
			}, func(_ context.Context, current nativePage) (nativePage, error) {
				snapshots++
				if snapshots >= 2 {
					current.Regions["hero-section"] = &nativeNode{Kind: "Group", Name: fmt.Sprintf("Dell Price $%.2f Offer ID %s", finalPrice, offerFromURL(p.URL))}
				}
				return current, nil
			}, time.Millisecond)
			price, _ := nativeCustomPrice(got)
			if err != nil || snapshots != 2 || reads != 3 || price != finalPrice || got.Regions["configuration-section"].Name != "new selection" {
				t.Fatalf("stale quote or loading accepted: reads=%d snapshots=%d price=%v err=%v", reads, snapshots, price, err)
			}
		})
	}
}

func TestOwnedDOMPersistentLoadingRetriesNextInterval(t *testing.T) {
	p := ownUnlabeledCustomPage()
	before := p.Regions["configuration-section"]
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	got, err := waitOwnedDOM(ctx, p, func(_ context.Context, current nativePage) (nativePage, error) {
		return applyOwnedDOMSnapshot(current, nativeRequest{URL: p.URL, Family: true}, fmt.Sprintf(`{"url":%q,"pending":true,"reason":"整机报价仍在加载；未接受旧报价"}`, p.URL))
	}, func(_ context.Context, current nativePage) (nativePage, error) {
		return current, nil
	}, time.Millisecond)
	var failure *nativeQuoteFailure
	if !errors.As(err, &failure) || actionForFailure(p.URL, normalReaderError(err).Error()) != "" || got.Regions["configuration-section"] != before || got.Handle != p.Handle {
		t.Fatalf("loading paused monitoring or accepted config: got=%+v err=%v", got, err)
	}
}

func TestOwnedDOMWaitStopsOnHardFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"wrong-url", "script-error", "canceled", "snapshot-error", "reset"} {
		t.Run(mode, func(t *testing.T) {
			p := ownUnlabeledCustomPage()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reads, snapshots := 0, 0
			got, err := waitOwnedDOM(ctx, p, func(_ context.Context, current nativePage) (nativePage, error) {
				reads++
				switch mode {
				case "wrong-url":
					return applyOwnedDOMSnapshot(current, nativeRequest{URL: p.URL, Family: true}, `{"url":"https://example.com","pending":true,"reason":"loading"}`)
				case "script-error":
					return current, errors.New("网页脚本执行失败")
				case "canceled":
					cancel()
				}
				return current, &ownedDOMPending{Reason: "loading"}
			}, func(_ context.Context, current nativePage) (nativePage, error) {
				snapshots++
				if mode == "reset" {
					return nativePage{ResetVerified: true}, context.DeadlineExceeded
				}
				return nativePage{}, errors.New("网页访问被限制（access denied）")
			}, time.Millisecond)
			var temporary *nativeQuoteFailure
			if err == nil || reads != 1 {
				t.Fatalf("hard error retried: reads=%d err=%v", reads, err)
			}
			if mode == "reset" {
				if !got.ResetVerified || !errors.As(err, &temporary) {
					t.Fatalf("reset confirmation lost: %+v %v", got, err)
				}
			} else if errors.As(err, &temporary) || got.Handle != p.Handle {
				t.Fatalf("hard error classified as temporary or ownership lost: %+v %v", got, err)
			}
			if mode == "canceled" && (!errors.Is(err, context.Canceled) || snapshots != 0) {
				t.Fatal("cancellation ignored")
			}
		})
	}
}

func TestOwnedDOMTargetRejectsOtherTabAndEndpoint(t *testing.T) {
	for _, mode := range []string{"valid", "other-tab", "foreign-url", "foreign-endpoint", "wrong-port"} {
		t.Run(mode, func(t *testing.T) {
			var srv *httptest.Server
			mux := http.NewServeMux()
			mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
				ws := "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools/page/owned"
				url := liveXPSURL
				if mode == "foreign-url" {
					url = "https://example.com"
				}
				if mode == "foreign-endpoint" {
					ws = "ws://example.com/devtools/page/owned"
				}
				if mode == "wrong-port" {
					ws = "ws://127.0.0.1:1/devtools/page/owned"
				}
				targets := []map[string]string{{"type": "page", "url": url, "webSocketDebuggerUrl": ws}}
				if mode == "other-tab" {
					targets = append(targets, map[string]string{"type": "page", "url": "about:blank"})
				}
				json.NewEncoder(w).Encode(targets)
			})
			mux.Handle("/devtools/page/owned", websocket.Handler(func(ws *websocket.Conn) { defer ws.Close(); var v string; websocket.Message.Receive(ws, &v) }))
			srv = httptest.NewServer(mux)
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, err := ownedDOMTarget(ctx, strings.TrimPrefix(srv.URL, "http://127.0.0.1:"), liveXPSURL, true)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				c.ws.Close()
			} else if err == nil {
				c.ws.Close()
				t.Fatal("unowned page accepted")
			}
		})
	}
}

func TestOwnedDOMQuoteSnapshotMustMatch(t *testing.T) {
	raw, err := os.ReadFile("testdata/dom_snapshot_v815.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"valid", "wrong-price", "wrong-url", "no-configuration", "missing-price", "pending-diagnostic"} {
		t.Run(mode, func(t *testing.T) {
			var v map[string]any
			json.Unmarshal(raw, &v)
			p := ownUnlabeledCustomPage()
			before := p.Regions["configuration-section"]
			v["url"] = p.URL
			switch mode {
			case "wrong-price":
				v["price"] = 9999
			case "wrong-url":
				v["url"] = "https://example.com"
			case "no-configuration":
				delete(v, "configuration")
			case "missing-price":
				delete(v, "price")
			case "pending-diagnostic":
				v["pending"] = true
				v["reason"] = "商品首屏及购买栏的当前整机总价尚未加载"
			}
			data, _ := json.Marshal(v)
			updated, err := applyOwnedDOMSnapshot(p, nativeRequest{URL: liveXPSURL, Family: true}, string(data))
			if mode == "valid" {
				if err != nil || len(nativeCoreGroups(updated)) != 5 {
					t.Fatalf("DOM config not bound: %v", err)
				}
			} else {
				if err == nil || updated.Regions["configuration-section"] != before {
					t.Fatal("unmatched quote accepted or configuration changed")
				}
				if mode == "pending-diagnostic" {
					var pending *ownedDOMPending
					if !errors.As(err, &pending) || !strings.Contains(pending.Diagnostic, "priceScopes") {
						t.Fatal("price visibility diagnostic lost")
					}
				}
			}
		})
	}
}
