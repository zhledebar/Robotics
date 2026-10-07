package main

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

var lenovoPartRE = regexp.MustCompile(`(?i)\bPart Number\s*:?\s*([a-z0-9]{6,24})\b`)
var lenovoFamilyRE = regexp.MustCompile(`(?i)^len[0-9a-z]+$`)

func lenovoURLPart(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return ""
	}
	path := strings.TrimRight(u.Path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	return strings.ToUpper(path)
}

func lenovoSpec(card *html.Node, label string) string {
	for _, li := range nodes(card, func(n *html.Node) bool { return n.Data == "li" }) {
		for _, img := range nodes(li, func(n *html.Node) bool { return n.Data == "img" }) {
			if strings.EqualFold(attr(img, "alt"), label) {
				return textOf(li)
			}
		}
	}
	for _, row := range nodes(card, func(n *html.Node) bool { return hasClass(n, "normal_specs") && !hiddenAncestor(n) }) {
		var name, value string
		for _, n := range nodes(row, func(n *html.Node) bool { return hasClass(n, "item_name") || hasClass(n, "item_content") }) {
			if hasClass(n, "item_name") {
				name = textOf(n)
			} else {
				value = textOf(n)
			}
		}
		if strings.EqualFold(name, label) {
			return value
		}
	}
	return ""
}

// Some fixed models show a single current price without an Exclusive Price label.
// Accept that price only inside the observed Perks storefront, bound to the SKU's
// own purchase area. A family hero, public-store redirect or CTO starting price
// cannot establish the member offer.
func lenovoMemberStore(root *html.Node, raw string) bool {
	if !vipURL(raw) {
		return false
	}
	affinityTitle, memberLabel := false, false
	for _, n := range nodes(root, func(n *html.Node) bool { return n.Data == "title" || hasClass(n, "affinity_label") }) {
		t := strings.ToLower(textOf(n))
		if n.Data == "title" && strings.Contains(t, "lenovo") && strings.Contains(t, "affinity store") {
			affinityTitle = true
		}
		if hasClass(n, "affinity_label") && !hiddenAncestor(n) && strings.Contains(t, "perks at work members") {
			memberLabel = true
		}
	}
	return affinityTitle && memberLabel
}

// A direct SKU page uses a different purchase component from the model list.
// Its displayed part number, current-price container and CTA wrapper must agree.
func parseLenovoSingleSKU(root *html.Node, raw string) (Observation, bool, error) {
	target := lenovoURLPart(raw)
	if lenovoFamilyRE.MatchString(target) {
		return Observation{}, false, nil
	}
	for _, panel := range nodes(root, func(n *html.Node) bool {
		return hasClass(n, "single_model_PDP_container_main_right") && !hiddenAncestor(n)
	}) {
		part := ""
		for _, n := range nodes(panel, func(n *html.Node) bool { return hasClass(n, "part_number_content") && !hiddenAncestor(n) }) {
			part = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(textOf(n), ":")))
			break
		}
		if part == "" || !strings.EqualFold(part, target) {
			return Observation{}, true, errors.New("联想独立商品页未加载目标 Part Number；未使用其他型号价格")
		}
		var priceScope *html.Node
		for _, n := range nodes(panel, func(n *html.Node) bool { return hasClass(n, "single_pdp_price_container") && !hiddenAncestor(n) }) {
			priceScope = n
			break
		}
		if priceScope == nil {
			return Observation{}, true, errors.New("联想独立商品页尚未加载目标型号的当前售价")
		}
		exclusive := labelAmount(textOf(priceScope), "Exclusive Price", "Member Price")
		price := exclusive
		if price == 0 {
			price = byClassPrice(priceScope, "price-title")
		}
		stock, cto := stockUnknown, false
		for _, wrapper := range nodes(panel, func(n *html.Node) bool { return hasClass(n, "pc-cta-wrapper_"+part) && !hiddenAncestor(n) }) {
			for _, button := range nodes(wrapper, func(n *html.Node) bool { return hasClass(n, "product_cta_button") && !hiddenAncestor(n) }) {
				if hasClass(button, "cto") || strings.Contains(strings.ToLower(textOf(button)), "build your pc") {
					cto = true
				} else if s := scopedStock(button); s != stockUnknown {
					stock = s
				}
			}
		}
		if price <= 0 {
			return Observation{}, true, errors.New("联想独立商品页未读到目标型号的当前售价")
		}
		source, note := "联想型号报价", "独立商品页 Part Number "+part
		if vipURL(raw) {
			if exclusive > 0 {
				source = "VIP 型号会员价"
			} else if lenovoMemberStore(root, raw) && !cto && stock != stockUnknown {
				source = "VIP 商店现价"
				note += "；会员店铺与固定型号购买区已核对，页面未标注专属折扣"
			} else {
				return Observation{}, true, errors.New("联想 VIP 独立商品页会员报价未确认；未使用定制起价或普通商店价格")
			}
		}
		if cto {
			source, stock = "定制起价", stockUnknown
			note += "；需完成配置后核价，未确认现货"
		}
		original := originalPrice(priceScope)
		row := DellResult{OfferID: part, Source: source, CPU: lenovoSpec(panel, "Processor"), GPU: lenovoGPU(panel), Memory: lenovoSpec(panel, "Memory"), Storage: lenovoSpec(panel, "Storage"), Display: lenovoSpec(panel, "Display"), URL: raw, Price: price, Original: original, Discount: discount(price, original), Stock: stock, Confirmed: !cto && stock != stockUnknown, Custom: cto, Note: note}
		if cto {
			row.Original, row.Discount, row.DiscountConfirmed = quoteDiscount(priceScope, price)
		}
		return Observation{Price: price, Original: original, Discount: row.Discount, Stock: stock, Parser: "Lenovo 型号 " + part + " / " + source, Results: []DellResult{row}}, true, nil
	}
	return Observation{}, false, nil
}

