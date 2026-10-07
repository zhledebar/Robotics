package main

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/net/websocket"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeScopedReadingViewRescuesMissingPriceNodes(t *testing.T) {
	p := ownUnlabeledCustomPage()
	p.Regions["hero-section"] = &nativeNode{ReadingText: "XPS 16 Laptop\n$\n2,399.99\nFree Shipping\n$200/mo\nOffer ID\nda16260_reg_01"}
	p.Regions["add-to-cart-stack"] = &nativeNode{ReadingText: "$2,399.99\nFree Shipping\n$200/mo\nAdd to Cart"}
	o, e := nativeObservation(p, liveXPSURL, true)
	if e != nil || len(o.Results) != 1 || o.Price != 2399.99 || !o.Results[0].Custom || o.Results[0].DiscountConfirmed || !o.Results[0].ConfigUnbound {
		t.Fatalf("scoped text rejected or invented discount: %+v %v", o, e)
	}
	for _, bad := range []string{"$2,999.99\nAdd to Cart", "$2,399.99\n$3,999.99\nAdd to Cart", "Starting at\n$2,399.99\nAdd to Cart", "Starting at\n$\n2,399.99\nAdd to Cart", "$2,399.99\n/mo\nAdd to Cart"} {
		p.Regions["add-to-cart-stack"].ReadingText = bad
		if strings.Contains(bad, "3,999") {
			p.Regions["hero-section"].ReadingText += "\n$3,999.99"
		}
		if _, e = nativeObservation(p, liveXPSURL, true); e == nil {
			t.Fatalf("ambiguous or finance text accepted: %q", bad)
		}
		p.Regions["hero-section"].ReadingText = "XPS 16 Laptop\n$2,399.99\nOffer ID da16260_reg_01"
	}
}

func TestNativeSplitCurrencyChildrenAndLabeledReadingPrice(t *testing.T) {
	p := ownUnlabeledCustomPage()
	for _, id := range []string{"hero-section", "add-to-cart-stack"} {
		at := 0
		if id == "hero-section" {
			at = 1
		}
		p.Regions[id].Children[at] = &nativeNode{Kind: "Group", Children: []*nativeNode{{Kind: "Text", Name: "$"}, {Kind: "Text", Name: "2,399.99"}}}
	}
	if price, _ := nativeCustomPrice(p); price != 2399.99 {
		t.Fatalf("split currency lost: %v", price)
	}
	p.Regions["hero-section"].ReadingText = "Dell Price\n$2,399.99\nEstimated Value\n$2,899.99"
	if price, original := nativeCustomPrice(p); price != 2399.99 || original != 2899.99 {
		t.Fatalf("reading label lost: %v/%v", price, original)
	}
	p.Regions["hero-section"].ReadingText += "\nDell Price $1,999.99"
	if price, _ := nativeCustomPrice(p); price != 0 {
		t.Fatal("reading label conflict accepted")
	}
}

func TestNativeReadingFailureHasActionableReason(t *testing.T) {
	p := ownUnlabeledCustomPage()
	delete(p.Regions, "hero-section")
	_, e := nativeObservation(p, liveXPSURL, true)
	if e == nil || !strings.Contains(e.Error(), "商品主报价区未加载") {
		t.Fatalf("missing region obscured: %v", e)
	}
}

func TestNativeSplitMoneyNeverCollapsesFinanceParent(t *testing.T) {
	p := ownUnlabeledCustomPage()
	p.Regions["hero-section"] = &nativeNode{Kind: "Group", Children: []*nativeNode{{Kind: "Text", Name: "Offer ID da16260_reg_01"}, {Kind: "Group", Name: "Monthly financing", Children: []*nativeNode{{Kind: "Text", Name: "$200"}}}}}
	p.Regions["add-to-cart-stack"] = &nativeNode{Kind: "Group", Children: []*nativeNode{{Kind: "Group", Name: "Monthly financing", Children: []*nativeNode{{Kind: "Text", Name: "$200"}}}}}
	if price, _ := nativeCustomPrice(p); price != 0 {
		t.Fatalf("finance parent collapsed into total: %v", price)
	}
}

