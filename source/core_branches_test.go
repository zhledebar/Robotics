package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Reproduce V8.17's failure: all RAM cards look available, but Dell silently
// retains 32GB for a 16GB request on the third and subsequent processors.
type rejectedRAMModel struct {
	cpu, ram, ssd, screen string
	baseline              bool
	prematureRAM          bool
	rejected              int
	mutateCPU             bool
	denyRecovery          bool
	denied                bool
}

func (m *rejectedRAMModel) page() nativePage {
	root := &nativeNode{Kind: "Group", ID: "configuration-section"}
	expanded := true
	memoryChoices := []string{"16GB"}
	if m.cpu >= "CPU C" {
		memoryChoices = []string{"16GB", "32GB", "64GB"}
	}
	for _, g := range []struct {
		id, label, current string
		opts               []string
	}{
		{"moduleCPU", "Processor", m.cpu, []string{"CPU A", "CPU B", "CPU C", "CPU D", "CPU E"}},
		{"moduleRAM", "Memory", m.ram, memoryChoices},
		{"moduleSSD", "Storage", m.ssd, []string{"512GB", "1TB", "2TB", "4TB"}},
		{"moduleDISPLAY", "Displays", m.screen, []string{"LED", "OLED"}},
	} {
		scope := &nativeNode{Kind: "Group", ID: g.id}
		for _, name := range g.opts {
			suffix := ". + $100.00"
			if name == g.current {
				suffix = ". Selected"
			}
			scope.Children = append(scope.Children, &nativeNode{Kind: "Button", ID: g.id + "-" + strings.ReplaceAll(name, " ", ""), Name: name + suffix, Selected: name == g.current, Enabled: true})
		}
		root.Children = append(root.Children, &nativeNode{Kind: "Button", ID: "label-" + g.id, Name: g.label, Enabled: true, Expanded: &expanded}, scope)
	}
	price := 2000.0 + float64(m.cpu[len(m.cpu)-1]-'A')*200
	if m.ram == "64GB" {
		price += 700
	}
	if m.ssd == "4TB" {
		price += 900
	}
	if m.screen == "OLED" {
		price += 150
	}
	return nativePage{DOMConfiguration: true, Handle: 99, URL: liveXPSURL, CanOrdinary: true, Regions: map[string]*nativeNode{
		"configuration-section": root,
		"hero-section":          {Kind: "Group", Name: fmt.Sprintf("Dell Price $%.2f Estimated Value $%.2f Offer ID da16260_reg_01", price, price/0.8)},
		"add-to-cart-stack":     {Kind: "Group", Children: []*nativeNode{{Kind: "Button", Name: "Add to Cart", Enabled: true}}},
	}}
}
func (m *rejectedRAMModel) call(ctx context.Context, r nativeRequest) (nativePage, error) {
	if m.denied {
		return nativePage{}, fmt.Errorf("read continued after denial")
	}
	if r.Action == "select" {
		key := nativeOptionKey(r.OptionName)
		switch r.GroupID {
		case "moduleCPU":
			m.cpu = key
			m.ram = "16GB"
			if key >= "CPU C" {
				m.ram = "32GB"
			}
		case "moduleSSD":
			m.ssd = key
		case "moduleDISPLAY":
			m.screen = key
		case "moduleRAM":
			if !m.baseline {
				m.prematureRAM = true
			}
			if key == "16GB" && m.cpu >= "CPU C" {
				m.rejected++
			} else {
				m.ram = key
			}
			if m.mutateCPU && key == "64GB" {
				m.cpu = "CPU B"
				m.ram = "16GB"
			}
		}
	}
	p := m.page()
	if m.denyRecovery && m.rejected > 0 && r.Action == "snapshot" {
		p.Error = "网页访问被限制（access denied）"
		m.denied = true
	}
	return p, nil
}
func TestRejectedRAMContinuesOtherCPUsAndMemoryWithoutFalseCompletion(t *testing.T) {
	m := &rejectedRAMModel{cpu: "CPU A", ram: "16GB", ssd: "512GB", screen: "LED"}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.call = m.call
	rows, partial, err := b.scanCustomCore(context.Background(), m.page(), nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(s string) {
		if strings.Contains(s, "继续检查其他显卡及内存选项") {
			if !strings.Contains(s, "40 项") {
				t.Fatalf("baseline incomplete: %s", s)
			}
			m.baseline = true
		}
	})
	if err == nil || !partial || len(rows) != 64 || m.prematureRAM || m.rejected != 3 {
		t.Fatalf("rows=%d partial=%v err=%v model=%+v", len(rows), partial, err, m)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		cpu := nativeOptionKey(r.Options["moduleCPU"])
		ram := nativeOptionKey(r.Options["moduleRAM"])
		if seen[r.OfferID] {
			t.Fatal("duplicate row")
		}
		seen[r.OfferID] = true
		if cpu >= "CPU C" && ram == "16GB" {
			t.Fatalf("rejected RAM accepted: %+v", r)
		}
	}
	if !strings.Contains(err.Error(), "3 个分支未确认") {
		t.Fatal(err)
	}
}
func TestLaterSelectionChangingCPUIsRestoredBeforeNextSibling(t *testing.T) {
	m := &rejectedRAMModel{cpu: "CPU A", ram: "16GB", ssd: "512GB", screen: "LED", mutateCPU: true}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.call = m.call
	rows, partial, err := b.scanCustomCore(context.Background(), m.page(), nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(s string) {
		if strings.Contains(s, "继续检查其他显卡及内存选项") {
			m.baseline = true
		}
	})
	if err == nil || !partial || len(rows) != 40 || !strings.Contains(err.Error(), "未确认") {
		t.Fatalf("rows=%d partial=%v err=%v", len(rows), partial, err)
	}
	for _, r := range rows {
		if nativeOptionKey(r.Options["moduleRAM"]) == "64GB" {
			t.Fatal("changed CPU mislabeled as requested combination")
		}
	}
}
func TestDeniedRecoveryStopsRemainingBranches(t *testing.T) {
	m := &rejectedRAMModel{cpu: "CPU A", ram: "16GB", ssd: "512GB", screen: "LED", denyRecovery: true}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.call = m.call
	rows, partial, err := b.scanCustomCore(context.Background(), m.page(), nativeRequest{Handle: 99, Owner: "test", URL: liveXPSURL, Family: true}, func(s string) {
		if strings.Contains(s, "继续检查其他显卡及内存选项") {
			m.baseline = true
		}
	})
	if err == nil || !partial || len(rows) != 40 || !strings.Contains(err.Error(), "access denied") || !m.denied {
		t.Fatalf("rows=%d partial=%v err=%v", len(rows), partial, err)
	}
}
