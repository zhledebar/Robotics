package main

import (
	"context"
	"time"
)

// A refresh batch commits every product's basic quote before starting the
// optional core pass. All passes still use the one verified owned window.
func (a *App) dispatchPendingCore() {
	a.mu.Lock()
	if a.ctx.Err() != nil || a.store.Settings.GlobalPaused {
		a.mu.Unlock()
		return
	}
	for _, p := range a.store.Products {
		if p.Checking || (p.Active && p.NeedsAction == "" && !time.Now().Before(p.nextCheck)) {
			a.mu.Unlock()
			return
		}
	}
	id := ""
	products := a.store.Products
	start := 0
	for i, p := range products {
		if p.ID == a.coreLastID && len(products) > 0 {
			start = (i + 1) % len(products)
			break
		}
	}
	// Rotate across products with outstanding configuration work. Otherwise
	// a long-running XPS scan at the top of the product list can starve Pro.
	for offset := 0; offset < len(products); offset++ {
		p := products[(start+offset)%len(products)]
		if revision, ok := a.corePending[p.ID]; ok {
			delete(a.corePending, p.ID)
			p.CorePending = false
			if revision == p.revision && p.NeedsAction == "" {
				id = p.ID
				a.coreLastID = id
				break
			}
		}
	}
	a.mu.Unlock()
	if id != "" {
		a.scheduleWith(id, func(ctx context.Context, p Product, progress func(string)) (Observation, error) {
			p.corePhase = true
			progress("各商品报价已更新；检查代表配置")
			o, err := a.fetch(ctx, p, progress)
			if err == nil && o.CorePending && ctx.Err() == nil {
				a.mu.Lock()
				if current := a.findLocked(p.ID); current != nil && current.revision == p.revision {
					a.corePending[p.ID] = p.revision
					current.CorePending = true
				}
				a.mu.Unlock()
			}
			return o, err
		})
	}
}
