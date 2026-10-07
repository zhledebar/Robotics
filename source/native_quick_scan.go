package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"
)

const representativeSelectionLimit = 6
const representativeTimeBudget = 60 * time.Second

type coreTarget struct{ group, id, key string }

// Check actual one-step transitions, not the Cartesian product. Dell may
// change CPU/GPU/RAM together: record the complete resulting state, without
// restoring an imagined parent combination or computing a price from deltas.
func (b *NormalBrowser) scanRepresentativeCore(ctx context.Context, page nativePage, r nativeRequest, cursor int, progress func(string)) ([]DellResult, bool, int, error) {
	o, err := nativeObservation(page, r.URL, true)
	if err != nil {
		return nil, true, cursor, err
	}
	if len(o.Results) != 1 {
		return nil, true, cursor, &nativeQuoteFailure{Reason: "当前定制报价不唯一"}
	}
	rows := append([]DellResult(nil), o.Results...)
	index := map[string]int{rows[0].OfferID: 0}
	publishScanRows(ctx, rows)
	deadline := time.Now().Add(representativeTimeBudget)
	if outer, ok := ctx.Deadline(); ok && outer.Add(-10*time.Second).Before(deadline) {
		deadline = outer.Add(-10 * time.Second)
	}
	work, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var targets []coreTarget
	known := map[string]bool{}
	addTargets := func(p nativePage) {
		for _, g := range nativeCoreGroups(p) {
			for _, option := range g.Options {
				if !option.Enabled && !option.Selected {
					continue
				}
				key := nativeOptionKey(option.Name)
				identity := g.ID + "|" + option.ID + "|" + key
				if !known[identity] && key != "" {
					known[identity] = true
					if !option.Selected {
						targets = append(targets, coreTarget{g.ID, option.ID, key})
					}
				}
			}
		}
	}
	addTargets(page)
	if len(targets) == 0 {
		progress("当前定制已核对；没有其他可选核心选项")
		return rows, false, cursor, nil
	}
	if cursor < 0 {
		cursor = 0
	}
	start := cursor % len(targets)
	// Rotate the starting option across refreshes. This checks new options
	// without repeating the same six choices on every interval.
	targets = append(append([]coreTarget(nil), targets[start:]...), targets[:start]...)
	visited := map[string]bool{}
	attempts, positions, failures := 0, 0, 0
	var lastErr error
	for positions < len(targets) && attempts < representativeSelectionLimit {
		if work.Err() != nil {
			lastErr = &nativeQuoteFailure{Reason: "本轮代表配置检查预算已用完；已核对结果保留，其他选项分轮检查"}
			break
		}
		target := targets[positions]
		positions++
		group, ok := nativeGroupByID(page, target.group)
		if !ok {
			continue
		}
		if group.Header.Expanded != nil && !*group.Header.Expanded {
			expand := r
			expand.Action = "expand"
			expand.GroupID = target.group
			next, e := b.request(work, expand)
			if e == nil {
				next, e = b.stable(work, next, expand)
			}
			if e != nil {
				lastErr = e
				break
			}
			page = next
			group, ok = nativeGroupByID(page, target.group)
			if !ok {
				continue
			}
		}
		var choice *nativeNode
		for _, option := range group.Options {
			if (target.id != "" && option.ID == target.id || target.id == "" && nativeOptionKey(option.Name) == target.key) && nativeOptionKey(option.Name) == target.key {
				choice = option
				break
			}
		}
		if choice == nil || choice.Selected || !choice.Enabled {
			continue
		}
		before, _ := json.Marshal(nativeOptions(page.Regions["configuration-section"]))
		edge := string(before) + "|" + target.group + "|" + target.id + "|" + target.key
		if visited[edge] {
			continue
		}
		visited[edge] = true
		attempts++
		selectReq := r
		selectReq.Action = "select"
		selectReq.GroupID = target.group
		selectReq.OptionID = choice.ID
		selectReq.OptionName = choice.Name
		progress(fmt.Sprintf("代表配置检查 %d/%d；联动后按实际选中配置核价，未穷举", attempts, representativeSelectionLimit))
		next, e := b.request(work, selectReq)
		if e == nil {
			next, e = b.stableWhen(work, next, selectReq, func(p nativePage) bool {
				if nativeOptionKey(nativeOptions(p.Regions["configuration-section"])[target.group]) != target.key {
					return false
				}
				if choice.ID == "" {
					return true
				}
				actual, exists := nativeGroupByID(p, target.group)
				if !exists {
					return false
				}
				for _, x := range actual.Options {
					if x.Selected && x.ID == choice.ID {
						return true
					}
				}
				return false
			}, "代表配置选项切换未确认（"+nativeGroupLabel(page, target.group)+"）")
		}
		if e != nil {
			if work.Err() != nil {
				lastErr = &nativeQuoteFailure{Reason: "代表配置检查预算结束；已核对结果保留"}
				break
			}
			var quote *nativeQuoteFailure
			if !errors.As(e, &quote) {
				lastErr = e
				break
			}
			// Recover the current actual quote, never classify an ignored choice
			// as out of stock or repeatedly click it in this pass.
			recover := selectReq
			recover.Action = "snapshot"
			recover.ConfirmAction = "select"
			next, e = b.stable(work, next, recover)
			if e != nil {
				lastErr = e
				break
			}
			failures++
			log.Printf("representative transition unconfirmed: group=%s option=%s; actual state retained", target.group, target.id)
		}
		if nativeView(next) != "custom" {
			lastErr = &nativeQuoteFailure{Reason: "代表配置检查离开定制页面"}
			break
		}
		page = next
		actual, e := nativeObservation(page, r.URL, true)
		if e != nil || len(actual.Results) != 1 {
			lastErr = e
			if lastErr == nil {
				lastErr = &nativeQuoteFailure{Reason: "代表配置报价不唯一"}
			}
			break
		}
		row := actual.Results[0]
		row.Source = "普通浏览器：代表性实际配置"
		row.Note += "；按联动后的实际 CPU/显卡/内存/硬盘/屏幕记录；代表性检查未穷举"
		if old, ok := index[row.OfferID]; ok {
			rows[old] = row
		} else {
			index[row.OfferID] = len(rows)
			rows = append(rows, row)
		}
		publishScanRows(ctx, rows)
		addTargets(page)
	}
	if failures > 0 && lastErr == nil {
		lastErr = &nativeQuoteFailure{Reason: fmt.Sprintf("已核对 %d 个代表配置；%d 次选项切换未确认，未把旧配置当作成功", len(rows), failures)}
	}
	progress(fmt.Sprintf("代表配置已核对 %d 项；其他选项分轮检查，未穷举", len(rows)))
	log.Printf("representative core finished: rows=%d transitions=%d unresolved=%d next-cursor=%d", len(rows), attempts, failures, cursor+positions)
	return rows, lastErr != nil, cursor + positions, lastErr
}
