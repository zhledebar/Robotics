package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func ownUnlabeledCustomPage() nativePage {
	money := func(price string) *nativeNode { return &nativeNode{Kind: "Text", Name: price, Enabled: true} }
	return nativePage{Handle: 99, URL: liveXPSURL, CanOrdinary: true, Regions: map[string]*nativeNode{
		"hero-section":      {Kind: "Group", Children: []*nativeNode{{Kind: "Text", Name: "Offer ID da16260_reg_01", Enabled: true}, money("$2,399.99"), {Kind: "Group", Children: []*nativeNode{money("$200"), {Kind: "Text", Name: "/mo"}}}}},
		"add-to-cart-stack": {Kind: "Group", Children: []*nativeNode{money("$2,399.99"), {Kind: "Group", Children: []*nativeNode{money("$200"), {Kind: "Text", Name: "/mo"}}}, {Kind: "Button", Name: "Add to Cart", Enabled: true}}},
	}}
}

func TestNativeCustomPriceWithoutHiddenLabelUsesTwoOwnRegions(t *testing.T) {
	p := ownUnlabeledCustomPage()
	o, err := nativeObservation(p, liveXPSURL, true)
	if err != nil || o.Price != 2399.99 || len(o.Results) != 1 || !o.Results[0].Custom || !o.Results[0].ConfigUnbound || o.Results[0].DiscountConfirmed {
		t.Fatalf("own total lost or monthly mistaken for total: %+v %v", o, err)
	}
	if values := nativeStandaloneAmounts(p.Regions["hero-section"]); len(values) != 1 || !values[2399.99] {
		t.Fatalf("monthly amount escaped exclusion: %v", values)
	}
	app := testApp(t)
	product := product("unbound-total")
	product.URL = liveXPSURL
	product.TargetPrice = 9999
	product.MinDiscount = 1
	app.applyLocked(product, o)
	if len(app.alerts) != 0 {
		t.Fatal("unpublished custom discount or total caused an alert")
	}
}

func TestNativeUnlabeledPriceRejectsAmbiguityAndWrongCart(t *testing.T) {
	for _, mode := range []string{"different-total", "two-common-totals", "foreign-offer", "starting-at", "monthly-only"} {
		t.Run(mode, func(t *testing.T) {
			p := ownUnlabeledCustomPage()
			switch mode {
			case "different-total":
				p.Regions["add-to-cart-stack"].Children[0].Name = "$2,999.99"
			case "two-common-totals":
				for _, id := range []string{"hero-section", "add-to-cart-stack"} {
					p.Regions[id].Children = append(p.Regions[id].Children, &nativeNode{Kind: "Text", Name: "$3,999.99", Enabled: true})
				}
			case "foreign-offer":
				p.Regions["add-to-cart-stack"].Children = append(p.Regions["add-to-cart-stack"].Children, &nativeNode{Kind: "Text", Name: "Offer ID pc16250_fixed_22", Enabled: true})
			case "starting-at":
				for _, id := range []string{"hero-section", "add-to-cart-stack"} {
					idx := 0
					if id == "hero-section" {
						idx = 1
					}
					p.Regions[id].Children[idx] = &nativeNode{Kind: "Group", Children: []*nativeNode{{Kind: "Text", Name: "Starting at"}, {Kind: "Text", Name: "$2,399.99"}}}
				}
			case "monthly-only":
				p.Regions["hero-section"].Children = p.Regions["hero-section"].Children[:1]
				p.Regions["hero-section"].Children = append(p.Regions["hero-section"].Children, &nativeNode{Kind: "Text", Name: "$200/mo"})
				p.Regions["add-to-cart-stack"].Children = []*nativeNode{{Kind: "Text", Name: "$200/mo"}}
			}
			if _, err := nativeObservation(p, liveXPSURL, true); err == nil {
				t.Fatal("unverified or unrelated money accepted as current total")
			}
		})
	}
}

func TestNativePriceLabelOnNonLeafAndConflict(t *testing.T) {
	n := &nativeNode{Kind: "Group", Name: "Dell Price $2,399.99", Children: []*nativeNode{{Kind: "Group"}}}
	if price, _ := nativePrice(n); price != 2399.99 {
		t.Fatal("non-leaf visible price label ignored")
	}
	n.Children = append(n.Children, &nativeNode{Kind: "Text", Name: "Dell Price $2,499.99"})
	if price, _ := nativePrice(n); price != 0 {
		t.Fatal("contradictory current prices accepted")
	}
}