func TestManagedReaderScopedTextContract(t *testing.T) {
	raw, e := os.ReadFile("normal_reader.ps1")
	if e != nil {
		t.Fatal(e)
	}
	s := string(raw)
	for _, require := range []string{"lookup.TreeFilter=Automation.RawViewCondition", "using(lookup.Activate())", "TextPattern.Pattern", "RangeFromChild(region)", "range.GetText(32769)", "text.Length>32768", "snapshot.reading_text=RegionText(doc,region)"} {
		if !strings.Contains(s, require) {
			t.Fatalf("missing %s", require)
		}
	}
	if strings.Contains(s, ".DocumentRange") {
		t.Fatal("unscoped whole-page text used as own price")
	}
}

func TestBrowserNavigationExceptionsRecoverOnSameConnection(t *testing.T) {
	body, err := os.ReadFile("testdata/lenovo_vip_p1_rendered.html")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	reads := 0
	mux.Handle("/ws", websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		for {
			var req struct {
				ID     int            `json:"id"`
				Params map[string]any `json:"params"`
			}
			if websocket.JSON.Receive(ws, &req) != nil {
				return
			}
			expr, _ := req.Params["expression"].(string)
			value := vipP1Family
			if expr == browserHTMLExpr {
				reads++
				if reads == 1 {
					websocket.JSON.Send(ws, map[string]any{"id": req.ID, "error": map[string]any{"message": "Execution context was destroyed"}})
					continue
				}
				value = string(body)
				if reads == 2 {
					value = ""
				}
			}
			websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": value}}})
		}
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ws, e := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "", "http://localhost")
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, e := waitBrowserPage(ctx, &cdpClient{ws: ws}, vipP1Family)
	if e != nil || result != string(body) {
		t.Fatalf("navigation/null root did not recover to verified member quote: %v", e)
	}
}

// Real local websocket transport, with an initial navigation exception followed
// by stable member HTML. This models CDP messages, not a Windows browser run.
func TestBrowserReadOnlyEvalGuardAndPendingClassification(t *testing.T) {
	mux := http.NewServeMux()
	calls := 0
	mux.Handle("/ws", websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		for {
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if websocket.JSON.Receive(ws, &req) != nil {
				return
			}
			calls++
			expr, _ := req.Params["expression"].(string)
			if calls == 1 {
				if expr != browserHTMLExpr {
					t.Errorf("HTML read not guarded: %q", expr)
				}
				websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"type": "object"}, "exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "TypeError: documentElement is null"}}}})
			} else {
				websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": "next loaded body"}}})
			}
		}
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ws, e := websocket.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", "", "http://localhost")
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	c := &cdpClient{ws: ws}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, e = c.eval(ctx, browserHTMLExpr)
	if e == nil || !strings.Contains(e.Error(), "浏览器页面暂未就绪") {
		t.Fatalf("navigation exception opaque: %v", e)
	}
	result, e := c.eval(ctx, browserHTMLExpr)
	if e != nil || result != "next loaded body" {
		t.Fatalf("did not recover: %q %v", result, e)
	}
	raw := "https://www.lenovo.com/us/vipmembers/perksoffer/en/p/21ql0021us"
	detail := "页面解析：会员当前售价未确认；采集浏览器：" + (&browserPagePending{Reason: "网页正在切换"}).Error()
	if actionForFailure(raw, detail) != "" {
		t.Fatal("transient read permanently paused")
	}
	for _, hard := range []string{"网页访问被限制", "会员商品网址跳转到非会员页面", "登录验证", "采集浏览器启动失败"} {
		if actionForFailure(raw, detail+"；"+hard) == "" {
			t.Fatalf("hard failure hidden: %s", hard)
		}
	}
}

