package main

import "strings"

// V7.4/V7.5 BYO rows predate the Custom field. Recognize only explicit historical
// BYO markers; a reg URL alone is also used by ordinary Dell cards.
func dellResultIsCustom(row DellResult) bool {
	if row.Custom {
		return true
	}
	id, source := strings.ToUpper(strings.TrimSpace(row.OfferID)), strings.ToUpper(strings.TrimSpace(row.Source))
	return strings.HasPrefix(id, "BYO-") || strings.Contains(id, "-BYO-") || source == "BYO" || strings.HasPrefix(source, "BYO ")
}

func migrateDellCustomRows(p *Product) {
	if siteName(p.URL) != "dell" {
		return
	}
	for i := range p.DellResults {
		if dellResultIsCustom(p.DellResults[i]) && !p.DellResults[i].Custom {
			p.DellResults[i].Custom = true
			p.DellResults[i].Matched = false
		}
	}
}

// Dell configuration options describe existing offers, not a Cartesian product.
// Keep unsuccessful/unknown observations internally for retries and diagnosis.
// Lenovo CTO retains its separate whole-device discount policy.
func customAvailableForProduct(p *Product, c DellResult) bool {
	return siteName(p.URL) != "dell" || (c.Confirmed && !c.Stale && c.Stock == stockIn && c.Price > 0)
}

func visibleModelResults(p *Product, rows []DellResult) []DellResult {
	visible := make([]DellResult, 0, len(rows))
	for _, row := range rows {
		if siteName(p.URL) == "dell" && dellResultIsCustom(row) {
			row.Custom = true
		}
		if row.Custom && siteName(p.URL) == "dell" && (p.Stale || !customAvailableForProduct(p, row)) {
			continue
		}
		visible = append(visible, row)
	}
	return visible
}

// The dashboard's preconfigured quote must not borrow a hidden custom quote.
func productForDisplay(p *Product) Product {
	q := *p
	q.History = nil
	q.DellResults = visibleModelResults(p, p.DellResults)
	if siteName(p.URL) != "dell" {
		return q
	}
	hasCustom := false
	var standard *DellResult
	rank := func(row DellResult) int {
		n := 3
		if row.Confirmed && row.Stock == stockIn {
			n = 1
		}
		if row.Matched {
			n = 0
		}
		if row.Stock == stockOut {
			n = 2
		}
		if row.Stale {
			n += 10
		}
		return n
	}
	for i := range p.DellResults {
		row := &p.DellResults[i]
		if dellResultIsCustom(*row) {
			hasCustom = true
			continue
		}
		if standard == nil || rank(*row) < rank(*standard) || (rank(*row) == rank(*standard) && row.Price > 0 && (standard.Price <= 0 || row.Price < standard.Price)) {
			standard = row
		}
	}
	if hasCustom {
		q.LastPrice, q.LastOriginal, q.LastDiscount = 0, 0, 0
		q.LastStock = ""
		if standard != nil {
			q.LastPrice, q.LastOriginal, q.LastDiscount, q.LastStock = standard.Price, standard.Original, standard.Discount, standard.Stock
		}
	}
	return q
}
