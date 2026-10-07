package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyBYORowsHiddenInsteadOfCountedAsOrdinary(t *testing.T) {
	p := product("legacy")
	p.URL, p.Stale = liveXPSURL, true
	legacy := availableCustom("BYO-legacy")
	legacy.Custom, legacy.Source, legacy.Stale = false, "BYO 已核价", true
	fixed := DellResult{OfferID: "da16260_fixed_71", Source: "预配置", Price: 2649.99, Stock: stockIn, Confirmed: true, Stale: true}
	p.DellResults = []DellResult{legacy, fixed}
	p.LastPrice = legacy.Price
	rows := visibleModelResults(p, p.DellResults)
	if len(rows) != 1 || rows[0].OfferID != fixed.OfferID {
		t.Fatal("legacy BYO leaked through ordinary display rules")
	}
	q := productForDisplay(p)
	if q.LastPrice != fixed.Price || len(q.DellResults) != 1 || p.DellResults[0].Custom {
		t.Fatal("legacy quote polluted summary or display mutated stored records")
	}
	p.Stale, legacy.Stale = false, false
	rows = visibleModelResults(p, []DellResult{legacy})
	if len(rows) != 1 || !rows[0].Custom {
		t.Fatal("fresh confirmed legacy BYO not classified as custom")
	}
	legacy.Stock = stockUnknown
	if len(visibleModelResults(p, []DellResult{legacy})) != 0 {
		t.Fatal("unknown legacy BYO displayed")
	}
}

func TestLegacyCustomMigrationOnRestartKeepsRecordsAndOrdinaryType(t *testing.T) {
	dir := t.TempDir()
	p := product("legacy")
	p.URL = liveXPSURL
	p.DellResults = []DellResult{
		{OfferID: "BYO-old-40", Source: "BYO 已核价", Price: 2249.99, Stock: stockIn, Confirmed: true, Matched: true},
		{OfferID: "da16260_reg_01", Source: "BYO 当前配置", Price: 2399.99, Stock: stockIn, Confirmed: true},
		{OfferID: "da16260_fixed_71", Source: "预配置", Price: 2649.99, Stock: stockIn, Confirmed: true},
		{OfferID: "da16260_reg_02", Source: "预配置", Price: 2999.99, Stock: stockIn, Confirmed: true},
	}
	b, _ := json.Marshal(Store{Products: []*Product{p}})
	if err := os.WriteFile(filepath.Join(dir, "monitor_data.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.shutdown()
	q := a.store.Products[0]
	if len(q.DellResults) != 4 || !q.DellResults[0].Custom || !q.DellResults[1].Custom || q.DellResults[2].Custom || q.DellResults[3].Custom {
		t.Fatal("old BYO type not migrated or ordinary reg URL classified as custom")
	}
	if q.DellResults[0].Matched || !q.DellResults[0].Stale || len(visibleModelResults(q, q.DellResults)) != 2 {
		t.Fatal("historical BYO still appears current")
	}
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(a.file)
	var saved Store
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Products[0].DellResults) != 4 || !saved.Products[0].DellResults[0].Custom {
		t.Fatal("migration not persisted")
	}
}

func TestExplicitBYOMarkersOnlyForDellMigration(t *testing.T) {
	p := product("lenovo-legacy")
	p.URL = vipP1Family
	p.DellResults = []DellResult{{OfferID: "BYO-legacy", Source: "BYO"}}
	migrateDellCustomRows(p)
	if p.DellResults[0].Custom {
		t.Fatal("Dell migration applied to Lenovo")
	}
	for _, id := range []string{"da16260_reg_01", "da16260_fixed_71", "da16260_so_8"} {
		if dellResultIsCustom(DellResult{OfferID: id}) {
			t.Fatal("ordinary SKU treated as BYO:", id)
		}
	}
}
