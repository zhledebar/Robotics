package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

//go:embed owned_dom.js
var ownedDOMScript string

func ownedDOMScriptFailure(raw json.RawMessage) error {
	var ex struct {
		Text      string `json:"text"`
		Exception struct {
			Description string `json:"description"`
		} `json:"exception"`
	}
	if err := json.Unmarshal(raw, &ex); err != nil {
		return fmt.Errorf("网页配置脚本返回异常；详细原因已记入程序日志")
	}
	message := strings.TrimSpace(strings.Split(ex.Exception.Description, "\n")[0])
	message = strings.TrimPrefix(message, "Error: ")
	if message == "" {
		message = ex.Text
	}
	if message == "" {
		message = "网页配置脚本返回异常"
	}
	if strings.Contains(message, "商品网址已变化") {
		return fmt.Errorf("普通浏览器窗口或机型已变化：%s", message)
	}
	return fmt.Errorf("网页配置操作未完成：%s", message)
}

func ownedDOMExpression(r nativeRequest, action string) string {
	r.Action = action
	b, _ := json.Marshal(r)
	return "(" + ownedDOMScript + ")(" + string(b) + ")"
}

// Only the single current page in the application's private browser qualifies.
// Never create a target, attach another tab, or accept a non-loopback endpoint.
func ownedDOMTarget(ctx context.Context, port, raw string, family bool) (*cdpClient, error) {
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil, fmt.Errorf("后台网页端口无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/json/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("后台网页接口 HTTP %d", resp.StatusCode)
	}
	var targets []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1024*1024)).Decode(&targets); err != nil {
		return nil, err
	}
	pages := 0
	endpoint := ""
	for _, t := range targets {
		if t.Type == "page" {
			pages++
			if normalURLMatches(raw, t.URL, family) {
				endpoint = t.WS
			}
		}
	}
	if pages != 1 || endpoint == "" {
		return nil, fmt.Errorf("后台商品标签页不唯一或网址已变化")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "ws" || u.Host != "127.0.0.1:"+port || !strings.HasPrefix(u.Path, "/devtools/page/") || u.User != nil {
		return nil, fmt.Errorf("后台网页连接归属未核对")
	}
	cfg, err := websocket.NewConfig(endpoint, "http://localhost")
	if err != nil {
		return nil, err
	}
	cfg.Dialer = nil
	conn, err := cfg.DialContext(ctx)
	if err != nil {
		// websocket.DialError does not unwrap context errors. Keep the
		// caller's cancellation/deadline visible to the retry policy.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return &cdpClient{ws: conn}, nil
}

func enrichOwnedDOM(ctx context.Context, c *cdpClient, p nativePage, r nativeRequest) (nativePage, error) {
	// Ordinary offer cards and actual quote regions retain the existing UIA
	// parser. DOM supplies configuration identity and selection, not prices.
	if nativeView(p) != "custom" {
		return p, nil
	}
	s, err := c.eval(ctx, ownedDOMExpression(r, "snapshot"))
	if err != nil {
		return p, err
	}
	return applyOwnedDOMSnapshot(p, r, s)
}

func applyOwnedDOMSnapshot(p nativePage, r nativeRequest, s string) (nativePage, error) {
	var result struct {
		URL           string          `json:"url"`
		Configuration *nativeNode     `json:"configuration"`
		Price         float64         `json:"price"`
		OfferID       string          `json:"offerID"`
		Stock         string          `json:"stock"`
		StockOfferID  string          `json:"stockOfferID"`
		Pending       bool            `json:"pending"`
		Reason        string          `json:"reason"`
		Diagnostic    json.RawMessage `json:"diagnostic"`
	}
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return p, err
	}
	if !normalURLMatches(r.URL, result.URL, r.Family) || result.URL != p.URL {
		return p, fmt.Errorf("后台配置快照与报价网址未对应")
	}
	if result.Pending {
		return p, &ownedDOMPending{Reason: result.Reason, Diagnostic: string(result.Diagnostic)}
	}
	if result.Configuration == nil {
		return p, fmt.Errorf("后台配置快照缺少配置区域")
	}
	price, _ := nativeCustomPrice(p)
	// DOM availability is accepted only for the single current custom Offer,
	// never for a preconfigured card or a different cached purchase button.
	stock := ""
	ids := nativeOfferIDs(p.Regions["hero-section"])
	if result.Stock == stockIn && len(ids) == 1 && result.OfferID == ids[0] && result.StockOfferID == ids[0] {
		stock = stockIn
	}
	if !(result.Price > 0) || math.IsNaN(result.Price) || math.IsInf(result.Price, 0) {
		return p, &ownedDOMPending{Reason: "网页当前整机总价无效；未接受报价"}
	}
	if price != result.Price {
		candidate := p
		candidate.Regions = make(map[string]*nativeNode, len(p.Regions))
		for id, node := range p.Regions {
			candidate.Regions[id] = node
		}
		candidate.Regions["configuration-section"] = result.Configuration
		candidate.DOMConfiguration = true
		candidate.DOMQuote = &nativeDOMQuote{Price: result.Price}
		candidate.DOMStock = stock
		candidate.Diagnostic = string(result.Diagnostic)
		pending := &ownedDOMPending{Reason: "网页配置快照与当前整机报价不同步", Diagnostic: fmt.Sprintf("UIA=%g DOM=%g; %s", price, result.Price, result.Diagnostic)}
		// A still-readable UIA quote and the same current custom Offer ID remain
		// required. No fallback for loading, conflicting visible totals or errors.
		ids := nativeOfferIDs(p.Regions["hero-section"])
		groups := nativeCoreGroups(candidate)
		coreBound := len(groups) > 0
		labels := map[string]bool{}
		for _, g := range groups {
			labels[g.Label] = true
			selected := 0
			for _, o := range g.Options {
				if o.Selected && o.ID != "" && nativeOptionKey(o.Name) != "" {
					selected++
				}
			}
			if selected != 1 {
				coreBound = false
			}
		}
		coreBound = coreBound && labels["processor"] && labels["storage"] && (labels["display"] || labels["displays"])
		if price > 0 && coreBound && len(ids) == 1 && result.OfferID == ids[0] {
			if _, err := nativeObservation(candidate, r.URL, r.Family); err == nil {
				pending.Candidate = &candidate
				pending.UIAPrice = price
			}
		}
		return p, pending
	}
	p.Regions["configuration-section"] = result.Configuration
	p.DOMConfiguration = true
	p.DOMStock = stock
	p.Diagnostic = string(result.Diagnostic)
	return p, nil
}

