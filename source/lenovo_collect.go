package main

import (
	"context"
	"net/url"
	"strings"
)

// The P1 route is a live-verified link. Read its default configuration once;
// do not enumerate Lenovo combinations or compare arbitrary upgraded totals.
func (a *App) completeLenovoCustom(ctx context.Context, raw string, o Observation, c *cdpClient, progress func(string)) (Observation, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return o, nil
	}
	u.Fragment = ""
	u.RawQuery = ""
	if strings.TrimRight(u.String(), "/") != vipP1FamilyURL() {
		return o, nil
	}
	for i, row := range o.Results {
		if !row.Custom || row.OfferID != "21UECTO1WWUS1" {
			continue
		}
		progress("正在核对 P1 定制基础配置的整机折扣")
		var quote Observation
		var err error
		if c != nil {
			_, err = c.call(ctx, "Page.navigate", map[string]any{"url": p1CTOURL})
			if err == nil {
				var body string
				body, err = waitBrowserPage(ctx, c, p1CTOURL)
				if err == nil {
					quote, err = parseLenovo(body, p1CTOURL)
				}
			}
		} else {
			var body string
			body, err = a.getPage(ctx, p1CTOURL)
			if err == nil {
				quote, err = parseLenovo(body, p1CTOURL)
			}
			if err != nil {
				quote, err = a.browser.read(ctx, p1CTOURL, func(_ *cdpClient, body string) (Observation, error) { return parseLenovo(body, p1CTOURL) })
			}
		}
		if err != nil {
			o.Partial = true
			o.Note = "定制整机折扣未取得；" + err.Error()
			o.Results[i].Note = o.Note
		} else if len(quote.Results) == 1 {
			quote.Results[0].Note += "；自动检查读取基础配置，不遍历所有升级组合"
			o.Results[i] = quote.Results[0]
			o.Parser += " / 定制整机折扣"
		}
		break
	}
	return o, nil
}

func vipP1FamilyURL() string {
	return "https://www.lenovo.com/us/vipmembers/perksoffer/en/p/laptops/thinkpad/thinkpadp/thinkpad-p1-gen-9-16-inch-intel-mobile-workstation/len101t0180"
}
