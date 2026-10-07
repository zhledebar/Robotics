package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

type nativeChoiceModel struct {
	cpu, ssd, display, view string
	deny, broken            bool
	selections, opens       int
}

func (m *nativeChoiceModel) page() nativePage {
	if m.view == "ordinary" {
		return nativePage{Handle: 99, URL: liveXPSURL, CanCustom: true, Regions: map[string]*nativeNode{"offers-container": {Kind: "Group", Children: []*nativeNode{{Kind: "Group", Name: "Dell Price $900 Estimated Value $1000 Offer ID da16260_fixed_71", Children: []*nativeNode{{Kind: "Text", Name: "Dell Price $900 Estimated Value $1000 Offer ID da16260_fixed_71"}, {Kind: "Button", Name: "Add to Cart", Enabled: true}}}}}}}
	}
	price := 1000.0
	memory := "16 GB"
	if m.cpu == "CPU B" {
		price = 2000
		memory = "32 GB"
	}
	if m.ssd == "SSD B" {
		price += 100
	}
	if m.display == "OLED" {
		price += 50
	}
	root := &nativeNode{Kind: "Group"}
	expanded := true
	for _, g := range []struct {
		id, label, current string
		opts               []string
	}{{"moduleCPU", "Processor", m.cpu, []string{"CPU A", "CPU B"}}, {"moduleMEM", "Memory", memory, []string{memory}}, {"moduleSSD", "Storage", m.ssd, []string{"SSD S", "SSD B"}}, {"moduleSCREEN", "Displays", m.display, []string{"LED", "OLED"}}} {
		root.Children = append(root.Children, &nativeNode{ID: "label-" + g.id, Name: g.label, Kind: "Button", Enabled: true, Expanded: &expanded})
		scope := &nativeNode{ID: g.id, Kind: "Group"}
		for _, name := range g.opts {
			selected := name == g.current
			enabled := !selected
			if m.cpu == "CPU A" && m.ssd == "SSD S" && name == "OLED" {
				enabled = false
			}
			label := name + ". + $100.00"
			if selected {
				label = name + ". Selected"
			}
			scope.Children = append(scope.Children, &nativeNode{Kind: "Button", Name: label, Selected: selected, Enabled: enabled})
		}
		root.Children = append(root.Children, scope)
	}
	if m.broken {
		root.Children[3].Children[0].Selected = false
		root.Children[3].Children[0].Enabled = false
	}
	page := nativePage{Handle: 99, URL: liveXPSURL, CanOrdinary: true, Regions: map[string]*nativeNode{"hero-section": {Kind: "Group", Name: fmt.Sprintf("Dell Price $%.2f Estimated Value $%.2f Offer ID da16260_reg_01", price, price/0.8)}, "configuration-section": root, "add-to-cart-stack": {Kind: "Group", Children: []*nativeNode{{Kind: "Button", Name: "Add to Cart", Enabled: true}}}}}
	if m.deny {
		page.Error = "普通浏览器网页访问被限制（access denied）"
	}
	return page
}
func (m *nativeChoiceModel) call(ctx context.Context, r nativeRequest) (nativePage, error) {
	switch r.Action {
	case "open":
		m.opens++
		m.view = "custom"
	case "custom":
		m.view = "custom"
	case "ordinary":
		m.view = "ordinary"
	case "select":
		if m.view != "custom" {
			return nativePage{}, fmt.Errorf("selection attempted in ordinary view")
		}
		m.selections++
		key := nativeOptionKey(r.OptionName)
		switch r.GroupID {
		case "moduleCPU":
			m.cpu = key
			m.ssd = "SSD S"
			m.display = "LED"
		case "moduleSSD":
			m.ssd = key
		case "moduleSCREEN":
			m.display = key
		}
	case "close", "cleanup":
		return nativePage{}, nil
	}
	return m.page(), nil
}
func TestNativeCoreScanReadsDependentEntireQuotes(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.call = m.call
	rows, partial, e := b.scanCustomCore(context.Background(), m.page(), nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(string) {})
	if e != nil || partial || len(rows) != 7 {
		t.Fatalf("core combinations: %d partial=%v error=%v", len(rows), partial, e)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if !r.Custom || !r.DiscountConfirmed || r.ConfigUnbound || r.Discount < 19.99 || r.Discount > 20.01 || seen[r.OfferID] {
			t.Fatalf("unverified/duplicate quote: %+v", r)
		}
		seen[r.OfferID] = true
		if strings.Contains(r.Options["moduleCPU"], "CPU B") && !strings.Contains(r.Options["moduleMEM"], "32 GB") {
			t.Fatalf("CPU dependency lost: %+v", r)
		}
		if strings.Contains(r.Options["moduleCPU"], "CPU A") && strings.Contains(r.Options["moduleSSD"], "SSD S") && strings.Contains(r.Options["moduleSCREEN"], "OLED") {
			t.Fatal("impossible combination synthesized")
		}
	}
	if m.opens != 0 {
		t.Fatal("scan opened extra browser windows")
	}
}
func TestNativeCollectReturnsToCustomBeforeFullCoreScan(t *testing.T) {
	app := testApp(t)
	b := app.normal
	b.available = true
	b.scanCore = true
	b.pollDelay = time.Millisecond
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	b.call = m.call
	p := product("full-core")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	p.DellScanMode = "full"
	p.TargetPrice = 99999
	p.MinDiscount = 100
	o, e := b.collect(context.Background(), *p, func(string) {})
	if e != nil || o.Partial || len(o.Results) != 8 || m.opens != 1 {
		t.Fatalf("mixed full collection: %d partial=%v error=%v opens=%d", len(o.Results), o.Partial, e, m.opens)
	}
	app.applyLocked(p, o)
	if len(app.alerts) != 0 {
		t.Fatalf("custom total triggered price alert: %+v", app.alerts)
	}
}
func TestNativeCoreScanCannotPretendUnboundOrDeniedIsComplete(t *testing.T) {
	for _, kind := range []string{"unbound", "denied", "selection-not-confirmed"} {
		t.Run(kind, func(t *testing.T) {
			m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
			page := m.page()
			if kind == "unbound" {
				delete(page.Regions, "configuration-section")
			}
			b := newNormalBrowser()
			b.pollDelay = time.Millisecond
			b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
				p, e := m.call(ctx, r)
				if r.Action == "select" {
					if kind == "denied" {
						p.Error = "网页访问被限制（access denied）"
					} else if kind == "selection-not-confirmed" {
						m.cpu = "CPU A"
						m.ssd = "SSD S"
						m.display = "LED"
						p = m.page()
					}
				}
				return p, e
			}
			rows, partial, e := b.scanCustomCore(context.Background(), page, nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(string) {})
			if e == nil || !partial || len(rows) == 0 {
				t.Fatalf("incomplete scan marked complete: %d %v %v", len(rows), partial, e)
			}
			if kind == "denied" && (!strings.Contains(e.Error(), "access denied") || strings.Contains(e.Error(), "下轮自动重验")) {
				t.Fatal("denial hidden by retry classification")
			}
		})
	}
}
func TestNativeOptionKeysDoNotTreatPriceAsIdentity(t *testing.T) {
	for _, name := range []string{"CPU A. + $100.00", "CPU A. – $100.00", "CPU A. Selected", "CPU A. + $0.00", "CPU A. $0.00", "CPU A. $100.00", "CPU A. $0.00. Selected"} {
		if nativeOptionKey(name) != "CPU A" {
			t.Fatalf("bad name normalization: %q", name)
		}
	}
}
func TestNativeReaderNormalizesDellRoleButtonOptions(t *testing.T) {
	raw, e := os.ReadFile("normal_reader.ps1")
	if e != nil {
		t.Fatal(e)
	}
	s := string(raw)
	for _, required := range []string{`n.kind=="Custom"`, "ControlType.Custom", `role="button"`, `n.kind="Button"`} {
		if !strings.Contains(s, required) {
			t.Fatalf("missing Dell role-button compatibility: %s", required)
		}
	}
}