// Model list cards bind each price and primary purchase button to its own part number.
// An unlabelled CTO starting price is kept for reference, never treated as a confirmed VIP offer.
func lenovoModelCards(root *html.Node, raw string) ([]DellResult, bool) {
	var rows []DellResult
	seen := map[string]bool{}
	memberStore := lenovoMemberStore(root, raw)
	for _, card := range nodes(root, func(n *html.Node) bool { return hasClass(n, "dlp-product-card") }) {
		if hiddenAncestor(card) {
			continue
		}
		m := lenovoPartRE.FindStringSubmatch(textOf(card))
		if len(m) != 2 {
			continue
		}
		part := strings.ToUpper(m[1])
		if seen[part] {
			continue
		}
		var priceScope *html.Node
		for _, n := range nodes(card, func(n *html.Node) bool { return hasClass(n, "price-stack") }) {
			if hasClass(n, "price-stack_"+part) || hasClass(n, "price-stack_"+strings.ToLower(part)) {
				priceScope = n
				break
			}
		}
		if priceScope == nil || hiddenAncestor(priceScope) {
			continue
		}
		seen[part] = true
		text := textOf(priceScope)
		exclusive := labelAmount(text, "Exclusive Price", "Member Price")
		price := exclusive
		if price == 0 {
			price = byClassPrice(priceScope, "price-title", "final-price")
		}
		original := originalPrice(priceScope)
		stock := stockUnknown
		cto := false
		for _, button := range nodes(card, func(n *html.Node) bool { return hasClass(n, "product_cta_button") }) {
			if strings.Contains(strings.ToLower(textOf(button)), "build your pc") || hasClass(button, "cto") {
				cto = true
				continue
			}
			if hiddenAncestor(button) {
				continue
			}
			if v := scopedStock(button); v != stockUnknown {
				stock = v
				break
			}
		}
		source := "联想型号报价"
		note := "在原机型页选择 Part Number " + part
		priceTrusted := price > 0 && (!vipURL(raw) || exclusive > 0)
		if vipURL(raw) && exclusive > 0 {
			source = "VIP 型号会员价"
		}
		if cto {
			source = "定制起价"
			note += "；基础配置，Build Your PC 不代表现货"
			stock = stockUnknown
		}
		if vipURL(raw) && exclusive == 0 {
			if memberStore && !cto && stock != stockUnknown && price > 0 {
				source = "VIP 商店现价"
				note += "；会员店铺与固定型号购买区已核对，页面未标注专属折扣"
				priceTrusted = true
			} else {
				note += "；该卡片未显示明确会员价格标签，不参与会员价提醒"
				priceTrusted = false
			}
		}
		row := DellResult{OfferID: part, Source: source, CPU: lenovoSpec(card, "Processor"), GPU: lenovoGPU(card), Memory: lenovoSpec(card, "Memory"), Storage: lenovoSpec(card, "Storage"), Display: lenovoSpec(card, "Display"), URL: raw, Price: price, Original: original, Discount: discount(price, original), Stock: stock, Confirmed: priceTrusted && stock != stockUnknown, Custom: cto, Note: note}
		if cto {
			row.Original, row.Discount, row.DiscountConfirmed = quoteDiscount(priceScope, price)
			row.DiscountConfirmed = row.DiscountConfirmed && (exclusive > 0 || !vipURL(raw) || memberStore)
			if !row.DiscountConfirmed {
				row.Discount = 0
				row.Note = "基础配置起价；未确认现货；页面未公布可核对的定制折扣，折扣待公布"
			}
		}
		rows = append(rows, row)
	}
	return rows, len(rows) > 0
}

func parseLenovoModels(root *html.Node, raw string) (Observation, bool, error) {
	rows, found := lenovoModelCards(root, raw)
	if !found {
		if lenovoFamilyRE.MatchString(lenovoURLPart(raw)) {
			label := "Lenovo"
			if vipURL(raw) {
				label += " VIP"
			}
			return Observation{}, true, errors.New(label + " 机型页尚未加载可核对的型号卡片；未使用起售价或推荐商品报价")
		}
		return Observation{}, false, nil
	}
	segment := lenovoURLPart(raw)
	family := lenovoFamilyRE.MatchString(segment)
	if !family {
		var target []DellResult
		for _, r := range rows {
			if strings.EqualFold(segment, r.OfferID) {
				target = append(target, r)
			}
		}
		if len(target) == 0 {
			return Observation{}, true, errors.New("联想页面未找到目标 Part Number；未使用其他型号价格")
		}
		rows = target
	}
	var best *DellResult
	for i := range rows {
		r := &rows[i]
		if !r.Confirmed || r.Price <= 0 {
			continue
		}
		if best == nil || (r.Stock == stockIn && best.Stock != stockIn) || (r.Stock == best.Stock && r.Price < best.Price) {
			best = r
		}
	}
	if best == nil {
		return Observation{}, true, errors.New("联想型号卡片已加载，但没有同时确认售价与库存的会员报价；定制起价未作为现货")
	}
	return Observation{Price: best.Price, Original: best.Original, Discount: best.Discount, Stock: best.Stock, Parser: "Lenovo 型号 " + best.OfferID + " / " + best.Source, Results: rows}, true, nil
}

func modelResults(p *Product) []DellResult {
	if siteName(p.URL) == "lenovo" {
		return p.LenovoResults
	}
	return p.DellResults
}

func lenovoGPU(card *html.Node) string {
	for _, label := range []string{"Graphic Card", "Graphics Card", "Graphics"} {
		if v := lenovoSpec(card, label); v != "" {
			return v
		}
	}
	return ""
}
