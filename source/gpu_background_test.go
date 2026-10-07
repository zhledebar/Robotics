package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGPUObservedDellAndLenovo(t *testing.T) {
	for _, tc := range []struct {
		file, url, gpu string
		native         bool
	}{
		{"testdata/dell_xps_feed_offers_live_20261005.html", liveXPSURL, "Intel", true},
		{"testdata/lenovo_vip_p1_rendered.html", vipP1Family, "NVIDIA", false},
		{"testdata/lenovo_vip_p14s_sku_rendered.html", observedP14sSKU, "Radeon", false},
		{"testdata/lenovo_vip_p1_cto_rendered.html", p1CTOURL, "NVIDIA", false},
	} {
		t.Run(tc.file, func(t *testing.T) {
			body, e := os.ReadFile(tc.file)
			if e != nil {
				t.Fatal(e)
			}
			var o Observation
			if tc.native {
				o, e = parseDell(string(body), tc.url, true)
			} else {
				o, e = parseLenovo(string(body), tc.url)
			}
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, r := range o.Results {
				if strings.Contains(r.GPU, tc.gpu) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing GPU %q in own rows: %+v", tc.gpu, o.Results)
			}
			encoded, _ := json.Marshal(o.Results)
			var rows []DellResult
			if e = json.Unmarshal(encoded, &rows); e != nil {
				t.Fatal(e)
			}
			if rows[0].GPU != o.Results[0].GPU {
				t.Fatal("GPU lost in persistence")
			}
		})
	}
	page := capturedNative(t, false)
	o, e := nativeObservation(page, liveXPSURL, true)
	if e != nil || len(o.Results) != 2 {
		t.Fatalf("native ordinary rows: %+v %v", o, e)
	}
	for _, r := range o.Results {
		if !strings.Contains(r.GPU, "Intel") {
			t.Fatalf("native GPU missing: %+v", r)
		}
	}
	custom := capturedNative(t, true)
	o, e = nativeObservation(custom, liveXPSURL, true)
	if e != nil || o.Results[0].GPU == "" {
		t.Fatalf("native default custom GPU missing: %+v %v", o, e)
	}
}

func TestGPUCoreDependentCombinationsHaveSeparateIdentity(t *testing.T) {
	model := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
	gpu := "Integrated"
	makePage := func() nativePage {
		p := model.page()
		root := p.Regions["configuration-section"]
		expanded := true
		root.Children = append(root.Children, &nativeNode{Kind: "Button", ID: "label-moduleGPU", Name: "Graphics Card", Expanded: &expanded, Enabled: true})
		group := &nativeNode{Kind: "Group", ID: "moduleGPU"}
		for _, name := range []string{"Integrated", "RTX Pro 8 GB"} {
			selected := name == gpu
			label := name + ". + $100.00"
			if selected {
				label = name + ". Selected"
			}
			group.Children = append(group.Children, &nativeNode{Kind: "Button", Name: label, Selected: selected, Enabled: !selected})
		}
		root.Children = append(root.Children, group)
		return p
	}
	b := newNormalBrowser()
	b.pollDelay = time.Millisecond
	b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "select" && r.GroupID == "moduleGPU" {
			gpu = nativeOptionKey(r.OptionName)
		} else {
			_, e := model.call(ctx, r)
			if e != nil {
				return nativePage{}, e
			}
			if r.Action == "select" && r.GroupID == "moduleCPU" {
				gpu = "Integrated"
			}
		}
		return makePage(), nil
	}
	rows, partial, e := b.scanCustomCore(context.Background(), makePage(), nativeRequest{Handle: 99, Owner: "gpu", URL: liveXPSURL, Family: true}, func(string) {})
	if e != nil || partial || len(rows) != 14 {
		t.Fatalf("GPU enumeration: rows=%d partial=%v %v", len(rows), partial, e)
	}
	by := map[string]map[string]string{}
	for _, r := range rows {
		if r.GPU == "" {
			t.Fatalf("GPU omitted: %+v", r)
		}
		key := r.CPU + "|" + r.Storage + "|" + r.Display
		if by[key] == nil {
			by[key] = map[string]string{}
		}
		by[key][r.GPU] = r.OfferID
	}
	for key, variants := range by {
		if len(variants) != 2 || variants["Integrated"] == variants["RTX Pro 8 GB"] {
			t.Fatalf("GPU collapsed at %s: %v", key, variants)
		}
	}
}

func TestBackgroundReaderHasNoForegroundFallback(t *testing.T) {
	read := func(file string) string {
		b, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	launch := read("background_desktop_windows.go")
	script := read("normal_reader.ps1")
	entry := read("normal_reader_windows.go")
	for _, api := range []string{"SwitchDesktop", "SetForegroundWindow", "SetCursorPos", "SendInput", "ShowWindowAsync"} {
		if strings.Contains(script, api+"(") {
			t.Fatalf("reader foreground API %s", api)
		}
		if strings.Contains(launch, "NewProc(\""+api+"\")") {
			t.Fatalf("launcher foreground API %s", api)
		}
	}
	for _, s := range []string{"Desktop: dp", "AssignProcessToJobObject", "0x20002", "JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE", "PRICE_MONITOR_PRIVATE_DESKTOP="} {
		if !strings.Contains(launch, s) {
			t.Fatalf("missing isolation mechanism %s", s)
		}
	}
	if !strings.Contains(entry, "runDesktopCommand(child, path, args, env, nativeScript, token)") || strings.Contains(entry, "cmd.Run()") {
		t.Fatal("reader can launch on default desktop")
	}
	for _, s := range []string{"PrivateDesktop();", "--user-data-dir=", "--disable-background-mode", "--force-renderer-accessibility", "AccessibleObjectFromWindow(ownedHandle", "matches.Count!=1", "accDoDefaultAction(target.child)", "SelectionItemPattern.Pattern", "SameBounds(target.obj"} {
		if !strings.Contains(script, s) {
			t.Fatalf("missing background compatibility guard %s", s)
		}
	}
}
