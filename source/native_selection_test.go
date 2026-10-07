package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Simulate Dell's accordion structure, including a visible selection summary
// next to a collapsed module and hidden choice controls. This is a protocol
// regression fixture; it is not a native Windows UIA run.
type collapsingChoiceModel struct {
	nativeChoiceModel
	expanded map[string]bool
	radio    bool
	expands  int
}

func (m *collapsingChoiceModel) page() nativePage {
	p := m.nativeChoiceModel.page()
	root := p.Regions["configuration-section"]
	values := nativeOptions(root)
	old := root.Children
	root.Children = nil
	for i := 0; i < len(old); i += 2 {
		h, g := old[i], old[i+1]
		expanded := m.expanded[g.ID]
		h.Expanded = &expanded
		heading := &nativeNode{Kind: "Group", Children: []*nativeNode{h}}
		if !expanded {
			heading.Children = append(heading.Children, &nativeNode{Kind: "Text", Class: "option-title-collapsed border-start", Name: values[g.ID], Enabled: true})
			g.Children = nil
		} else if m.radio {
			for _, opt := range g.Children {
				opt.Kind = "RadioButton"
			}
		}
		root.Children = append(root.Children, &nativeNode{Kind: "Group", Class: "accordion-box", Children: []*nativeNode{heading, g}})
	}
	return p
}
func (m *collapsingChoiceModel) call(ctx context.Context, r nativeRequest) (nativePage, error) {
	_, e := m.nativeChoiceModel.call(ctx, r)
	if e != nil {
		return nativePage{}, e
	}
	if r.Action == "expand" {
		m.expands++
		for id := range m.expanded {
			m.expanded[id] = false
		}
		m.expanded[r.GroupID] = true
	}
	if r.Action == "select" {
		m.expanded[r.GroupID] = false
	}
	return m.page(), nil
}
func TestNativeCoreScanHandlesAutoCollapseAndVisibleSummary(t *testing.T) {
	for _, radio := range []bool{false, true} {
		name := "button"
		if radio {
			name = "radio"
		}
		t.Run(name, func(t *testing.T) {
			m := &collapsingChoiceModel{nativeChoiceModel: nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}, expanded: map[string]bool{"moduleCPU": true, "moduleMEM": false, "moduleSSD": false, "moduleSCREEN": false}, radio: radio}
			b := newNormalBrowser()
			b.pollDelay = time.Millisecond
			b.call = m.call
			rows, partial, e := b.scanCustomCore(context.Background(), m.page(), nativeRequest{Handle: 99, Owner: "collapse", URL: liveXPSURL, Family: true}, func(string) {})
			if e != nil || partial || len(rows) != 7 || m.expands < 5 || m.opens != 0 {
				t.Fatalf("collapsed scan rows=%d partial=%v expands=%d opens=%d err=%v", len(rows), partial, m.expands, m.opens, e)
			}
			for _, row := range rows {
				if row.ConfigUnbound || len(row.Options) != 4 {
					t.Fatalf("collapsed summary lost own selection: %+v", row)
				}
			}
		})
	}
}
func TestNativeCollapsedSummaryPreservesProcessorInModelName(t *testing.T) {
	const name = "Series 3 Intel® Core™ Ultra 7 Processor 355 (8 cores, 12MB Cache, up to 4.7 GHz)"
	for _, class := range []string{"option-title-collapsed", ""} {
		header := &nativeNode{ID: "label-moduleCPU", Kind: "Button", Name: "Processor"}
		root := &nativeNode{Children: []*nativeNode{header, {Kind: "Text", Class: class, Name: name, Enabled: true}}}
		value := nativeOptions(root)["moduleCPU"]
		if value != name {
			t.Fatalf("CPU model name corrupted by header removal: %q", value)
		}
	}
}
func TestNativeSelectionWaitsPastStableOldSnapshots(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	calls := 0
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		calls++
		if calls == 4 {
			m.cpu = "CPU B"
		}
		return m.page(), nil
	}
	r := nativeRequest{Action: "select", Handle: 99, URL: liveXPSURL, Family: true, GroupID: "moduleCPU", OptionName: "CPU B. + $100.00"}
	p, e := b.stableWhen(context.Background(), m.page(), r, func(p nativePage) bool {
		return nativeOptionKey(nativeOptions(p.Regions["configuration-section"])[r.GroupID]) == "CPU B"
	}, "expected CPU B")
	if e != nil || calls < 5 || nativeOptionKey(nativeOptions(p.Regions["configuration-section"])["moduleCPU"]) != "CPU B" {
		t.Fatalf("old stable quote accepted: calls=%d err=%v", calls, e)
	}
	o, e := nativeObservation(p, liveXPSURL, true)
	if e != nil || o.Price != 2000 || o.Results[0].Memory != "32 GB" {
		t.Fatalf("new selection quote/dependency mismatch: %+v %v", o, e)
	}
}
func TestNativeSelectionNeverAcceptsUnchangedOldChoice(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	calls := 0
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) { calls++; return m.page(), nil }
	p, e := b.stableWhen(context.Background(), m.page(), nativeRequest{Handle: 99, URL: liveXPSURL, Family: true, GroupID: "moduleCPU", OptionName: "CPU B"}, func(p nativePage) bool {
		return nativeOptionKey(nativeOptions(p.Regions["configuration-section"])["moduleCPU"]) == "CPU B"
	}, "处理器切换未确认")
	if e == nil || !strings.Contains(e.Error(), "处理器切换未确认") || calls != 7 || nativeOptionKey(nativeOptions(p.Regions["configuration-section"])["moduleCPU"]) != "CPU A" {
		t.Fatalf("unverified change accepted: calls=%d err=%v", calls, e)
	}
}
func TestNativeOptionsRequireUniqueActualSelection(t *testing.T) {
	h := &nativeNode{ID: "label-moduleCPU", Kind: "Button", Name: "Processor"}
	for _, selected := range []bool{false, true} {
		group := &nativeNode{ID: "moduleCPU", Children: []*nativeNode{{Kind: "Button", Name: "CPU A. + $0.00", Enabled: true, Selected: selected}, {Kind: "Button", Name: "CPU B. + $100.00", Enabled: true, Selected: selected}}}
		root := &nativeNode{Children: []*nativeNode{h, group}}
		if value := nativeOptions(root)["moduleCPU"]; value != "" {
			t.Fatalf("concatenated/ambiguous selection accepted: %q", value)
		}
	}
}
func TestNativeCollapsedSummaryCannotBorrowAnotherModule(t *testing.T) {
	h := &nativeNode{ID: "label-moduleCPU", Kind: "Button", Name: "Processor"}
	cpu := &nativeNode{Children: []*nativeNode{h, &nativeNode{Class: "option-title-collapsed", Name: "CPU A"}, &nativeNode{Kind: "Button", Name: "Which processor is right for you?"}}}
	gpu := &nativeNode{Children: []*nativeNode{{ID: "label-moduleGPU", Kind: "Button", Name: "Graphics Card"}, {Class: "option-title-collapsed", Name: "RTX Pro 8 GB"}}}
	root := &nativeNode{Children: []*nativeNode{cpu, gpu}}
	options := nativeOptions(root)
	if options["moduleCPU"] != "CPU A" || options["moduleGPU"] != "RTX Pro 8 GB" {
		t.Fatalf("module summary not scoped: %v", options)
	}
}
func TestNativeButtonSelectionPatternAndSemanticChoiceContract(t *testing.T) {
	raw, e := os.ReadFile("normal_reader.ps1")
	if e != nil {
		t.Fatal(e)
	}
	src := string(raw)
	start := strings.Index(src, "private static PMNode SnapshotNode(")
	end := strings.Index(src, "private static AutomationElement ActionButton(")
	snapshot := src[start:end]
	for _, required := range []string{"bool semanticChoice=c.ControlType==ControlType.Button||c.ControlType==ControlType.CheckBox||c.ControlType==ControlType.RadioButton", "n.kind==\"Custom\"", "InvokePattern.Pattern", "SelectionItemPattern.Pattern", "TogglePattern.Pattern"} {
		if !strings.Contains(snapshot, required) {
			t.Fatalf("button state source missing %s", required)
		}
	}
	selectStart := strings.Index(src, "AutomationElementCollection choices=scope.FindAll")
	choices := src[selectStart:]
	for _, required := range []string{"ControlType.Button", "ControlType.RadioButton", "ControlType.CheckBox", "Name(choice)==optionName&&choice.Current.IsEnabled", "Validate(h,expected,family);Invoke(target)"} {
		if !strings.Contains(choices, required) {
			t.Fatalf("semantic choice guard missing %s", required)
		}
	}
}

