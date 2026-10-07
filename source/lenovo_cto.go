package main

import (
	"errors"
	"golang.org/x/net/html"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Observed by following Build Your PC on the user's exact P1 family page.
const p1CTOURL = "https://www.lenovo.com/us/vipmembers/perksoffer/en/configurator/cto/?bundleId=21UECTO1WWUS1"

// The scope must contain only the whole-device purchase quote. Never pass option
// surcharges, rewards, related offers or an entire configurator to this helper.
func quoteDiscount(scope *html.Node, price float64) (float64, float64, bool) {
	for _, n := range nodes(scope, func(n *html.Node) bool { return n.Type == html.ElementNode && !hiddenAncestor(n) }) {
		if n.Data == "del" || n.Data == "s" || hasClass(n, "original-price") || hasClass(n, "strike-through") || hasClass(n, "estimated-value-price") || hasClass(n, "web-price") {
			if original := amount(textOf(n)); original >= price && price > 0 {
				return original, discount(price, original), true
			}
		}
	}
	if original := labelAmount(textOf(scope), "Estimated Value", "Est Value", "Est. Value", "List Price", "Regular Price"); original >= price && price > 0 {
		return original, discount(price, original), true
	}
	re := regexp.MustCompile(`(?i)\b([0-9]{1,2}(?:\.[0-9]+)?)\s*%\s*off\b`)
	if m := re.FindStringSubmatch(textOf(scope)); len(m) == 2 && price > 0 {
		d := number(m[1])
		if d >= 0 && d < 100 {
			return 0, d, true
		}
	}
	return 0, 0, false
}

func parseLenovoCTO(root *html.Node, raw string) (Observation, bool, error) {
	u, err := url.Parse(raw)
	if err != nil || !strings.Contains(strings.ToLower(u.Path), "/configurator/cto/") {
		return Observation{}, false, nil
	}
	part := strings.ToUpper(u.Query().Get("bundleId"))
	if part == "" || !strings.Contains(part, "CTO") {
		return Observation{}, true, errors.New("联想定制页缺少目标 bundleId")
	}
	if vipURL(raw) && !lenovoMemberStore(root, raw) {
		return Observation{}, true, errors.New("联想 VIP 定制页尚未确认会员店铺")
	}
	var header, priceScope *html.Node
	for _, n := range nodes(root, func(n *html.Node) bool { return hasClass(n, "header-content") && !hiddenAncestor(n) }) {
		if len(nodes(n, func(x *html.Node) bool {
			return hasClass(x, "header-rating") && hasClass(x, "card-rating-container_"+part) && !hiddenAncestor(x)
		})) > 0 {
			header = n
			break
		}
	}
	if header == nil {
		return Observation{}, true, errors.New("联想定制页未加载目标型号标识；未使用其他配置报价")
	}
	for _, n := range nodes(header, func(n *html.Node) bool { return hasClass(n, "headerPrice") && !hiddenAncestor(n) }) {
		priceScope = n
		break
	}
	if priceScope == nil {
		return Observation{}, true, errors.New("联想定制页尚未加载整机当前售价")
	}
	price := 0.0
	for _, n := range nodes(priceScope, func(n *html.Node) bool { return hasClass(n, "formatPrice") && !hiddenAncestor(n) }) {
		price = amount(textOf(n))
		if price > 0 {
			break
		}
	}
	if price <= 0 {
		return Observation{}, true, errors.New("联想定制页尚未加载整机当前售价")
	}
	options, labels := map[string]string{}, map[string]string{}
	for _, group := range nodes(root, func(n *html.Node) bool { return attr(n, "role") == "radiogroup" && !hiddenAncestor(n) }) {
		key := strings.Fields(attr(group, "aria-labelledby"))
		if len(key) == 0 {
			continue
		}
		for _, n := range nodes(group, func(n *html.Node) bool {
			return hasClass(n, "cto-v") && attr(n, "aria-checked") == "true" && !hiddenAncestor(n)
		}) {
			if value := attr(n, "value"); value != "" {
				options[key[0]] = value
				labels[key[0]] = strings.TrimSpace(attr(n, "aria-label"))
			}
		}
	}
	memory := labels["CItemTitle_NBCAMM_MEMORY"]
	if memory == "" {
		memory = labels["CItemTitle_NBMEMORY"]
	}
	if options["CItemTitle_NBPROCESSOR"] == "" || memory == "" {
		return Observation{}, true, errors.New("联想定制页当前配置尚未加载完整")
	}
	original, pct, verified := quoteDiscount(priceScope, price)
	note := "当前页面选中配置的整机报价；不代表现货"
	if !verified {
		note += "；页面未公布整机原价或折扣，折扣待公布"
	}
	keys := make([]string, 0, len(options))
	for k := range options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var signature strings.Builder
	for _, k := range keys {
		signature.WriteString(k + "=" + options[k] + ";")
	}
	row := DellResult{OfferID: part + "-" + shortHash(signature.String()), Source: "定制当前配置", URL: raw, Price: price, Original: original, Discount: pct, Stock: stockUnknown, Custom: true, DiscountConfirmed: verified, Note: note, Options: options, CPU: labels["CItemTitle_NBPROCESSOR"], GPU: labels["CItemTitle_NBGRAPHICS"], Memory: memory, Storage: labels["CItemTitle_NBSTORAGE_SELECTION"], Display: labels["CItemTitle_NBDISPLAY"]}
	return Observation{Price: price, Original: original, Discount: pct, Stock: stockUnknown, Parser: "Lenovo 定制整机 / " + part, Results: []DellResult{row}, Note: note}, true, nil
}