func TestNativeUnlabeledMonthlyParentNamesAreExcluded(t *testing.T) {
	p := ownUnlabeledCustomPage()
	for _, id := range []string{"hero-section", "add-to-cart-stack"} {
		idx := 1
		if id == "hero-section" {
			idx = 2
		}
		p.Regions[id].Children[idx] = &nativeNode{Kind: "Group", Name: "Financing monthly payments", Children: []*nativeNode{{Kind: "Custom", Name: "$200", Children: []*nativeNode{{Kind: "Group"}}}}}
	}
	o, err := nativeObservation(p, liveXPSURL, true)
	if err != nil || o.Price != 2399.99 {
		t.Fatalf("non-leaf finance captions leaked a monthly value: %+v %v", o, err)
	}
	for _, id := range []string{"hero-section", "add-to-cart-stack"} {
		idx := 0
		if id == "hero-section" {
			idx = 1
		}
		p.Regions[id].Children[idx].Name = ""
	}
	if _, err := nativeObservation(p, liveXPSURL, true); err == nil {
		t.Fatal("monthly-only native names mistaken for a total")
	}
}

func TestNativeCustomViewSurvivesMissingSwitchAndOptions(t *testing.T) {
	p := ownUnlabeledCustomPage()
	p.CanOrdinary = false
	if nativeView(p) != "custom" {
		t.Fatal("missing switch reclassified own CTO as ordinary")
	}
	o, err := nativeObservation(p, liveXPSURL, true)
	if err != nil || !o.Results[0].ConfigUnbound {
		t.Fatalf("verified total blocked by unreadable selected options: %+v %v", o, err)
	}
	p.CanCustom = true
	if nativeView(p) != "ordinary" {
		t.Fatal("explicit ordinary view switch ignored")
	}
}

func TestNativeCustomStabilityRequiresStockButIgnoresTracking(t *testing.T) {
	for _, changeStock := range []bool{false, true} {
		t.Run(fmt.Sprintf("stock-change=%v", changeStock), func(t *testing.T) {
			b := newNormalBrowser()
			b.pollDelay = time.Millisecond
			initial := capturedNative(t, true)
			calls := 0
			b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
				calls++
				next := capturedNative(t, true)
				next.URL = liveXPSURL + "?tracking=changed#gallery"
				if changeStock {
					walkNative(next.Regions["add-to-cart-stack"], func(n *nativeNode) {
						if n.Kind == "Button" {
							n.Enabled = false
						}
					})
				}
				return next, nil
			}
			wantCalls := 1
			if changeStock {
				wantCalls = 2
			}
			got, err := b.stable(context.Background(), initial, nativeRequest{URL: liveXPSURL, Family: true, Handle: 99})
			if err != nil || calls != wantCalls {
				t.Fatalf("stock evidence/irrelevant tracking: %v calls=%d want=%d", err, calls, wantCalls)
			}
			if changeStock {
				o, err := nativeObservation(got, liveXPSURL, true)
				if err != nil || o.Results[0].Stock != stockUnknown || o.Results[0].Confirmed {
					t.Fatal("accepted old availability after stock changed")
				}
			}
		})
	}
}

func TestNativeQuoteStabilityRejectsPriceChanges(t *testing.T) {
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	calls := 0
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		calls++
		p := capturedNative(t, true)
		if calls%2 == 1 {
			walkNative(p.Regions["hero-section"], func(n *nativeNode) { n.Name = strings.ReplaceAll(n.Name, "$2,399.99", "$2,499.99") })
		}
		return p, nil
	}
	_, err := b.stable(context.Background(), capturedNative(t, true), nativeRequest{URL: liveXPSURL, Family: true, Handle: 99})
	var failure *nativeQuoteFailure
	if !errors.As(err, &failure) || !strings.Contains(err.Error(), "连续变化") || calls != 7 {
		t.Fatalf("unstable quote accepted or mistyped: %v calls=%d", err, calls)
	}
}