func TestLenovoOldScriptPauseRecoveredOnUpgrade(t *testing.T) {
	dir := t.TempDir()
	p := product("p14s")
	p.URL = "https://www.lenovo.com/us/vipmembers/perksoffer/en/p/21ql0021us"
	p.LastError = "页面解析：会员当前售价未确认；采集浏览器：网页脚本执行失败"
	p.NeedsAction = "会员报价未确认；自动检查已暂停"
	p.LastPrice = 2259
	p.LastTrusted = true
	b, _ := json.Marshal(Store{Schema: 84, Products: []*Product{p}})
	os.WriteFile(filepath.Join(dir, "monitor_data.json"), b, 0600)
	app, e := newApp(dir, true)
	if e != nil {
		t.Fatal(e)
	}
	defer app.shutdown()
	q := app.store.Products[0]
	if q.NeedsAction != "" || !q.Stale || q.LastTrusted || q.LastPrice != 2259 || !q.Active {
		t.Fatalf("not recovered conservatively: %+v", q)
	}
	p.LastError += "；网页访问被限制"
	b, _ = json.Marshal(Store{Schema: 84, Products: []*Product{p}})
	os.WriteFile(filepath.Join(dir, "monitor_data.json"), b, 0600)
	app2, e := newApp(dir, true)
	if e != nil {
		t.Fatal(e)
	}
	defer app2.shutdown()
	if app2.store.Products[0].NeedsAction == "" {
		t.Fatal("blocked page reopened automatically")
	}
}

// An actual owned OS child is terminated after a headless collection. The local
// CDP server models browser responses; this is not native Windows acceptance.
func TestHeadlessBrowserProcessReleasedAfterCheck(t *testing.T) {
	if os.Getenv("PRICE_MONITOR_TEST_CHILD") == "owned-headless-sleeper" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	body, e := os.ReadFile("testdata/lenovo_vip_p1_rendered.html")
	if e != nil {
		t.Fatal(e)
	}
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprintf("callback_failure_%v", failed), func(t *testing.T) {
			mux := http.NewServeMux()
			var srv *httptest.Server
			mux.HandleFunc("/json/version", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, map[string]string{"Browser": "local-test"}) })
			mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) {
				jsonReply(w, map[string]string{"id": "owned-tab", "webSocketDebuggerUrl": "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"})
			})
			mux.Handle("/ws", websocket.Handler(func(ws *websocket.Conn) {
				defer ws.Close()
				for {
					var r struct {
						ID     int            `json:"id"`
						Params map[string]any `json:"params"`
					}
					if websocket.JSON.Receive(ws, &r) != nil {
						return
					}
					value := string(body)
					if r.Params["expression"] == "location.href" {
						value = vipP1Family
					}
					websocket.JSON.Send(ws, map[string]any{"id": r.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": value}}})
				}
			}))
			srv = httptest.NewServer(mux)
			defer srv.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestHeadlessBrowserProcessReleasedAfterCheck$")
			cmd.Env = append(os.Environ(), "PRICE_MONITOR_TEST_CHILD=owned-headless-sleeper")
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			defer killOwned(cmd)
			session := &browserSession{cmd: cmd, port: strings.TrimPrefix(srv.URL, "http://127.0.0.1:"), done: make(chan struct{})}
			go func() { session.waitErr = cmd.Wait(); close(session.done) }()
			manager := newBrowserManager(t.TempDir())
			manager.sessions["lenovo"] = session
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			_, err := manager.read(ctx, vipP1Family, func(c *cdpClient, html string) (Observation, error) {
				if failed {
					return Observation{}, fmt.Errorf("callback failed")
				}
				return parseLenovo(html, vipP1Family)
			})
			if (err != nil) != failed {
				t.Fatalf("unexpected read result: %v", err)
			}
			if len(manager.sessions) != 0 {
				t.Fatal("idle headless session still retained")
			}
			select {
			case <-session.done:
				if session.waitErr == nil {
					t.Fatal("owned child exited normally rather than being reclaimed")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("owned child still running after check")
			}
		})
	}
}
