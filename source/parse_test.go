package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapturedPages(t *testing.T) {
	dir := os.Getenv("MONITOR_FIXTURES")
	if dir == "" {
		t.Skip("set MONITOR_FIXTURES for captured live HTML")
	}
	pages := []struct {
		name, url string
		family    bool
	}{
		{"dell_fixed.html", "https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250/pc16250_fixed_22", false},
		{"dell_fixed.html", "https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250/pc16250_fixed_28", false},
		{"dell_fixed.html", "https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250", true},
		{"dell_xps_byo.html", "https://www.dell.com/en-us/shop/dell-laptops/spd/xps16da16260/da16260_reg_01", false},
		{"lenovo_p14s.html", "https://www.lenovo.com/us/en/p/laptops/thinkpad/thinkpadp/thinkpad-p14s-gen-6-14-inch-amd-mobile-workstation/21ql0021us", false},
		{"walmart_laptop.html", "https://www.walmart.com/ip/5498888952", false},
		{"bestbuy_laptop.html", "https://www.bestbuy.com/product/lenovo-yoga-7i-2-in-1-copilot-pc-16-2k-touchscreen-laptop-intel-core-ultra-5-processor-2024-16gb-memory-512gb-ssd-luna-grey/6615769?sb_share_source=PDP", false},
	}
	for _, p := range pages {
		t.Run(p.name+p.url, func(t *testing.T) {
			b, e := os.ReadFile(filepath.Join(dir, p.name))
			if e != nil {
				t.Skip(e)
			}
			o, e := parsePage(string(b), p.url, p.family)
			t.Logf("price=%.2f original=%.2f discount=%.2f stock=%s count=%d parser=%s err=%v", o.Price, o.Original, o.Discount, o.Stock, len(o.Results), o.Parser, e)
			if e != nil {
				t.Error(e)
			}
			if p.name == "dell_fixed.html" && strings.Contains(p.url, "fixed_22") && o.Price != 1700 {
				t.Errorf("target Offer price: %.2f", o.Price)
			}
			if p.name == "dell_xps_byo.html" {
				if o.Stock != stockIn {
					t.Error("enabled Add to Cart for current BYO should confirm stock")
				}
				sizes := map[string]int{}
				for _, g := range configGroups(string(b)) {
					sizes[g.Label] = len(g.Options)
				}
				t.Logf("core option counts=%v", sizes)
				if sizes["Processor"] != 5 || sizes["Storage"] != 4 || sizes["Displays"] != 2 {
					t.Error("captured XPS tree not correctly read")
				}
			}
			if p.name == "bestbuy_laptop.html" && o.Price != 999.99 {
				t.Error("open-box price must not replace the new item price")
			}
		})
	}
}
func TestDellOfferIsolation(t *testing.T) {
	body := `<div class="card-deck-item"><button data-oc="a_fixed_1">Compare</button><span class="sale-price">$900.00</span><div>Unavailable</div><button disabled>Add to Cart</button></div><div class="card-deck-item"><button data-oc="a_fixed_2">Compare</button><span class="sale-price">$700.00</span><button>Add to Cart</button></div>`
	o, e := parseDell(body, "https://www.dell.com/en-us/shop/test/a_fixed_1", false)
	if e != nil || o.Price != 900 || o.Stock != stockOut {
		t.Fatalf("%+v %v", o, e)
	}
	_, e = parseDell(body, "https://www.dell.com/en-us/shop/test/a_fixed_3", false)
	if e == nil {
		t.Error("unknown Offer must not borrow a price")
	}
}
func TestLenovoVIP(t *testing.T) {
	u := "https://www.lenovo.com/us/vipmembers/perksoffer/p/test/21ql0021us"
	o, e := parseLenovo(`<div class="product-details"><div class="product-price">Est Value $2,149 Exclusive Price $1,525.79 29% off</div><button>Add to Cart</button></div>`, u)
	if e != nil || o.Price != 1525.79 || o.Original != 2149 || o.Discount != 29 || o.Stock != stockIn {
		t.Fatalf("%+v %v", o, e)
	}
	for _, s := range []string{"Est Value $2149 29% off", "Web Price $1800 Est Value $2149"} {
		if _, e = parseLenovo(s, u); e == nil {
			t.Error("VIP requires an explicit exclusive price")
		}
	}
}
func TestStockUnknownAndDisabled(t *testing.T) {
	for _, s := range []string{`<button disabled>Add to Cart</button>`, `<button aria-disabled="true">Add to Cart</button>`, `<p>$123</p>`, `<button>Select Configuration</button>`, `<div hidden><button>Add to Cart</button></div>`, `<div class="d-none"><button>Buy Now</button></div>`} {
		r, _ := rootHTML(s)
		if got := scopedStock(r); got != stockUnknown {
			t.Errorf("%s => %s", s, got)
		}
	}
}
func TestSchemaTargetAndMonthlyPayment(t *testing.T) {
	body := `<script type="application/ld+json">{"@graph":[{"@type":"Product","sku":"wrong","offers":{"@type":"Offer","price":99,"priceCurrency":"USD","availability":"https://schema.org/InStock"}},{"@type":"Product","sku":"12345","offers":{"@type":"Offer","price":"999.99","priceCurrency":"USD","availability":"https://schema.org/OutOfStock"}}]}</script><p>or $25/mo</p>`
	o, e := parseRetail(body, "https://www.walmart.com/ip/12345")
	if e != nil || o.Price != 999.99 || o.Stock != stockOut {
		t.Fatalf("%+v %v", o, e)
	}
	if _, e = parseRetail(`<p>or $25/mo</p>`, "https://www.bestbuy.com/product/test/12345"); e == nil {
		t.Error("monthly payment must not become item price")
	}
}
func TestURLValidation(t *testing.T) {
	for _, u := range []string{"https://dell.com.evil.example/p", "https://localhost/p", "file:///tmp/file", "https://user:secret@www.dell.com/p"} {
		if validateURL(u) == nil {
			t.Error(u)
		}
	}
}
