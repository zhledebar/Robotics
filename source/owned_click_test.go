package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type clickTransportModel struct {
	mode    string
	actions []string
	plans   int
}

func (m *clickTransportModel) eval(ctx context.Context, expr string) (string, error) {
	m.plans++
	hit := strings.Contains(expr, `"dom_phase":"hit"`)
	if hit && m.mode == "blocked-after-hover" {
		return "", errors.New("核心选项被其他控件遮挡")
	}
	p := map[string]any{"url": liveXPSURL, "groupID": "moduleCPU", "optionID": "CPU-B", "targetID": "CPU-B", "selectedIDs": []string{"CPU-A"}, "already": m.mode == "already", "point": map[string]float64{"x": 50, "y": 40}}
	if m.mode == "already" {
		p["selectedIDs"] = []string{"CPU-B"}
	}
	if m.mode == "wrong-identity" {
		p["targetID"] = "AddToCart"
	}
	if hit && m.mode == "moved-after-hover" {
		p["point"] = map[string]float64{"x": 100, "y": 40}
	}
	raw, _ := json.Marshal(p)
	return string(raw), nil
}
func (m *clickTransportModel) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if method != "Input.dispatchMouseEvent" {
		return nil, fmt.Errorf("unexpected method %s", method)
	}
	v := params.(map[string]any)
	m.actions = append(m.actions, v["type"].(string))
	if m.mode == "press-response-error" && v["type"] == "mousePressed" {
		return nil, errors.New("press response failed")
	}
	if v["x"] != float64(50) || v["y"] != float64(40) {
		return nil, errors.New("wrong click position")
	}
	return json.RawMessage(`{}`), nil
}
func TestOwnedCoreBrowserClickOnlyAfterTargetHitCheck(t *testing.T) {
	for _, mode := range []string{"valid", "already", "wrong-identity", "blocked-after-hover", "moved-after-hover", "press-response-error"} {
		t.Run(mode, func(t *testing.T) {
			m := &clickTransportModel{mode: mode}
			err := ownedDOMOperation(context.Background(), m, nativeRequest{Action: "select", URL: liveXPSURL, Family: true, GroupID: "moduleCPU", OptionID: "CPU-B", OptionName: "CPU B. + $100.00"})
			actual := strings.Join(m.actions, ",")
			switch mode {
			case "valid":
				if err != nil || actual != "mouseMoved,mousePressed,mouseReleased" || m.plans != 2 {
					t.Fatalf("input not verified: %s plans=%d err=%v", actual, m.plans, err)
				}
			case "already":
				if err != nil || actual != "" {
					t.Fatal("clicked already selected option")
				}
			case "wrong-identity":
				if err == nil || actual != "" {
					t.Fatal("foreign target clicked")
				}
			case "press-response-error":
				if err == nil || actual != "mouseMoved,mousePressed,mouseReleased" {
					t.Fatal("pressed button not released after response failure")
				}
			default:
				if err == nil || actual != "mouseMoved" {
					t.Fatalf("unsafe point pressed: %s %v", actual, err)
				}
			}
		})
	}
}

func TestOwnedCoreCanceledOperationDoesNotClick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := &clickTransportModel{}
	err := ownedDOMOperation(ctx, m, nativeRequest{Action: "select", URL: liveXPSURL, Family: true, GroupID: "moduleCPU", OptionID: "CPU-B"})
	if !errors.Is(err, context.Canceled) || m.plans != 0 || len(m.actions) != 0 {
		t.Fatal("canceled action reached browser")
	}
}

func TestOwnedDOMScriptRetainsDenialAndOwnershipReason(t *testing.T) {
	for _, message := range []string{"网页访问被限制（access denied）", "点击前商品网址已变化", "核心选项被其他控件遮挡；没有点击"} {
		t.Run(message, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "Error: " + message + "\n    at <anonymous>:1:1"}})
			err := ownedDOMScriptFailure(raw)
			if !strings.Contains(err.Error(), message) {
				t.Fatal("specific failure hidden")
			}
			if strings.Contains(message, "网址") && !strings.Contains(err.Error(), "窗口") {
				t.Fatal("ownership change became ordinary loading failure")
			}
			if strings.Contains(message, "access denied") && !hardBrowserContentFailure(err.Error()) {
				t.Fatal("denial became ordinary loading failure")
			}
		})
	}
}

func TestNativeDOMPendingSelectionDoesNotCollapseModule(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprint(stale), func(t *testing.T) {
			m := &nativeChoiceModel{cpu: "CPU A", ssd: "SSD S", display: "LED", view: "custom"}
			page := func() nativePage {
				p := m.page()
				p.DOMConfiguration = true
				for _, g := range nativeCoreGroups(p) {
					for _, o := range g.Options {
						o.ID = g.ID + "-" + nativeOptionKey(o.Name)
					}
				}
				return p
			}
			b := newNormalBrowser()
			b.pollDelay = time.Millisecond
			var pending *nativeRequest
			reads, collapses := 0, 0
			b.call = func(ctx context.Context, r nativeRequest) (nativePage, error) {
				if r.Action == "collapse" {
					collapses++
					return page(), errors.New("collapsing cancels selection")
				}
				if r.Action == "select" && r.GroupID == "moduleCPU" {
					copy := r
					pending = &copy
					reads = 0
					return page(), nil
				}
				if r.Action == "snapshot" && pending != nil {
					reads++
					if !stale && reads >= 2 {
						if _, err := m.call(ctx, *pending); err != nil {
							return page(), err
						}
						pending = nil
					}
				}
				if r.Action == "select" {
					if _, err := m.call(ctx, r); err != nil {
						return page(), err
					}
				}
				return page(), nil
			}
			rows, partial, err := b.scanCustomCore(context.Background(), page(), nativeRequest{Handle: 99, URL: liveXPSURL, Family: true}, func(string) {})
			if collapses != 0 {
				t.Fatal("DOM-confirmed module collapsed while selection pending")
			}
			if stale {
				if err == nil || !partial {
					t.Fatal("ignored selection accepted")
				}
				for _, r := range rows {
					if r.CPU != "CPU A" {
						t.Fatal("old CPU attributed to requested CPU")
					}
				}
			} else if err != nil || partial || len(rows) != 7 {
				t.Fatalf("delayed selection not completed: %d %v %v", len(rows), partial, err)
			}
		})
	}
}

func TestDiagnosticDownloadIncludesCurrentSelectionFailureAndBoundedLog(t *testing.T) {
	a := testApp(t)
	p := product("diagnostic")
	p.LastError = "核心选项切换未确认（处理器）"
	a.store.Products = append(a.store.Products, p)
	log := strings.Repeat("x", 2*1024*1024+100) + "\nowned core click wanted-id=CPU-B selected-ids=CPU-A\n"
	if err := os.WriteFile(filepath.Join(filepath.Dir(a.file), "app_v826.log"), []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1:38840/api/diagnostics", nil)
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(w.Body.String(), "核心选项切换未确认") || !strings.Contains(w.Body.String(), "wanted-id=CPU-B") || w.Body.Len() > 2*1024*1024+4096 {
		t.Fatal("diagnostic missing failure or unbounded")
	}
	req.Header.Set("Origin", "https://example.com")
	w = httptest.NewRecorder()
	a.handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-site diagnostic read allowed")
	}
}