func TestNativeCoreCSharpActionsStayInsideOwnedCoreRegion(t *testing.T) {
	raw, e := os.ReadFile("normal_reader.ps1")
	if e != nil {
		t.Fatal(e)
	}
	s := string(raw)
	for _, required := range []string{"action==\"expand\"||action==\"collapse\"||action==\"select\"", "ByID(doc,groupID)", "Name(choice)==optionName&&choice.Current.IsEnabled", "不是核心配置区域", "Validate(h,expected,family);Invoke(target)", "ExpandCollapsePattern.Pattern"} {
		if !strings.Contains(s, required) {
			t.Fatalf("missing action guard %s", required)
		}
	}
}

func TestCapturedXPSCoreStructureTraversesFortyChoices(t *testing.T) {
	page := capturedNative(t, true)
	groups := nativeCoreGroups(page)
	counts := map[string]int{}
	for _, g := range groups {
		counts[g.Label] = len(g.Options)
	}
	if counts["processor"] != 5 || counts["storage"] != 4 || counts["displays"] != 2 {
		t.Fatalf("captured core options missing: %v", counts)
	}
	var amounts []*nativeNode
	for _, id := range []string{"hero-section", "add-to-cart-stack"} {
		walkNative(page.Regions[id], func(n *nativeNode) {
			if n.Name == "$2,399.99" {
				amounts = append(amounts, n)
			}
		})
	}
	if len(amounts) < 2 {
		t.Fatal("fixture has no two own total regions")
	}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	selections := 0
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		g, ok := nativeGroupByID(page, r.GroupID)
		if r.Action == "expand" {
			if !ok {
				return page, fmt.Errorf("missing core group")
			}
			expanded := true
			g.Header.Expanded = &expanded
		}
		if r.Action == "select" {
			if !ok {
				return page, fmt.Errorf("missing selected group")
			}
			var target *nativeNode
			for _, n := range g.Options {
				if n.Name == r.OptionName {
					target = n
				}
			}
			if target == nil {
				return page, fmt.Errorf("target option missing")
			}
			for _, n := range g.Options {
				key := nativeOptionKey(n.Name)
				n.Selected = n == target
				n.Enabled = n != target
				n.Name = key + ". + $123.00"
				if n.Selected {
					n.Name = key + ". Selected"
				}
			}
			selections++
			price := fmt.Sprintf("$%.2f", 3000-float64(selections%127)*3.17) // simulated non-delta quote
			for _, n := range amounts {
				n.Name = price
			}
		}
		return page, nil
	}
	rows, partial, e := b.scanCustomCore(context.Background(), page, nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(string) {})
	if e != nil || partial || len(rows) != 40 {
		t.Fatalf("captured core traversal: %d partial=%v %v", len(rows), partial, e)
	}
	for _, r := range rows {
		if !r.Custom || r.DiscountConfirmed || r.CPU == "" || r.Storage == "" || r.Display == "" {
			t.Fatalf("unpublished discount or missing option binding: %+v", r)
		}
	}
}