type ownedDOMPending struct {
	Reason, Diagnostic string
	Candidate          *nativePage
	UIAPrice           float64
}

func (e *ownedDOMPending) Error() string { return e.Reason }

// Retry reads of the same owned page. Never repeat a click or refresh to recover
// from loading, and never return the pre-loading quote as a successful read.
func waitOwnedDOM(ctx context.Context, p nativePage, enrich func(context.Context, nativePage) (nativePage, error), snapshot func(context.Context, nativePage) (nativePage, error), delay time.Duration) (nativePage, error) {
	reason := ""
	lastCandidate := ""
	candidateReads := 0
	for {
		updated, err := enrich(ctx, p)
		if err == nil {
			return updated, nil
		}
		var pending *ownedDOMPending
		if !errors.As(err, &pending) {
			if reason != "" && errors.Is(err, context.DeadlineExceeded) {
				return p, &nativeQuoteFailure{Reason: "浏览器页面暂未就绪：" + reason + "；等待超时，本轮未接受报价"}
			}
			return p, err
		}
		reason = pending.Reason
		if pending.Candidate != nil {
			candidate := *pending.Candidate
			observation, parseErr := nativeObservation(candidate, candidate.URL, true)
			if parseErr != nil {
				return p, parseErr
			}
			signature := fmt.Sprintf("%d|%s|%g|%s", candidate.Handle, candidate.URL, pending.UIAPrice, nativeSignature(candidate, observation))
			if signature == lastCandidate {
				candidateReads++
			} else {
				lastCandidate = signature
				candidateReads = 1
			}
			// Three fresh reads: both visible quote/config identity and lagging UIA
			// amount must remain stable. The outer stableWhen still verifies the
			// requested exact option ID and two matching accepted machine quotes.
			if candidateReads >= 3 {
				log.Printf("owned DOM visible quote recovered after %d matching reads: UIA=%g DOM=%g; original and stock withheld", candidateReads, pending.UIAPrice, candidate.DOMQuote.Price)
				return candidate, nil
			}
		} else {
			lastCandidate = ""
			candidateReads = 0
		}
		if err = pauseContext(ctx, delay); err == nil {
			var next nativePage
			next, err = snapshot(ctx, p)
			if err == nil || next.Handle > 0 || next.ResetVerified {
				p = next
			}
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return p, &nativeQuoteFailure{Reason: "浏览器页面暂未就绪：" + reason + "；等待超时，本轮未接受报价"}
			}
			return p, err
		}
	}
}
