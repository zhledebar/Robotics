package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Discount monitoring reads one actual default configuration. It does not run
// the large combination traversal used by ordinary family price scanning.
func (a *App) completeDellDiscount(ctx context.Context, raw, body string, o Observation, c *cdpClient, progress func(string)) (Observation, error) {
	target := byoURL(body, raw)
	if target == "" {
		return o, nil
	}
	progress("正在读取 Dell 当前定制配置的折扣；不遍历组合")
	var row DellResult
	var err error
	if target == raw && len(configGroups(body)) > 0 {
		row, err = currentBYO(body, raw)
	} else if c != nil {
		_, err = c.call(ctx, "Page.navigate", map[string]any{"url": target})
		if err == nil {
			body, err = waitBrowserPage(ctx, c, target)
			if err == nil {
				row, err = currentBYO(body, target)
			}
		}
	} else {
		_, err = a.browser.read(ctx, target, func(_ *cdpClient, body string) (Observation, error) {
			var e error
			row, e = currentBYO(body, target)
			return Observation{}, e
		})
	}
	if err != nil {
		o.Partial = true
		o.Note = "定制整机折扣未取得；" + err.Error()
		return o, nil
	}
	keys := make([]string, 0, len(row.Options))
	for k := range row.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sig strings.Builder
	for _, k := range keys {
		sig.WriteString(k + "=" + row.Options[k] + ";")
	}
	row.OfferID = offerFromURL(target) + "-BYO-" + shortHash(sig.String())
	row.Note = "只监控当前所选配置的整机折扣；不代表全部升级组合；总价不参与提醒"
	if !row.DiscountConfirmed {
		row.Note += "；折扣待公布"
	}
	// Replacing a custom placeholder must never discard fixed/Deal rows.
	kept := []DellResult{}
	for _, old := range o.Results {
		if !old.Custom {
			kept = append(kept, old)
		}
	}
	o.Results = append(kept, row)
	if len(kept) == 0 {
		o.Price, o.Original, o.Discount, o.Stock = row.Price, row.Original, row.Discount, row.Stock
	}
	o.Parser = "Dell 预配置型号 + 定制整机折扣"
	return o, nil
}

// Ordinary cards are collected together with one actually selected BYO quote.
// Do not click upgrade combinations: their totals are not this user's criterion.
func (a *App) completeDellMixed(ctx context.Context, p Product, body string, o Observation, c *cdpClient, progress func(string)) (Observation, error) {
	root, _ := rootHTML(body)
	back := len(nodes(root, func(n *html.Node) bool {
		return n.Data == "button" && attr(n, "data-user-action") == "ViewAllConfigurations" && !hiddenAncestor(n)
	})) > 0
	if p.DellFamilyScan && len(configGroups(body)) > 0 && back {
		// Capture the requested custom configuration before going to the normal
		// offers view, where all ordinary cards and their own quotes are shown.
		if c == nil {
			return a.browser.read(ctx, p.URL, func(c *cdpClient, b string) (Observation, error) {
				q, e := parseDell(b, p.URL, true)
				if e != nil {
					return Observation{}, e
				}
				return a.completeDellMixed(ctx, p, b, q, c, progress)
			})
		}
		custom, err := a.completeDellDiscount(ctx, p.URL, body, o, c, progress)
		if err != nil {
			return Observation{}, err
		}
		progress("正在读取其他预配置/Deal 型号；不遍历升级组合")
		clicked, err := c.eval(ctx, `(()=>{const b=document.querySelector('button[data-user-action="ViewAllConfigurations"]');if(!b)return 'missing';b.click();return 'clicked'})()`)
		if err == nil && clicked != "clicked" {
			err = errors.New("其他配置入口未加载")
		}
		var offers Observation
		if err == nil {
			var b string
			b, err = waitDellOffers(ctx, c, p.URL)
			if err == nil {
				offers, err = parseDell(b, p.URL, true)
			}
		}
		if err != nil {
			custom.Partial = true
			custom.Note = "其他型号未取得；" + err.Error()
			return custom, nil
		}
		for _, row := range custom.Results {
			if row.Custom {
				offers.Results = append(offers.Results, row)
			}
		}
		offers.Parser = "Dell 预配置/Deal + 当前定制折扣"
		return offers, nil
	}
	if p.DellFamilyScan || len(configGroups(body)) > 0 {
		return a.completeDellDiscount(ctx, p.URL, body, o, c, progress)
	}
	return o, nil
}

func waitDellOffers(ctx context.Context, c *cdpClient, raw string) (string, error) {
	previous := ""
	for i := 0; i < 40; i++ {
		if err := pauseContext(ctx, 750*time.Millisecond); err != nil {
			return "", err
		}
		body, err := c.eval(ctx, browserHTMLExpr)
		if err != nil {
			return "", err
		}
		root, _ := rootHTML(body)
		if err = pageProblem(root); err != nil {
			return "", err
		}
		cards := dellCards(root, raw)
		if len(cards) == 0 || len(configGroups(body)) > 0 {
			previous = ""
			continue
		}
		sig := observationSignature(Observation{Results: cards})
		if sig == previous {
			return body, nil
		}
		previous = sig
	}
	return "", errors.New("其他型号卡片未稳定加载")
}