func TestNativeStableKeepsParseReasonAndRetryableState(t *testing.T) {
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	p := ownUnlabeledCustomPage()
	p.Regions["add-to-cart-stack"].Children[0].Name = "$2,999.99"
	b.call = func(context.Context, nativeRequest) (nativePage, error) { return p, nil }
	_, err := b.stable(context.Background(), p, nativeRequest{URL: liveXPSURL, Family: true, Handle: 99})
	if err == nil || !strings.Contains(err.Error(), "定制整机现价未识别") || strings.Contains(err.Error(), "未稳定") {
		t.Fatalf("parse failure swallowed: %v", err)
	}
	if actionForFailure(liveXPSURL, normalReaderError(err).Error()) != "" {
		t.Fatal("transient missing quote paused future checks")
	}
	p.Title = "Access Denied"
	_, err = b.stable(context.Background(), p, nativeRequest{URL: liveXPSURL, Family: true, Handle: 99})
	var failure *nativeQuoteFailure
	if err == nil || errors.As(err, &failure) || actionForFailure(liveXPSURL, normalReaderError(err).Error()) == "" {
		t.Fatal("denied page was allowed to retry or change views")
	}
}

func TestNativeFirstViewUnavailableStillReadsOrdinary(t *testing.T) {
	a := testApp(t)
	a.normal.available = true
	a.normal.pollDelay = time.Millisecond
	p := product("partial-first")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	p.TargetPrice = 2300
	p.MinDiscount = 10
	p.DellResults = []DellResult{{OfferID: "previous-custom", Custom: true, Price: 1000, Discount: 70, DiscountConfirmed: true, Confirmed: true, Matched: true}}
	current := ownUnlabeledCustomPage()
	current.Regions["add-to-cart-stack"].Children[0].Name = "$2,999.99"
	ordinary := capturedNative(t, false)
	ordinary.CanCustom = false
	a.normal.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "close" || r.Action == "cleanup" {
			return nativePage{}, nil
		}
		if r.Action == "ordinary" {
			current = ordinary
		}
		return current, nil
	}
	o, err := a.normal.collect(context.Background(), *p, func(string) {})
	if err != nil || !o.Partial || !o.VerifiedNative || len(o.Results) != 2 {
		t.Fatalf("initial custom failure blocked own ordinary quotes: %+v %v", o, err)
	}
	a.applyLocked(p, o)
	if p.LastPrice != 2249.99 || !p.LastTrusted || p.NeedsAction != "" || len(a.alerts) != 1 {
		t.Fatalf("fresh ordinary quote lost or scan unnecessarily paused: %+v alerts=%v", p, a.alerts)
	}
	for _, row := range p.DellResults {
		if row.OfferID == "previous-custom" && (!row.Stale || row.Matched) {
			t.Fatal("old custom participated in matching")
		}
	}
}

func TestNativeHistoricalRowsDoNotMakeCurrentScanIncomplete(t *testing.T) {
	a := testApp(t)
	a.normal.pollDelay = time.Millisecond
	p := product("history")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	for i := 0; i < 7; i++ {
		p.DellResults = append(p.DellResults, DellResult{OfferID: fmt.Sprintf("historical-%d", i), Price: 1000, Stock: stockIn, Confirmed: true, Matched: true})
	}
	page := capturedNative(t, false)
	page.CanCustom = false
	a.normal.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "close" || r.Action == "cleanup" {
			return nativePage{}, nil
		}
		return page, nil
	}
	o, err := a.normal.collect(context.Background(), *p, func(string) {})
	if err != nil || o.Partial {
		t.Fatalf("historical rows mislabeled current scan: %+v %v", o, err)
	}
	a.applyLocked(p, o)
	if p.ScanIncomplete || p.LastError != "" || p.NeedsAction != "" || len(p.DellResults) != 9 {
		t.Fatalf("history lost or current success marked incomplete: %+v", p)
	}
	old := 0
	for _, row := range p.DellResults {
		if strings.HasPrefix(row.OfferID, "historical-") {
			old++
			if !row.Stale || row.Matched {
				t.Fatal("historical row still matches")
			}
		}
	}
	if old != 7 {
		t.Fatal("history was removed")
	}
}

func TestNativeOptionWhitespaceHasStableIdentity(t *testing.T) {
	one, two := capturedNative(t, true), capturedNative(t, true)
	walkNative(two.Regions["configuration-section"], func(n *nativeNode) { n.Name = strings.ReplaceAll(n.Name, " ", " \n ") })
	a, err := nativeObservation(one, liveXPSURL, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := nativeObservation(two, liveXPSURL, true)
	if err != nil {
		t.Fatal(err)
	}
	if a.Results[0].OfferID != b.Results[0].OfferID || nativeSignature(one, a) != nativeSignature(two, b) {
		t.Fatal("formatting-only whitespace changed configuration or quote identity")
	}
}
