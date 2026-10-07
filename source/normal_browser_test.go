package main

import (
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/net/html"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// An accessible-tree model of real scoped HTML, NOT a Windows UIA execution.
// IDs and labels come from the captured product page; browser exposure of these
// elements still requires native Windows acceptance.
func fixtureNative(n *html.Node) *nativeNode {
	if n == nil {
		return nil
	}
	if n.Type == html.TextNode {
		if s := strings.TrimSpace(n.Data); s != "" {
			return &nativeNode{Name: s, Kind: "Text", Enabled: true}
		}
		return nil
	}
	if n.Type != html.ElementNode && n.Type != html.DocumentNode {
		return nil
	}
	if n.Data == "script" || n.Data == "style" || attr(n, "aria-hidden") == "true" {
		return nil
	}
	for _, a := range n.Attr {
		if a.Key == "hidden" {
			return nil
		}
	}
	x := &nativeNode{ID: attr(n, "id"), Class: attr(n, "class"), Kind: "Group", Enabled: attr(n, "aria-disabled") != "true"}
	for _, a := range n.Attr {
		if a.Key == "disabled" {
			x.Enabled = false
		}
	}
	if n.Data == "button" || attr(n, "role") == "button" {
		x.Kind = "Button"
	}
	if attr(n, "role") == "checkbox" {
		x.Kind = "CheckBox"
	}
	if attr(n, "role") == "radio" {
		x.Kind = "RadioButton"
	}
	if x.Kind == "Button" || x.Kind == "CheckBox" || x.Kind == "RadioButton" {
		x.Name = attr(n, "aria-label")
		if x.Name == "" {
			x.Name = textOf(n)
		}
	}
	x.Selected = attr(n, "data-is-selected") == "true" || attr(n, "aria-checked") == "true"
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if q := fixtureNative(c); q != nil {
			x.Children = append(x.Children, q)
		}
	}
	return x
}
func capturedNative(t *testing.T, custom bool) nativePage {
	t.Helper()
	f := "dell_xps_feed_offers_live_20261005.html"
	if custom {
		f = "dell_xps_feed_custom_live_20261005.html"
	}
	body, e := os.ReadFile(filepath.Join("testdata", f))
	if e != nil {
		t.Fatal(e)
	}
	doc, e := html.Parse(strings.NewReader(string(body)))
	if e != nil {
		t.Fatal(e)
	}
	p := nativePage{Handle: 99, URL: liveXPSURL, Title: "XPS 16 Laptop", CanOrdinary: custom, CanCustom: !custom, Regions: map[string]*nativeNode{}}
	if !custom {
		p.Regions["offers-container"] = fixtureNative(doc)
		return p
	}
	for _, id := range []string{"hero-section", "configuration-section", "offers-container", "add-to-cart-stack"} {
		found := nodes(doc, func(n *html.Node) bool { return attr(n, "id") == id })
		if len(found) > 0 {
			p.Regions[id] = fixtureNative(found[0])
		}
	}
	return p
}
func TestNormalBrowserRealProductTreeModel(t *testing.T) {
	ordinary := capturedNative(t, false)
	o, e := nativeObservation(ordinary, liveXPSURL, true)
	if e != nil || len(o.Results) != 2 {
		t.Fatalf("ordinary: %+v %v", o, e)
	}
	by := map[string]DellResult{}
	for _, r := range o.Results {
		by[r.OfferID] = r
	}
	r := by["da16260_fixed_71"]
	if r.Price != 2249.99 || r.Original != 2649.99 || math.Abs(r.Discount-15.094) > 0.01 || r.Stock != stockIn || !r.Confirmed {
		t.Fatalf("actual own quote/stock: %+v", r)
	}
	if by["da16260_so_8"].Price != 3449.99 {
		t.Fatalf("other ordinary quote: %+v", by)
	}
	custom := capturedNative(t, true)
	q, e := nativeObservation(custom, liveXPSURL, true)
	if e != nil || len(q.Results) != 1 {
		t.Fatalf("custom: %+v %v heroIDs=%v price=%q", q, e, nativeOfferIDs(custom.Regions["hero-section"]), nativeText(custom.Regions["hero-section"]))
	}
	c := q.Results[0]
	if !c.Custom || c.Price != 2399.99 || c.Original != 0 || c.DiscountConfirmed || len(c.Options) < 10 || strings.Contains(c.OfferID, "UNBOUND") {
		t.Fatalf("actual current custom, no borrowed discount: %+v", c)
	}
	t.Logf("real HTML model: ordinary %.2f/%.2f %.2f%%, custom %.2f %d options unpublished discount", r.Price, r.Original, r.Discount, c.Price, len(c.Options))
}
func TestNormalBrowserAutoMixedRefreshAndPartialRules(t *testing.T) {
	a := testApp(t)
	b := a.normal
	b.available = true
	ordinary, custom := capturedNative(t, false), capturedNative(t, true)
	page := custom
	var mu sync.Mutex
	var calls []nativeRequest
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, r)
		if r.Owner == "" {
			t.Error("missing window owner")
		}
		switch r.Action {
		case "close", "cleanup":
			return nativePage{}, nil
		case "open", "refresh":
			page = custom
		case "ordinary":
			page = ordinary
		case "custom":
			page = custom
		case "snapshot":
		default:
			t.Errorf("unexpected browser action %q", r.Action)
		}
		return page, nil
	}
	a.client.Transport = quoteTransport(func(*http.Request) (*http.Response, error) {
		t.Error("HTTP fallback on native route")
		return nil, errors.New("must not fetch")
	})
	p := product("native")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	p.TargetPrice = 2300
	p.MinDiscount = 10
	p.DellResults = []DellResult{{OfferID: "old-combination", Custom: true, Price: 1000, Original: 2000, Discount: 50, DiscountConfirmed: true, Matched: true}}
	o, e := a.fetchObservation(context.Background(), *p, func(string) {})
	if e != nil || len(o.Results) != 3 || o.Partial || !o.VerifiedNative {
		t.Fatalf("mixed: %+v %v", o, e)
	}
	a.applyLocked(p, o)
	if p.LastPrice != 2249.99 || len(p.DellResults) != 4 || len(a.alerts) != 1 {
		t.Fatalf("ordinary summary or alerts lost: %+v alerts=%+v", p, a.alerts)
	}
	for _, r := range p.DellResults {
		if r.OfferID == "old-combination" && (!r.Stale || r.Matched) {
			t.Fatal("old custom result still matches")
		}
		if r.Custom && !r.Stale && r.Matched {
			t.Fatal("unpublished current custom matches")
		}
	}
	if strings.Contains(a.alerts[0].Message, "old-combination") {
		t.Fatal("stale row notified")
	}
	o, e = b.collect(context.Background(), *p, func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	opens, refreshes := 0, 0
	for _, r := range calls {
		if r.Action == "open" {
			opens++
		}
		if r.Action == "refresh" {
			refreshes++
			if r.Handle != 99 {
				t.Fatal("wrong refresh window")
			}
		}
	}
	if opens != 1 || refreshes != 1 {
		t.Fatalf("new owned window then toolbar refresh: opens=%d refresh=%d", opens, refreshes)
	}
}
func TestNormalBrowserDeniedOrChangedPageNeverRetries(t *testing.T) {
	for _, which := range []string{"denied", "wrong-model", "wrong-host", "closed"} {
		t.Run(which, func(t *testing.T) {
			b := newNormalBrowser()
			b.available = true
			calls := 0
			b.call = func(context.Context, nativeRequest) (nativePage, error) {
				calls++
				p := capturedNative(t, false)
				switch which {
				case "denied":
					p.Title = "Access Denied"
				case "wrong-model":
					p.URL = strings.Replace(p.URL, "xps16da16260", "different", 1)
				case "wrong-host":
					p.URL = strings.Replace(p.URL, "www.dell.com", "other.example", 1)
				case "closed":
					p.Handle = 0
				}
				return p, nil
			}
			p := product("n")
			p.URL = liveXPSURL
			p.DellFamilyScan = true
			if _, e := b.collect(context.Background(), *p, func(string) {}); e == nil || calls != 1 {
				t.Fatalf("unsafe retry/accept: calls=%d err=%v", calls, e)
			}
		})
	}
}
func TestNormalBrowserClosedOwnedWindowReopensOnlyOnce(t *testing.T) {
	b := newNormalBrowser()
	b.window = nativeWindow{Handle: 99, Owner: "owner", URL: liveXPSURL}
	actions := []string{}
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		actions = append(actions, r.Action)
		if r.Action == "refresh" {
			return nativePage{Error: "监控商品窗口已关闭"}, nil
		}
		return capturedNative(t, false), nil
	}
	p := product("n")
	p.URL = liveXPSURL
	p.DellFamilyScan = false
	if _, e := b.collect(context.Background(), *p, func(string) {}); e == nil { // reg URL deliberately cannot accept an ordinary fixed quote
		t.Fatal("fixed URL must not accept a different offer")
	}
	if len(actions) < 2 || actions[0] != "refresh" || actions[1] != "open" {
		t.Fatalf("closed owned window not replaced: %v", actions)
	}
}
func TestNormalBrowserFailuresPauseKeepOldData(t *testing.T) {
	a := testApp(t)
	p := product("n")
	p.URL = liveXPSURL
	p.LastPrice = 1000
	p.LastTrusted = true
	p.TargetPrice = 3000
	p.DellResults = []DellResult{{OfferID: "old", Price: 1000, Confirmed: true, Matched: true, Stock: stockIn}}
	a.store.Products = append(a.store.Products, p)
	a.normal.available = true
	calls := 0
	a.normal.call = func(context.Context, nativeRequest) (nativePage, error) {
		calls++
		return nativePage{Error: "普通浏览器网页访问被限制（access denied）；已停止本轮"}, nil
	}
	a.schedule(p.ID)
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if calls != 1 || p.LastPrice != 1000 || !p.Stale || p.LastTrusted || p.NeedsAction == "" || p.DellResults[0].Matched || len(a.alerts) != 0 {
		t.Fatalf("denied consumed trusted old row: %+v calls=%d", p, calls)
	}
}
func TestNormalBrowserV80ExtensionMigration(t *testing.T) {
	dir := t.TempDir()
	p := product("m")
	p.URL = liveXPSURL
	p.BrowserFeed = true
	p.CustomDiscountOnly = true
	p.NeedsAction = "等待浏览器助手连接"
	p.TargetPrice = 2300
	p.MinDiscount = 20
	p.LastPrice = 2399.99
	p.DellResults = []DellResult{{OfferID: "custom-old", Custom: true, DiscountConfirmed: true, Matched: true, Price: 2000, Discount: 40}}
	p.History = []HistoryPoint{{Price: 1234}}
	data, _ := json.Marshal(Store{Schema: 80, Products: []*Product{p}})
	if e := os.WriteFile(filepath.Join(dir, "monitor_data.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	a, e := newApp(dir, true)
	if e != nil {
		t.Fatal(e)
	}
	defer a.shutdown()
	q := a.store.Products[0]
	if a.store.Schema != 90 || q.BrowserFeed || q.CustomDiscountOnly || q.NeedsAction != "" || q.URL != p.URL || q.TargetPrice != 2300 || q.MinDiscount != 20 || q.LastPrice != 2399.99 || q.LastTrusted || !q.DellResults[0].Stale || q.DellResults[0].Matched || len(q.History) != 1 {
		t.Fatalf("migration %+v", q)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "monitor_data_before_v810_*.json"))
	if len(backups) != 1 {
		t.Fatal("missing original backup")
	}
	if e = a.persist(); e != nil {
		t.Fatal(e)
	}
}
func TestNormalBrowserFixedOfferAndForeignCardProtection(t *testing.T) {
	page := capturedNative(t, false)
	foreign := &nativeNode{Class: "card-deck-item", Kind: "Group", Children: []*nativeNode{{ID: "compare-box-foreign_fixed_1", Kind: "Button"}, {Kind: "Text", Name: "Dell Price $1.00 Estimated Value $99.00"}, {Kind: "Button", Name: "Add to Cart", Enabled: true}}}
	page.Regions["offers-container"].Children = append(page.Regions["offers-container"].Children, foreign)
	o, e := nativeObservation(page, liveXPSURL, true)
	if e != nil || len(o.Results) != 2 {
		t.Fatalf("foreign model accepted: %+v %v", o, e)
	}
	requested := strings.Replace(liveXPSURL, "da16260_reg_01", "da16260_fixed_71", 1)
	page.URL = requested
	o, e = nativeObservation(page, requested, false)
	if e != nil || len(o.Results) != 1 || o.Results[0].OfferID != "da16260_fixed_71" {
		t.Fatalf("fixed offer mismatch: %+v %v", o, e)
	}
	page.URL = strings.Replace(requested, "fixed_71", "fixed_72", 1)
	if _, e = nativeObservation(page, requested, false); e == nil {
		t.Fatal("wrong current offer accepted")
	}
}
func TestNormalBrowserCancellationDoesNotOperate(t *testing.T) {
	b := newNormalBrowser()
	b.gate <- struct{}{}
	b.call = func(context.Context, nativeRequest) (nativePage, error) {
		t.Fatal("operation after cancellation")
		return nativePage{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := product("cancel")
	if _, e := b.collect(ctx, *p, func(string) {}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
func TestNormalBrowserHTTPCheckUsesAutoRoute(t *testing.T) {
	a := testApp(t)
	a.normal.available = true
	p := product("api")
	p.URL = strings.Replace(liveXPSURL, "da16260_reg_01", "da16260_fixed_71", 1)
	a.store.Products = append(a.store.Products, p)
	page := capturedNative(t, false)
	page.URL = p.URL
	var mu sync.Mutex
	var actions []string
	a.normal.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		mu.Lock()
		actions = append(actions, r.Action)
		mu.Unlock()
		return page, nil
	}
	srv := httptest.NewServer(a.handler())
	defer srv.Close()
	before := time.Now()
	r, e := http.Post(srv.URL+"/api/check?id=api", "application/json", nil)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 200 || time.Since(before) > time.Second {
		t.Fatal("check blocked UI")
	}
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	ok := p.LastPrice == 2249.99 && p.LastTrusted && !p.Checking
	a.mu.RUnlock()
	if !ok {
		t.Fatalf("auto quote not committed %+v", p)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(actions) != 2 || actions[0] != "open" || actions[1] != "snapshot" {
		t.Fatalf("no-setup API: %v", actions)
	}
}

func TestNormalBrowserUnboundCustomDisablesImprovementOnly(t *testing.T) {
	page := capturedNative(t, true)
	page.Regions["configuration-section"].Children = append(page.Regions["configuration-section"].Children, &nativeNode{ID: "label-moduleMissing", Name: "Missing collapsed choice", Kind: "Button"})
	page.Regions["hero-section"].Children = append(page.Regions["hero-section"].Children, &nativeNode{Name: "Estimated Value $3,000.00", Kind: "Text"})
	first, e := nativeObservation(page, liveXPSURL, true)
	if e != nil {
		t.Fatal(e)
	}
	page.Regions["hero-section"].Children[len(page.Regions["hero-section"].Children)-1].Name = "Estimated Value $3,200.00"
	next, e := nativeObservation(page, liveXPSURL, true)
	if e != nil {
		t.Fatal(e)
	}
	if !first.Results[0].ConfigUnbound || first.Results[0].OfferID != next.Results[0].OfferID {
		t.Fatal("unbound reads grow false configuration IDs")
	}
	a := testApp(t)
	p := product("custom")
	p.URL = liveXPSURL
	p.TargetPrice = 500
	p.AlertOnDiscountIncrease = true
	a.applyLocked(p, first)
	a.applyLocked(p, next)
	if len(a.alerts) != 0 {
		t.Fatal("unbound custom caused same-config or price alert")
	}
	p.MinDiscount = 20
	a.applyLocked(p, next)
	if len(a.alerts) != 1 || !strings.Contains(a.alerts[0].Message, "定制折扣达到条件") {
		t.Fatal("valid own custom threshold lost")
	}
}
func TestNormalBrowserEditedURLClosesPreviousWindowBeforeOpening(t *testing.T) {
	b := newNormalBrowser()
	p := product("edit")
	p.URL = strings.Replace(liveXPSURL, "da16260_reg_01", "da16260_fixed_71", 1)
	b.window = nativeWindow{Handle: 123, Owner: "previous-owner", URL: liveXPSURL}
	closed := false
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "close" {
			if r.Handle != 123 || r.Owner != "previous-owner" || r.URL != liveXPSURL {
				t.Fatal("wrong previous window closed")
			}
			closed = true
			return nativePage{}, nil
		}
		if r.Action == "open" && (!closed || r.Handle != 0 || r.Owner == "previous-owner") {
			t.Fatal("edited product opens before releasing old window")
		}
		page := capturedNative(t, false)
		page.URL = p.URL
		return page, nil
	}
	if _, e := b.collect(context.Background(), *p, func(string) {}); e != nil {
		t.Fatal(e)
	}
}
func TestNormalBrowserLaunchURLCannotInjectBrowserArguments(t *testing.T) {
	b := newNormalBrowser()
	b.call = func(context.Context, nativeRequest) (nativePage, error) {
		t.Fatal("unsafe URL reached browser launcher")
		return nativePage{}, nil
	}
	p := product("unsafe")
	p.URL = liveXPSURL + `?x=" --user-data-dir=unrequested`
	if _, e := b.collect(context.Background(), *p, func(string) {}); e == nil {
		t.Fatal("unsafe launch URL accepted")
	}
}

func TestNormalBrowserStabilityUsesQuoteNotGallery(t *testing.T) {
	b := newNormalBrowser()
	initial := capturedNative(t, true)
	requests := 0
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		requests++
		p := capturedNative(t, true)
		p.Regions["hero-section"].Children = append(p.Regions["hero-section"].Children, &nativeNode{Kind: "Text", Name: "Gallery animation changed"})
		return p, nil
	}
	r := nativeRequest{URL: liveXPSURL, Family: true, Handle: 99, Owner: "own"}
	if _, e := b.stable(context.Background(), initial, r); e != nil || requests != 1 {
		t.Fatalf("unrelated gallery prevents quote verification: requests=%d err=%v", requests, e)
	}
}

func TestNormalBrowserPartialSecondDenialPreservesOnlyVerifiedFreshAlerts(t *testing.T) {
	a := testApp(t)
	p := product("partial-native")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	p.TargetPrice = 2300
	p.MinDiscount = 10
	p.DellResults = []DellResult{{OfferID: "old-custom", Custom: true, Price: 2000, Discount: 50, DiscountConfirmed: true, Matched: true}}
	calls := 0
	a.normal.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		calls++
		if r.Action == "custom" {
			return nativePage{Error: "网页访问被限制（access denied）"}, nil
		}
		return capturedNative(t, false), nil
	}
	o, e := a.normal.collect(context.Background(), *p, func(string) {})
	if e != nil || !o.Partial || !o.VerifiedNative || len(o.Results) != 2 || calls != 3 {
		t.Fatalf("second denial retried/lost fresh data: %+v %v calls=%d", o, e, calls)
	}
	a.applyLocked(p, o)
	if p.NeedsAction == "" || !p.ScanIncomplete || p.LastPrice != 2249.99 || len(a.alerts) != 1 {
		t.Fatalf("partial failed to pause or lost own fresh threshold: %+v alerts=%+v", p, a.alerts)
	}
	for _, r := range p.DellResults {
		if r.OfferID == "old-custom" && (!r.Stale || r.Matched) {
			t.Fatal("previous custom result counted as current")
		}
	}
}
func TestNormalBrowserSingleFixedHeroQuote(t *testing.T) {
	requested := "https://www.dell.com/en-us/shop/laptop-computers/spd/dell-pro-16-pc16250/s012pc16250us_vp"
	// Use the format already supported by Dell's fixed offer identifiers.
	requested = strings.Replace(requested, "s012pc16250us_vp", "pc16250_fixed_1", 1)
	p := nativePage{Handle: 99, URL: requested, Regions: map[string]*nativeNode{
		"hero-section":      {Children: []*nativeNode{{Kind: "Text", Name: "Dell Price"}, {Kind: "Text", Name: "$1,520.00"}, {Kind: "Text", Name: "Estimated Value"}, {Kind: "Text", Name: "$1,700.00"}, {Kind: "Text", Name: "Offer ID"}, {Kind: "Text", Name: "pc16250_fixed_1"}}},
		"add-to-cart-stack": {Children: []*nativeNode{{Kind: "Button", Name: "Add to Cart", Enabled: true}}},
	}}
	o, e := nativeObservation(p, requested, false)
	if e != nil || len(o.Results) != 1 || o.Price != 1520 || o.Original != 1700 || o.Stock != stockIn || !o.Results[0].Confirmed {
		t.Fatalf("single-offer hero: %+v %v", o, e)
	}
	p.Regions["add-to-cart-stack"].Children[0].Enabled = false
	o, e = nativeObservation(p, requested, false)
	if e != nil || o.Stock == stockIn || o.Results[0].Confirmed {
		t.Fatal("disabled purchase control reported stock")
	}
}