func TestNativeSelectedStateWithoutSelectedLabelKeepsConfigurationIdentity(t *testing.T) {
	m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	a, b := m.page(), m.page()
	walkNative(b.Regions["configuration-section"], func(n *nativeNode) {
		if nativeChoiceNode(n) && n.Selected {
			n.Name = nativeOptionKey(n.Name) + ". + $100.00. Selected"
		}
	})
	x, ex := nativeObservation(a, liveXPSURL, true)
	y, ey := nativeObservation(b, liveXPSURL, true)
	if ex != nil || ey != nil || x.Results[0].OfferID != y.Results[0].OfferID || nativeSignature(a, x) != nativeSignature(b, y) {
		t.Fatalf("choice price metadata became configuration identity: %v %v %v %v", ex, ey, x.Results, y.Results)
	}
	if y.Results[0].Price != 1000 || y.Results[0].CPU != "CPU A" || y.Results[0].Memory != "16 GB" {
		t.Fatalf("delta used as a total or corrupted a spec: %+v", y.Results[0])
	}
}

// No selection state is exposed on the expanded cards. Only collapsing the
// module makes the actual selected value available. A failed click must fail.
func TestNativeCoreScanConfirmsByExplicitCollapse(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			m := &collapsingChoiceModel{nativeChoiceModel: nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}, expanded: map[string]bool{"moduleCPU": false, "moduleMEM": false, "moduleSSD": false, "moduleSCREEN": false}}
			page := func() nativePage {
				p := m.page()
				walkNative(p.Regions["configuration-section"], func(n *nativeNode) {
					if nativeChoiceNode(n) && !strings.HasPrefix(n.ID, "label-") {
						n.Selected = false
						n.Enabled = true
						n.Name = nativeOptionKey(n.Name) + ". + $100.00"
					}
				})
				return p
			}
			collapses := 0
			b := newNormalBrowser()
			b.pollDelay = time.Millisecond
			b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
				switch r.Action {
				case "expand":
					m.expanded[r.GroupID] = true
				case "collapse":
					collapses++
					m.expanded[r.GroupID] = false
				case "select":
					if !stale {
						if _, err := m.nativeChoiceModel.call(ctx, r); err != nil {
							return nativePage{}, err
						}
					}
				}
				return page(), nil
			}
			rows, partial, err := b.scanCustomCore(context.Background(), page(), nativeRequest{Handle: 99, Owner: "collapse", URL: liveXPSURL, Family: true}, func(string) {})
			if stale {
				if err == nil || !partial || !strings.Contains(err.Error(), "切换未确认") {
					t.Fatalf("stale click accepted: rows=%d partial=%v err=%v", len(rows), partial, err)
				}
			} else if err != nil || partial || len(rows) < 7 || collapses == 0 {
				t.Fatalf("summary confirmation failed: rows=%d partial=%v collapses=%d err=%v", len(rows), partial, collapses, err)
			}
		})
	}
}
