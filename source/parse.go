package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}
func hasClass(n *html.Node, k string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == k {
			return true
		}
	}
	return false
}
func hasAttr(n *html.Node, k string) bool {
	for _, a := range n.Attr {
		if a.Key == k {
			return true
		}
	}
	return false
}
func hiddenNode(n *html.Node) bool {
	return hasAttr(n, "hidden") || attr(n, "aria-hidden") == "true" || hasClass(n, "d-none") || strings.Contains(strings.ReplaceAll(attr(n, "style"), " ", ""), "display:none") || strings.Contains(strings.ReplaceAll(attr(n, "style"), " ", ""), "visibility:hidden")
}
func hiddenAncestor(n *html.Node) bool {
	for ; n != nil; n = n.Parent {
		if hiddenNode(n) {
			return true
		}
	}
	return false
}
func nodes(n *html.Node, pred func(*html.Node) bool) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if pred(x) {
			out = append(out, x)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && (x.Data == "script" || x.Data == "style" || x.Data == "noscript" || hiddenNode(x)) {
			return
		}
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			b.WriteByte(' ')
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
func renderNode(n *html.Node) string {
	var b strings.Builder
	_ = html.Render(&b, n)
	return b.String()
}
func rootHTML(body string) (*html.Node, error) { return html.Parse(strings.NewReader(body)) }
func jsonDecode(r io.Reader, v any) error      { return json.NewDecoder(r).Decode(v) }

var moneyRE = regexp.MustCompile(`\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)

func amount(s string) float64 {
	m := moneyRE.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 1000000 {
		return 0
	}
	return v
}
func number(s string) float64 {
	v, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), 64)
	if v > 0 && v < 1000000 {
		return v
	}
	return 0
}
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func discount(price, original float64) float64 {
	if price <= 0 || original <= price {
		return 0
	}
	return math.Round((1-price/original)*10000) / 100
}
func isHost(host, base string) bool { return host == base || strings.HasSuffix(host, "."+base) }
func siteName(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	for _, s := range []string{"dell", "lenovo", "walmart", "bestbuy"} {
		if isHost(h, s+".com") {
			return s
		}
	}
	return ""
}
func validateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("请输入完整的 HTTP/HTTPS 商品网址")
	}
	if siteName(raw) == "" {
		return errors.New("目前支持 Dell、Lenovo、Walmart、Best Buy 的美国商品页面")
	}
	return nil
}
func vipURL(raw string) bool {
	return siteName(raw) == "lenovo" && (strings.Contains(strings.ToLower(raw), "/vipmembers/") || strings.Contains(strings.ToLower(raw), "/perksoffer/"))
}

func pageProblem(root *html.Node) error {
	t := strings.ToLower(textOf(root))
	title := ""
	for _, n := range nodes(root, func(n *html.Node) bool { return n.Data == "title" }) {
		title = textOf(n)
		break
	}
	for _, x := range []string{"verify you are human", "checking your browser", "unusual traffic", "automated queries", "robot or human", "access denied", "request blocked", "captcha challenge"} {
		if strings.Contains(t, x) {
			return fmt.Errorf("网页访问被限制（%s）；保留上次数据，可用“打开采集浏览器”查看", x)
		}
	}
	if strings.Contains(strings.ToLower(title), "sign in") || strings.Contains(strings.ToLower(title), "log in") || strings.Contains(strings.ToLower(title), "login") {
		return errors.New("页面要求登录；保留上次数据，请打开采集浏览器完成登录")
	}
	return nil
}
func byClassPrice(root *html.Node, classes ...string) float64 {
	for _, c := range classes {
		for _, n := range nodes(root, func(x *html.Node) bool { return hasClass(x, c) }) {
			if v := amount(textOf(n)); v > 0 {
				return v
			}
		}
	}
	return 0
}
func originalPrice(root *html.Node) float64 {
	for _, n := range nodes(root, func(n *html.Node) bool {
		return hasClass(n, "original-price") || hasClass(n, "strike-through") || hasClass(n, "estimated-value-price") || n.Data == "del" || n.Data == "s"
	}) {
		if v := amount(textOf(n)); v > 0 {
			return v
		}
	}
	t := textOf(root)
	re := regexp.MustCompile(`(?i)(?:Estimated Value|Est\.? Value|List Price|Regular Price)\s*[: ]*\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
	if m := re.FindStringSubmatch(t); len(m) > 1 {
		return number(m[1])
	}
	return 0
}
func scopedStock(root *html.Node) string {
	// A disabled placeholder purchase button does not prove availability.
	t := strings.ToLower(textOf(root))
	for _, s := range []string{"out of stock", "sold out", "currently unavailable", "temporarily unavailable"} {
		if strings.Contains(t, s) {
			return stockOut
		}
	}
	for _, n := range nodes(root, func(n *html.Node) bool {
		return hasClass(n, "unavailable") || hasClass(n, "out-of-stock") || hasClass(n, "sold-out")
	}) {
		if textOf(n) != "" {
			return stockOut
		}
	}
	if regexp.MustCompile(`(?i)\bUnavailable\b`).MatchString(t) {
		return stockOut
	}
	for _, n := range nodes(root, func(n *html.Node) bool { return n.Data == "button" || n.Data == "a" || n.Data == "input" }) {
		txt := strings.ToLower(textOf(n) + " " + attr(n, "value") + " " + attr(n, "aria-label"))
		disabled := false
		for _, a := range n.Attr {
			if a.Key == "disabled" || a.Key == "hidden" {
				disabled = true
			}
		}
		if attr(n, "aria-disabled") == "true" || strings.Contains(attr(n, "class"), "disabled") {
			disabled = true
		}
		if !disabled && !hiddenAncestor(n) && (strings.Contains(txt, "add to cart") || strings.Contains(txt, "add to bag") || strings.Contains(txt, "buy now")) {
			return stockIn
		}
	}
	return stockUnknown
}

var offerRE = regexp.MustCompile(`(?i)[a-z0-9]+_(?:fixed|reg|so|cto|sb|custom)_[a-z0-9_]+`)

func offerFromURL(raw string) string {
	u, _ := url.Parse(raw)
	if u == nil {
		return ""
	}
	return strings.ToLower(offerRE.FindString(u.Path))
}
func cardOffer(n *html.Node) string {
	for _, x := range nodes(n, func(x *html.Node) bool { return x.Type == html.ElementNode }) {
		for _, k := range []string{"data-oc", "data-offerid", "data-offer-id", "value", "id"} {
			if v := offerRE.FindString(attr(x, k)); v != "" {
				return strings.ToLower(v)
			}
		}
	}
	return ""
}
func cardURL(raw, id string, n *html.Node) string {
	u, _ := url.Parse(raw)
	if u == nil {
		return ""
	}
	for _, x := range nodes(n, func(x *html.Node) bool { return x.Data == "a" }) {
		h := attr(x, "href")
		if strings.Contains(strings.ToLower(h), id) {
			v, e := u.Parse(h)
			if e == nil && siteName(v.String()) == "dell" {
				return v.String()
			}
		}
	}
	parts := strings.Split(strings.TrimRight(u.Path, "/"), "/")
	if offerRE.MatchString(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	u.Path = strings.Join(parts, "/") + "/" + id
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func specFromCard(n *html.Node, label string) string {
	for _, x := range nodes(n, func(x *html.Node) bool { return x.Data == "li" }) {
		t := textOf(x)
		if strings.HasPrefix(strings.ToLower(t), strings.ToLower(label)+" ") {
			return strings.TrimSpace(t[len(label):])
		}
	}
	return ""
}
func dellCards(root *html.Node, raw string) []DellResult {
	for _, n := range nodes(root, func(n *html.Node) bool { return n.Data == "body" }) {
		if hasClass(n, "upd-superconfig") && hasClass(n, "show-custom-order") {
			return nil
		}
	}
	var out []DellResult
	seen := map[string]bool{}
	for _, n := range nodes(root, func(n *html.Node) bool {
		return hasClass(n, "card-deck-item") || hasClass(n, "offer-card") || hasClass(n, "ps-stack")
	}) {
		id := cardOffer(n)
		if id == "" || seen[id] || hiddenAncestor(n) {
			continue
		}
		seen[id] = true
		p := byClassPrice(n, "sale-price", "ps-dell-price", "offer-price")
		o := originalPrice(n)
		s := scopedStock(n)
		if s == stockUnknown && strings.Contains(strings.ToLower(textOf(n)), "get it as soon as") {
			for _, button := range nodes(n, func(x *html.Node) bool { return hasClass(x, "offer-select") }) {
				if attr(button, "data-offer-id") == id && !hasAttr(button, "disabled") && attr(button, "aria-disabled") != "true" {
					s = stockIn
				}
			}
		}
		source := "Fixed Offer"
		t := strings.ToLower(textOf(n))
		if discount(p, o) > 0 || strings.Contains(t, "limited quantity deal") {
			source = "Deal"
		}
		out = append(out, DellResult{OfferID: id, Source: source, CPU: specFromCard(n, "Processor"), GPU: specFromCard(n, "Graphics Card"), Memory: specFromCard(n, "Memory"), Storage: specFromCard(n, "Storage"), Display: specFromCard(n, "Displays"), Price: p, Original: o, Discount: discount(p, o), Stock: s, URL: cardURL(raw, id, n), Confirmed: p > 0 && s != stockUnknown})
	}
	return out
}
func heroScope(root *html.Node) *html.Node {
	for _, n := range nodes(root, func(n *html.Node) bool { return attr(n, "id") == "hero-section" || hasClass(n, "hero-section") }) {
		return n
	}
	return root
}
func parseDell(body, raw string, family bool) (Observation, error) {
	root, err := rootHTML(body)
	if err != nil {
		return Observation{}, err
	}
	if err = pageProblem(root); err != nil {
		return Observation{}, err
	}
	cards := dellCards(root, raw)
	// Dell keeps prior offer-view templates in the configurable DOM. They are
	// not freshly observed ordinary quotes while the BYO view is active.
	if len(configGroups(body)) > 0 {
		cards = nil
	}
	requested := offerFromURL(raw)
	if !family && requested != "" {
		for _, c := range cards {
			if c.OfferID == requested {
				if c.Price <= 0 && c.Stock != stockOut {
					return Observation{}, errors.New("目标 Dell Offer 未读到当前售价")
				}
				return Observation{Price: c.Price, Original: c.Original, Discount: c.Discount, Stock: c.Stock, Parser: "Dell Offer ID + 卡片", Results: []DellResult{c}}, nil
			}
		}
		// For BYO pages without an offer carousel use the displayed current configuration.
		if !strings.Contains(requested, "_reg_") && !strings.Contains(requested, "_cto_") {
			return Observation{}, errors.New("页面未找到目标 Offer ID；不使用其他配置价格")
		}
	}
	if family && len(cards) > 0 {
		o := Observation{Results: cards, Parser: "Dell 实际 Offer 卡片", Stock: stockUnknown}
		for _, c := range cards {
			if c.Price > 0 && (o.Price == 0 || c.Price < o.Price) {
				o.Price = c.Price
				o.Original = c.Original
				o.Discount = c.Discount
			}
			if c.Stock == stockIn {
				o.Stock = stockIn
			}
		}
		return o, nil
	}
	h := heroScope(root)
	p := byClassPrice(h, "sale-price", "ps-dell-price")
	o := originalPrice(h)
	s := scopedStock(h)
	if p == 0 {
		return Observation{}, errors.New("Dell 当前配置售价未识别；未读取分期月供或其他配置价格")
	}
	for _, n := range nodes(root, func(n *html.Node) bool {
		return hasClass(n, "add-to-cart-stack") || attr(n, "id") == "add-to-cart-stack"
	}) {
		s = scopedStock(n)
		break
	}
	custom := len(configGroups(body)) > 0 || strings.Contains(requested, "_reg_") || strings.Contains(requested, "_cto_")
	row := DellResult{OfferID: requested, Source: "BYO 当前配置", URL: raw, Price: p, Original: o, Discount: discount(p, o), Stock: s, Confirmed: s != stockUnknown, Custom: custom, DiscountConfirmed: custom && o >= p, Options: selectedMap(body)}
	bindHTMLCoreSpecs(&row, body)
	return Observation{Price: p, Original: o, Discount: row.Discount, Stock: s, Parser: "Dell 当前配置", Results: []DellResult{row}}, nil
}

func labelAmount(t string, labels ...string) float64 {
	for _, label := range labels {
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(label) + `\s*[: ]*\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
		if m := re.FindStringSubmatch(t); len(m) > 1 {
			return number(m[1])
		}
	}
	return 0
}
func parseLenovo(body, raw string) (Observation, error) {
	r, e := rootHTML(body)
	if e != nil {
		return Observation{}, e
	}
	if e = pageProblem(r); e != nil {
		return Observation{}, e
	}
	if configured, found, err := parseLenovoCTO(r, raw); found {
		return configured, err
	}
	if single, found, err := parseLenovoSingleSKU(r, raw); found {
		return single, err
	}
	if models, found, err := parseLenovoModels(r, raw); found {
		return models, err
	}
	// Prefer the product purchase panel, never related-product cards.
	var panel *html.Node
	for _, n := range nodes(r, func(n *html.Node) bool {
		return hasClass(n, "product-price") || hasClass(n, "pdp-price") || hasClass(n, "pricingSummary") || attr(n, "id") == "product-price"
	}) {
		if labelAmount(textOf(n), "Exclusive Price", "Your Price", "Web Price", "Price") > 0 {
			panel = n
			break
		}
	}
	if panel == nil {
		panel = r
	}
	t := textOf(panel)
	price := labelAmount(t, "Exclusive Price", "Your Price", "Web Price", "Sale Price")
	original := labelAmount(t, "Est Value", "Est. Value", "Estimated Value", "List Price")
	if price == 0 && !vipURL(raw) {
		price = byClassPrice(panel, "final-price", "sale-price", "price-current")
		if price == 0 {
			price, _ = schemaOffer(r, raw)
		}
	}
	if price == 0 {
		return Observation{}, errors.New("Lenovo 未读到明确当前售价；Est Value 或折扣反算不作为现价")
	}
	if vipURL(raw) && labelAmount(t, "Exclusive Price") == 0 {
		return Observation{}, errors.New("Lenovo VIP 页面未显示 Exclusive Price；可能需要登录，不使用普通价替代")
	}
	// Do not turn stock from recommended products into this product's stock.
	stockScope := panel
	for p := panel; p.Parent != nil; p = p.Parent {
		if hasClass(p, "product-details") || hasClass(p, "pdp-product") || hasClass(p, "product-info") {
			stockScope = p
			break
		}
	}
	s := scopedStock(stockScope)
	if panel == r {
		s = stockUnknown
		_, v := schemaOffer(r, raw)
		if v != "" {
			s = v
		}
	}
	return Observation{Price: price, Original: original, Discount: discount(price, original), Stock: s, Parser: "Lenovo 当前价语义"}, nil
}

// JSON-LD Product offers are useful only when bound to the URL/SKU, not to recommendations.
func schemaOffer(root *html.Node, raw string) (float64, string) {
	var best float64
	stock := ""
	wanted, _ := url.Parse(raw)
	var walk func(any, string)
	walk = func(v any, parent string) {
		switch x := v.(type) {
		case []any:
			for _, q := range x {
				walk(q, parent)
			}
		case map[string]any:
			typeName, _ := x["@type"].(string)
			if typeName == "Product" {
				identity := ""
				for _, k := range []string{"url", "sku", "productID", "mpn", "@id"} {
					if s, ok := x[k].(string); ok && s != "" && wanted != nil && (strings.Contains(strings.ToLower(wanted.Path), strings.ToLower(s)) || sameProductURL(s, raw)) {
						identity = s
						break
					}
				}
				if identity != "" {
					walk(x["offers"], identity)
				}
				return
			}
			if parent != "" && (typeName == "Offer" || x["price"] != nil) {
				condition := strings.ToLower(fmt.Sprint(x["itemCondition"]))
				openbox := strings.Contains(strings.ToLower(raw), "openbox") || strings.Contains(strings.ToLower(raw), "open-box")
				if !openbox && (strings.HasSuffix(condition, "/usedcondition") || strings.HasSuffix(condition, "/refurbishedcondition")) {
					return
				}
				if openbox && !strings.HasSuffix(condition, "/usedcondition") {
					return
				}
				currency, _ := x["priceCurrency"].(string)
				if currency != "" && currency != "USD" {
					return
				}
				s := fmt.Sprint(x["price"])
				p := number(s)
				if p > 0 && best == 0 {
					best = p
					a := strings.ToLower(fmt.Sprint(x["availability"]))
					if strings.HasSuffix(a, "/instock") || strings.HasSuffix(a, "/limitedavailability") {
						stock = stockIn
					} else if strings.HasSuffix(a, "/outofstock") || strings.HasSuffix(a, "/soldout") {
						stock = stockOut
					}
				}
				return
			}
			for _, k := range []string{"@graph", "mainEntity"} {
				walk(x[k], parent)
			}
		}
	}
	for _, n := range nodes(root, func(n *html.Node) bool { return n.Data == "script" && attr(n, "type") == "application/ld+json" }) {
		var b strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				b.WriteString(c.Data)
			}
		}
		var v any
		if jsonDecode(strings.NewReader(b.String()), &v) == nil {
			walk(v, "")
		}
	}
	return best, stock
}
func sameProductURL(a, b string) bool {
	ua, e := url.Parse(a)
	ub, f := url.Parse(b)
	if e != nil || f != nil {
		return false
	}
	return ua.Hostname() == ub.Hostname() && strings.TrimRight(ua.Path, "/") == strings.TrimRight(ub.Path, "/")
}
func parseRetail(body, raw string) (Observation, error) {
	r, e := rootHTML(body)
	if e != nil {
		return Observation{}, e
	}
	if e = pageProblem(r); e != nil {
		return Observation{}, e
	}
	p, s := schemaOffer(r, raw)
	original := 0.0
	// Walmart/Best Buy purchase panels; without a primary-product scope do not guess.
	for _, n := range nodes(r, func(n *html.Node) bool {
		return attr(n, "data-testid") == "product-price" || hasClass(n, "priceView-customer-price") || hasClass(n, "price-block")
	}) {
		if p == 0 {
			p = amount(textOf(n))
		}
		original = originalPrice(n)
		if s == "" {
			s = scopedStock(n.Parent)
		}
		break
	}
	if p == 0 {
		return Observation{}, errors.New("未读取到绑定目标商品的现价；页面可能未加载完成或访问受限")
	}
	if s == "" {
		s = stockUnknown
	}
	return Observation{Price: p, Original: original, Discount: discount(p, original), Stock: s, Parser: strings.ToUpper(siteName(raw)) + " 商品价格/库存"}, nil
}
func parsePage(body, raw string, family bool) (Observation, error) {
	switch siteName(raw) {
	case "dell":
		return parseDell(body, raw, family)
	case "lenovo":
		return parseLenovo(body, raw)
	default:
		return parseRetail(body, raw)
	}
}

func observationSignature(o Observation) string {
	if len(o.Results) == 0 {
		return fmt.Sprintf("%.2f|%.2f|%.2f|%s", o.Price, o.Original, o.Discount, o.Stock)
	}
	var v []string
	for _, c := range o.Results {
		if c.Stale {
			continue
		}
		v = append(v, fmt.Sprintf("%s|%s|%s|%s|%s|%s|%.2f|%.2f|%.2f|%s|%v", c.OfferID, c.CPU, c.GPU, c.Memory, c.Storage, c.Display, c.Price, c.Original, c.Discount, c.Stock, c.Confirmed))
	}
	sort.Strings(v)
	return strings.Join(v, "\n")
}

func bindHTMLCoreSpecs(row *DellResult, body string) {
	for _, g := range configGroups(body) {
		for _, q := range g.Options {
			if !q.Selected {
				continue
			}
			switch strings.ToLower(g.Label) {
			case "processor":
				row.CPU = q.Label
			case "graphics card":
				row.GPU = q.Label
			case "memory":
				row.Memory = q.Label
			case "storage":
				row.Storage = q.Label
			case "display", "displays":
				row.Display = q.Label
			}
		}
	}
}
