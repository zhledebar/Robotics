package main

import "context"

type scanObservationKey struct{}
type scanRowsKey struct{}

func publishScanRows(ctx context.Context, rows []DellResult) {
	if ctx.Err() == nil {
		if publish, ok := ctx.Value(scanRowsKey{}).(func([]DellResult)); ok {
			publish(append([]DellResult(nil), rows...))
		}
	}
}

// Progress updates publish verified current rows, without creating history or
// notifications before the final observation. Old absent rows remain stale.
func (a *App) applyScanProgress(ctx context.Context, id string, revision uint64, o Observation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.findLocked(id)
	if ctx.Err() != nil || p == nil || p.revision != revision || !p.Checking {
		return
	}
	seen := make(map[string]bool)
	rows := append([]DellResult(nil), o.Results...)
	for i := range rows {
		seen[rows[i].OfferID] = true
		rows[i].Matched = matches(p, rows[i])
	}
	for _, old := range p.DellResults {
		if !seen[old.OfferID] {
			old.Stale, old.Matched = true, false
			old.Note = "本轮未覆盖；上次结果"
			rows = append(rows, old)
		}
	}
	p.DellResults = rows
	p.Stale = false
	p.ScanIncomplete = true
	p.LastTrusted = false
}
