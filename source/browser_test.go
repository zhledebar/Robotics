package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// A dependent configurator verifies the scanner skips impossible combinations,
// retains CPU-dependent memory, and reads each final price instead of adding deltas.
func TestCDPDependentConfigurationTraversal(t *testing.T) {
	state := map[string]string{"CPU": "CPU-a", "SSD": "SSD-s", "SCREEN": "SCREEN-l"}
	body := func() string {
		price := 1000
		memory := "16 GB"
		if state["CPU"] == "CPU-b" {
			price += 200
			memory = "32 GB"
		}
		if state["SSD"] == "SSD-b" {
			price += 100
		}
		if state["SCREEN"] == "SCREEN-o" {
			price += 50
		}
		var b strings.Builder
		fmt.Fprintf(&b, `<html><div class="hero-section"><span class="sale-price">$%d.00</span></div><div class="add-to-cart-stack"><button>Add to Cart</button></div>`, price)
		for _, g := range []struct {
			id, label string
			options   []string
		}{{"CPU", "Processor", []string{"CPU-a", "CPU-b"}}, {"MEM", "Memory", []string{"MEM-" + memory}}, {"SSD", "Storage", []string{"SSD-s", "SSD-b"}}, {"SCREEN", "Displays", []string{"SCREEN-l", "SCREEN-o"}}} {
			fmt.Fprintf(&b, `<div data-module-id="%s" role="group" aria-label="%s">`, g.id, g.label)
			for _, id := range g.options {
				selected := state[g.id] == id || g.id == "MEM"
				disabled := state["CPU"] == "CPU-a" && state["SSD"] == "SSD-s" && id == "SCREEN-o"
				fmt.Fprintf(&b, `<div data-option-id="%s" data-is-selected="%v" aria-disabled="%v" data-status="available" aria-label="%s"><span data-test-id="option-title">%s</span></div>`, id, selected, disabled, id, id)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</html>`)
		return b.String()
	}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Error("target creation requires PUT")
		}
		jsonReply(w, map[string]string{"id": "one", "webSocketDebuggerUrl": "ws" + strings.TrimPrefix(srv.URL, "http") + "/devtools"})
	})
	mux.Handle("/devtools", websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		for {
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if e := websocket.JSON.Receive(ws, &req); e != nil {
				return
			}
			expr, _ := req.Params["expression"].(string)
			result := ""
			if expr == browserHTMLExpr {
				result = body()
			} else {
				re := regexp.MustCompile(`dataset\.optionId===("[^"]+")`)
				m := re.FindStringSubmatch(expr)
				if len(m) != 2 {
					t.Error("selection expression missing option id")
					return
				}
				var id string
				json.Unmarshal([]byte(m[1]), &id)
				parts := strings.SplitN(id, "-", 2)
				if len(parts) == 2 {
					state[parts[0]] = id
				}
				result = "clicked"
			}
			_ = websocket.JSON.Send(ws, map[string]any{"method": "Page.lifecycleEvent", "params": map[string]any{}})
			if e := websocket.JSON.Send(ws, map[string]any{"id": req.ID, "result": map[string]any{"result": map[string]any{"type": "string", "value": result}}}); e != nil {
				return
			}
		}
	}))
	srv = httptest.NewServer(mux)
	defer srv.Close()
	s := &browserSession{port: strings.TrimPrefix(srv.URL, "http://127.0.0.1:")}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	c, _, e := openTarget(ctx, s, "https://www.dell.com/spd/test/a_reg_1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.ws.Close()
	initial, e := c.eval(ctx, browserHTMLExpr)
	if e != nil {
		t.Fatal(e)
	}
	rs, partial, e := scanBYO(ctx, c, initial, "https://www.dell.com/spd/test/a_reg_1", func(string) {})
	if e != nil || partial || len(rs) != 7 {
		t.Fatalf("count=%d partial=%v err=%v", len(rs), partial, e)
	}
	for _, r := range rs {
		if !r.Confirmed || r.Price <= 0 {
			t.Fatalf("unconfirmed row %+v", r)
		}
		if r.CPU == "CPU-b" && r.Memory != "MEM-32 GB" {
			t.Error("CPU-dependent memory was not refreshed")
		}
		if r.CPU == "CPU-a" && r.Storage == "SSD-s" && r.Display == "SCREEN-o" {
			t.Error("incompatible combination was fabricated")
		}
	}
}
