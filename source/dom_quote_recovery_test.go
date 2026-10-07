package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func delayedUIAQuote(t *testing.T) (nativePage, map[string]any) {
	t.Helper()
	p := capturedNative(t, true)
	p.Regions["hero-section"] = &nativeNode{Kind: "Group", Name: "Dell Price $5649.99 Estimated Value $6500 Offer ID da16260_reg_01"}
	raw, e := osReadDOMFixture()
	if e != nil {
		t.Fatal(e)
	}
	v := map[string]any{}
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	v["url"] = p.URL
	v["price"] = 2999.99
	v["offerID"] = "da16260_reg_01"
	return p, v
}
func osReadDOMFixture() ([]byte, error) { return os.ReadFile("testdata/dom_snapshot_v815.json") }

func TestVisibleCurrentQuoteRecoversLaggingUIAWithoutOldDiscountOrStock(t *testing.T) {
	p, v := delayedUIAQuote(t)
	oldRoot := p.Regions["configuration-section"]
	reads, snapshots := 0, 0
	got, e := waitOwnedDOM(context.Background(), p, func(_ context.Context, current nativePage) (nativePage, error) {
		reads++
		raw, _ := json.Marshal(v)
		return applyOwnedDOMSnapshot(current, nativeRequest{URL: p.URL, Family: true}, string(raw))
	}, func(_ context.Context, current nativePage) (nativePage, error) { snapshots++; return p, nil }, time.Millisecond)
	if e != nil || reads != 3 || snapshots != 2 || got.DOMQuote == nil {
		t.Fatalf("reads=%d snapshots=%d quote=%+v err=%v", reads, snapshots, got.DOMQuote, e)
	}
	if p.Regions["configuration-section"] != oldRoot || p.DOMQuote != nil {
		t.Fatal("pending candidate mutated prior snapshot")
	}
	o, e := nativeObservation(got, p.URL, true)
	if e != nil || len(o.Results) != 1 {
		t.Fatal(e)
	}
	row := o.Results[0]
	if row.Price != 2999.99 || row.Original != 0 || row.DiscountConfirmed || row.Stock != stockUnknown || row.Confirmed {
		t.Fatalf("old metadata accepted: %+v", row)
	}
	a := testApp(t)
	product := product("dom-price")
	product.URL = p.URL
	product.MinDiscount = 1
	product.TargetPrice = 99999
	a.applyLocked(product, o)
	if len(a.alerts) > 0 {
		t.Fatal("old discount produced an alert")
	}
}

func TestVisibleQuoteFallbackRequiresStableIdentityAndMatchingOffer(t *testing.T) {
	for _, mode := range []string{"changing-price", "changing-options", "changing-uia-price", "wrong-offer", "missing-offer", "unbound-core", "missing-core", "loading"} {
		t.Run(mode, func(t *testing.T) {
			p, v := delayedUIAQuote(t)
			reads := 0
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			got, e := waitOwnedDOM(ctx, p, func(_ context.Context, current nativePage) (nativePage, error) {
				reads++
				switch mode {
				case "changing-price":
					v["price"] = 2999.99 + float64(reads%2)*100
				case "changing-options":
					// Actual selected names alternate, so no stable configuration identity.
					root := v["configuration"].(map[string]any)
					groups := root["children"].([]any)
					for _, entry := range groups {
						g := entry.(map[string]any)
						if strings.HasPrefix(fmt.Sprint(g["id"]), "label-") {
							continue
						}
						for _, child := range g["children"].([]any) {
							c := child.(map[string]any)
							if selected, _ := c["selected"].(bool); selected {
								c["name"] = fmt.Sprintf("Choice %d. Selected", reads%2)
							}
						}
					}
				case "changing-uia-price":
					current.Regions["hero-section"] = &nativeNode{Kind: "Group", Name: fmt.Sprintf("Dell Price $%.2f Offer ID da16260_reg_01", 5649.99+float64(reads%2)*100)}
				case "wrong-offer":
					v["offerID"] = "da16260_reg_02"
				case "missing-offer":
					delete(v, "offerID")
				case "missing-core":
					root := v["configuration"].(map[string]any)
					filtered := []any{}
					for _, entry := range root["children"].([]any) {
						g := entry.(map[string]any)
						if !strings.Contains(fmt.Sprint(g["id"]), "PJ3K2R") {
							filtered = append(filtered, entry)
						}
					}
					root["children"] = filtered
				case "unbound-core":
					v["configuration"] = map[string]any{"id": "configuration-section"}
				case "loading":
					v["pending"] = true
					v["reason"] = "整机报价仍在加载"
				}
				raw, _ := json.Marshal(v)
				return applyOwnedDOMSnapshot(current, nativeRequest{URL: p.URL, Family: true}, string(raw))
			}, func(_ context.Context, current nativePage) (nativePage, error) { return p, nil }, time.Millisecond)
			if e == nil || got.DOMQuote != nil {
				t.Fatalf("unsafe fallback: %+v %v", got.DOMQuote, e)
			}
			var q *nativeQuoteFailure
			if !errors.As(e, &q) {
				t.Fatal(e)
			}
		})
	}
}

func TestRecoveredQuoteStillRequiresRequestedOption(t *testing.T) {
	p, v := delayedUIAQuote(t)
	raw, _ := json.Marshal(v)
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		return waitOwnedDOM(ctx, p, func(_ context.Context, current nativePage) (nativePage, error) {
			return applyOwnedDOMSnapshot(current, nativeRequest{URL: p.URL, Family: true}, string(raw))
		}, func(_ context.Context, current nativePage) (nativePage, error) { return p, nil }, time.Millisecond)
	}
	recovered, e := b.call(context.Background(), nativeRequest{})
	if e != nil {
		t.Fatal(e)
	}
	_, e = b.stableWhen(context.Background(), recovered, nativeRequest{URL: p.URL, Family: true, Action: "select", GroupID: "modulePJ3K2R", OptionID: "not-actually-selected", OptionName: "Unselected CPU"}, func(next nativePage) bool {
		g, _ := nativeGroupByID(next, "modulePJ3K2R")
		for _, opt := range g.Options {
			if opt.Selected && opt.ID == "not-actually-selected" {
				return true
			}
		}
		return false
	}, "核心选项切换未确认（处理器）")
	if e == nil || !strings.Contains(e.Error(), "未把旧配置当成切换成功") {
		t.Fatal("fallback bypassed selection", e)
	}
}