func TestNativeCoreScanDoesNotRequireUnknownWarrantyText(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	page := func() nativePage {
		p := m.page()
		p.Regions["configuration-section"].Children = append(p.Regions["configuration-section"].Children, &nativeNode{ID: "label-moduleWARRANTY", Name: "Warranty", Kind: "Button", Enabled: true})
		return p
	}
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		_, e := m.call(ctx, r)
		return page(), e
	}
	rows, partial, e := b.scanCustomCore(context.Background(), page(), nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(string) {})
	if e != nil || partial || len(rows) != 7 {
		t.Fatalf("unread non-core text prevented core traversal: %d %v %v", len(rows), partial, e)
	}
	for _, r := range rows {
		if !r.ConfigUnbound || !strings.Contains(r.Note, "不判断同配置折扣提高") {
			t.Fatalf("unknown non-core identity hidden: %+v", r)
		}
	}
}

func TestNativeCoreBudgetPreservesRowsBeforeOuterTimeout(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	calls := 0
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) { calls++; return m.call(ctx, r) }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, partial, e := b.scanCustomCore(ctx, m.page(), nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(string) {})
	if e == nil || !partial || len(rows) != 1 || ctx.Err() != nil || calls != 0 {
		t.Fatalf("outer deadline consumed or partial rows lost: %d %v %v outer=%v calls=%d", len(rows), partial, e, ctx.Err(), calls)
	}
}

func TestNativeCoreDoesNotResumeAfterDeniedSecondView(t *testing.T) {
	app := testApp(t)
	b := app.normal
	b.available = true
	b.scanCore = true
	b.pollDelay = time.Millisecond
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	denied := false
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if denied {
			t.Fatalf("collector continued after denial: %s", r.Action)
		}
		p, e := m.call(ctx, r)
		if r.Action == "ordinary" {
			p.Error = "网页访问被限制（access denied）"
			denied = true
		}
		return p, e
	}
	p := product("hard-stop")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	o, e := b.collect(context.Background(), *p, func(string) {})
	if e != nil || !o.Partial || len(o.Results) != 1 || m.selections != 0 || !strings.Contains(o.Note, "access denied") {
		t.Fatalf("hard second-view failure not preserved: %+v %v", o, e)
	}
	// Cleanup is independently allowed after the check has stopped.
	denied = false
}

func TestZeroDeltaAndSelectedHaveSameConfigurationIdentity(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	a, b := m.page(), m.page()
	expanded := true
	addGPU := func(p nativePage, label string) {
		root := p.Regions["configuration-section"]
		root.Children = append(root.Children, &nativeNode{ID: "label-moduleGPU", Name: "Graphics Card", Kind: "Button", Enabled: true, Expanded: &expanded}, &nativeNode{ID: "moduleGPU", Kind: "Group", Children: []*nativeNode{{ID: "GPU-Arc", Name: label, Kind: "Button", Selected: true, Enabled: true}}})
	}
	addGPU(a, "Intel® Arc™ graphics. $0.00")
	addGPU(b, "Intel® Arc™ graphics. Selected")
	x, ex := nativeObservation(a, liveXPSURL, true)
	y, ey := nativeObservation(b, liveXPSURL, true)
	if ex != nil || ey != nil || x.Results[0].OfferID != y.Results[0].OfferID || nativeSignature(a, x) != nativeSignature(b, y) {
		t.Fatalf("zero price changed identity: %v %v", ex, ey)
	}
	if x.Results[0].Price != 1000 || y.Results[0].Price != 1000 {
		t.Fatal("option delta used as quote")
	}
	if nativeOptionKey("Intel® Arc™ graphics. $0.00") != "Intel® Arc™ graphics" {
		t.Fatal("zero delta still part of identity")
	}
}
