package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Only an unconfirmed selection with a freshly recovered stable owned page
// can be bypassed. Transport, ownership, popup and loading failures still stop.
type nativeBranchFailure struct{ quote *nativeQuoteFailure }

func (e *nativeBranchFailure) Error() string { return e.quote.Error() }
func (e *nativeBranchFailure) Unwrap() error { return e.quote }

type nativeCoreGroup struct {
	ID, Label string
	Header    *nativeNode
	Options   []*nativeNode
}

var nativeOptionSuffix = regexp.MustCompile(`(?i)(?:[.]\s*)?(?:Selected|[+−–-]?\s*\$\s*[0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*$`)

func nativeOptionKey(name string) string {
	value := normalSpace(name)
	for i := 0; i < 3; i++ {
		next := strings.TrimSpace(strings.TrimSuffix(nativeOptionSuffix.ReplaceAllString(value, ""), "."))
		if next == value {
			break
		}
		value = next
	}
	return value
}
func nativeCoreLabel(name string) string {
	s := strings.ToLower(normalSpace(name))
	for _, label := range []string{"processor", "graphics card", "memory", "storage", "displays", "display"} {
		if s == label || strings.HasPrefix(s, label+" ") {
			return label
		}
	}
	return ""
}
func nativeCoreGroups(page nativePage) []nativeCoreGroup {
	root := page.Regions["configuration-section"]
	byID := map[string]*nativeNode{}
	headers := map[string]*nativeNode{}
	walkNative(root, func(n *nativeNode) {
		if n.ID != "" {
			byID[n.ID] = n
		}
		if strings.HasPrefix(n.ID, "label-module") && nativeCoreLabel(n.Name) != "" {
			headers[strings.TrimPrefix(n.ID, "label-")] = n
		}
	})
	var groups []nativeCoreGroup
	for id, h := range headers {
		g := nativeCoreGroup{ID: id, Label: nativeCoreLabel(h.Name), Header: h}
		var readChoices func(*nativeNode)
		readChoices = func(n *nativeNode) {
			if n == nil {
				return
			}
			if nativeChoiceNode(n) {
				if n.Name != "" && (n.Enabled || n.Selected) {
					g.Options = append(g.Options, n)
				}
				return
			}
			for _, child := range n.Children {
				readChoices(child)
			}
		}
		readChoices(byID[id])
		groups = append(groups, g)
	}
	priority := map[string]int{"processor": 0, "graphics card": 1, "memory": 2, "storage": 3, "display": 4, "displays": 4}
	sort.Slice(groups, func(i, j int) bool {
		if priority[groups[i].Label] != priority[groups[j].Label] {
			return priority[groups[i].Label] < priority[groups[j].Label]
		}
		return groups[i].ID < groups[j].ID
	})
	return groups
}
func nativeGroupByID(page nativePage, id string) (nativeCoreGroup, bool) {
	for _, g := range nativeCoreGroups(page) {
		if g.ID == id {
			return g, true
		}
	}
	return nativeCoreGroup{}, false
}

// Each leaf uses the actual current entire-machine quote and actual selection.
// Price deltas are removed only from option names, never used as price arithmetic.
func (b *NormalBrowser) scanCustomCore(ctx context.Context, page nativePage, r nativeRequest, progress func(string)) ([]DellResult, bool, error) {
	initial, e := nativeObservation(page, r.URL, true)
	if e != nil {
		return nil, true, e
	}
	rows := append([]DellResult(nil), initial.Results...)
	publishScanRows(ctx, rows)
	seen := map[string]bool{}
	index := map[string]int{}
	for i, row := range rows {
		seen[row.OfferID] = true
		index[row.OfferID] = i
	}
	groups := nativeCoreGroups(page)
	if len(groups) == 0 || len(rows) != 1 {
		return rows, true, &nativeQuoteFailure{Reason: "定制核心选项未完整绑定；仅取得当前配置"}
	}
	// End traversal before the outer check deadline, so its verified partial
	// rows can be committed instead of all being discarded by scheduleWith.
	deadline := time.Now().Add(13 * time.Minute)
	if outer, ok := ctx.Deadline(); ok && outer.Add(-10*time.Second).Before(deadline) {
		deadline = outer.Add(-10 * time.Second)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var ids []string
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	// CPU first: later option sets are read again after preceding selections.
	path := map[string]string{}
	steps := 0
	basePass := true
	hasExtra := false
	completedBranches := map[string]bool{}
	lastVariableLevel := -1
	for i, g := range groups {
		if g.Label == "memory" || g.Label == "graphics card" {
			lastVariableLevel = i
		}
	}
	branchSignature := func() string {
		selections := nativeOptions(page.Regions["configuration-section"])
		for _, g := range groups {
			if g.Label == "storage" || g.Label == "display" || g.Label == "displays" {
				delete(selections, g.ID)
			}
		}
		for id, name := range selections {
			selections[id] = nativeOptionKey(name)
		}
		raw, _ := json.Marshal(selections)
		return string(raw)
	}
	unresolved := []string{}
	recordUnresolved := func(reason string) {
		unresolved = append(unresolved, reason)
		log.Printf("native core branch deferred: %s", reason)
	}
	change := func(action, group, option string) error {
		steps++
		if steps > 600 {
			return &nativeQuoteFailure{Reason: "定制组合操作达到本轮上限；已核对结果保留"}
		}
		req := r
		req.Action = action
		req.GroupID = group
		req.OptionName = option
		if action == "select" {
			if current, ok := nativeGroupByID(page, group); ok {
				for _, choice := range current.Options {
					if choice.Name == option {
						req.OptionID = choice.ID
					}
				}
			}
		}
		next, err := b.request(ctx, req)
		if err != nil {
			return err
		}
		if action == "select" {
			key := nativeOptionKey(option)
			// Some Chromium providers expose actionable cards without selection
			// state. Collapse this exact module to expose Dell's current-value
			// summary; the requested value is still checked by stableWhen.
			if !next.DOMConfiguration && nativeOptionKey(nativeOptions(next.Regions["configuration-section"])[group]) != key {
				verify := req
				verify.Action = "collapse"
				collapsed, collapseErr := b.request(ctx, verify)
				if collapseErr == nil {
					next = collapsed
				} else if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			next, err = b.stableWhen(ctx, next, req, func(p nativePage) bool {
				if nativeOptionKey(nativeOptions(p.Regions["configuration-section"])[group]) != key {
					return false
				}
				if req.OptionID == "" {
					return true
				}
				actual, ok := nativeGroupByID(p, group)
				if !ok {
					return false
				}
				for _, choice := range actual.Options {
					if choice.Selected && choice.ID == req.OptionID {
						return true
					}
				}
				return false
			}, "核心选项切换未确认（"+nativeGroupLabel(page, group)+"）")
		} else {
			next, err = b.stable(ctx, next, req)
		}
		if err != nil {
			var quote *nativeQuoteFailure
			reason := "核心选项切换未确认（" + nativeGroupLabel(page, group) + "）；未把旧配置当成切换成功"
			if action != "select" || !errors.As(err, &quote) || quote.Reason != reason {
				return err
			}
			// The requested selection was never accepted. Obtain two matching current
			// whole-machine quotes, still checking the failed option's owned popup.
			recoverReq := req
			recoverReq.Action = "snapshot"
			recoverReq.ConfirmAction = "select"
			actual, recoverErr := b.stable(ctx, next, recoverReq)
			if recoverErr != nil {
				return recoverErr
			}
			if nativeView(actual) != "custom" {
				return &nativeQuoteFailure{Reason: "分支恢复离开定制视图"}
			}
			page = actual
			return &nativeBranchFailure{quote: quote}
		}
		if nativeView(next) != "custom" {
			return &nativeQuoteFailure{Reason: "配置切换离开定制视图"}
		}
		page = next
		return nil
	}
	ensureExpanded := func(id string) (nativeCoreGroup, error) {
		g, ok := nativeGroupByID(page, id)
		if !ok {
			return g, &nativeQuoteFailure{Reason: "核心选项区域在配置切换后缺失"}
		}
		if g.Header.Expanded == nil || !*g.Header.Expanded || len(g.Options) == 0 {
			if err := change("expand", id, ""); err != nil {
				return g, err
			}
			g, ok = nativeGroupByID(page, id)
			if !ok || len(g.Options) == 0 {
				return g, &nativeQuoteFailure{Reason: "核心配置展开后仍未读到选项（" + nativeGroupLabel(page, id) + "）"}
			}
			if g.Header.Expanded != nil && !*g.Header.Expanded {
				return g, &nativeQuoteFailure{Reason: "核心配置没有完成展开"}
			}
		}
		return g, nil
	}
	// Dell may replace an earlier choice when accepting a later one. Restore
	// the preceding path in order before trying another sibling, never label
	// that changed configuration as the requested combination.
	restorePath := func(level int) error {
		for _, priorID := range ids[:level] {
			key := path[priorID]
			if key == "" || nativeOptionKey(nativeOptions(page.Regions["configuration-section"])[priorID]) == key {
				continue
			}
			current, err := ensureExpanded(priorID)
			if err != nil {
				return err
			}
			var target *nativeNode
			for _, opt := range current.Options {
				if nativeOptionKey(opt.Name) == key {
					target = opt
					break
				}
			}
			if target == nil || (!target.Enabled && !target.Selected) {
				return &nativeQuoteFailure{Reason: "此前核心配置无法恢复；未继续使用变更后的配置"}
			}
			if err := change("select", priorID, target.Name); err != nil {
				return err
			}
		}
		for _, priorID := range ids[:level] {
			if key := path[priorID]; key != "" && nativeOptionKey(nativeOptions(page.Regions["configuration-section"])[priorID]) != key {
				return &nativeQuoteFailure{Reason: "此前核心配置恢复后仍不一致"}
			}
		}
		return nil
	}
	var visit func(int) error
	visit = func(level int) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(rows) >= 250 {
			return &nativeQuoteFailure{Reason: "定制组合达到 250 项本轮上限；仍有未检查组合"}
		}
		if level == len(ids) {
			o, err := nativeObservation(page, r.URL, true)
			if err != nil {
				return err
			}
			if len(o.Results) != 1 {
				return &nativeQuoteFailure{Reason: "所选配置未完整核对"}
			}
			row := o.Results[0]
			for id, key := range path {
				if nativeOptionKey(row.Options[id]) != key {
					return &nativeQuoteFailure{Reason: "所选配置与本轮组合不一致；未接受该组合报价"}
				}
			}
			row.Source = "普通浏览器：核心定制组合"
			row.Note = "实际核对 CPU、显卡、内存、硬盘、屏幕核心组合；仅按整机折扣提醒"
			if !row.DiscountConfirmed {
				row.Note += "；折扣待公布"
			}
			if row.ConfigUnbound {
				row.Note += "；其他选项未完整绑定，不判断同配置折扣提高"
			}
			bindNativeCoreSpecs(&row, page)
			if seen[row.OfferID] {
				rows[index[row.OfferID]] = row
			} else {
				seen[row.OfferID] = true
				index[row.OfferID] = len(rows)
				rows = append(rows, row)
				progress(fmt.Sprintf("定制核心组合已核对 %d 项；仍在扫描", len(rows)))
			}
			publishScanRows(ctx, rows)
			return nil
		}
		id := ids[level]
		g, err := ensureExpanded(id)
		if err != nil {
			return err
		}
		keys := []string{}
		selectedKey := ""
		unique := map[string]bool{}
		for _, opt := range g.Options {
			key := nativeOptionKey(opt.Name)
			if key == "" || unique[key] {
				return &nativeQuoteFailure{Reason: "核心选项名称不唯一；没有猜测选项"}
			}
			unique[key] = true
			keys = append(keys, key)
			if opt.Selected {
				selectedKey = key
			}
		}
		// First cover CPU x SSD x display with Dell's currently selected GPU/RAM.
		// Start leaf groups at their actual current selection. Traversal finishes
		// at the opposite end, so the next SSD/CPU avoids resetting the screen.
		// All options still receive their own verified whole-machine quote.
		if g.Label == "display" || g.Label == "displays" || g.Label == "storage" {
			sort.SliceStable(keys, func(i, j int) bool { return keys[i] == selectedKey && keys[j] != selectedKey })
		}
		// A RAM option which the website silently refuses cannot block later CPUs.
		if g.Label == "memory" || g.Label == "graphics card" {
			if len(keys) > 1 {
				hasExtra = true
			}
			if selectedKey == "" && len(keys) == 1 && !page.DOMConfiguration {
				selectedKey = keys[0]
			}
			if selectedKey == "" {
				return &nativeQuoteFailure{Reason: "显卡或内存当前选项未绑定"}
			}
			if basePass {
				keys = []string{selectedKey}
			} else {
				sort.SliceStable(keys, func(i, j int) bool { return keys[i] == selectedKey && keys[j] != selectedKey })
			}
		}
		for _, key := range keys {
			if err := restorePath(level); err != nil {
				return err
			}
			current, err := ensureExpanded(id)
			if err != nil {
				return err
			}
			var target *nativeNode
			for _, opt := range current.Options {
				if nativeOptionKey(opt.Name) == key {
					target = opt
					break
				}
			}
			if target == nil || (!target.Enabled && !target.Selected) {
				continue
			} // dependent/incompatible branch
			if !target.Selected {
				if err := change("select", id, target.Name); err != nil {
					var branch *nativeBranchFailure
					if !errors.As(err, &branch) {
						return err
					}
					recordUnresolved(fmt.Sprintf("%s / %s：%s", nativeGroupLabel(page, id), key, err))
					continue
				}
			}
			ancestorsMatch := true
			for priorID, priorKey := range path {
				if nativeOptionKey(nativeOptions(page.Regions["configuration-section"])[priorID]) != priorKey {
					ancestorsMatch = false
					break
				}
			}
			if !ancestorsMatch {
				recordUnresolved(fmt.Sprintf("%s / %s 改变了此前核心选项；未接受为原组合", nativeGroupLabel(page, id), key))
				continue
			}
			options := nativeOptions(page.Regions["configuration-section"])
			if nativeOptionKey(options[id]) != key {
				return &nativeQuoteFailure{Reason: "核心选项切换未得到实际所选值确认（" + nativeGroupLabel(page, id) + "）"}
			}
			path[id] = key
			signature := ""
			if level == lastVariableLevel {
				signature = branchSignature()
				if !basePass && completedBranches[signature] {
					delete(path, id)
					continue
				}
			}
			beforeUnresolved := len(unresolved)
			if err := visit(level + 1); err != nil {
				return err
			}
			if basePass && signature != "" && beforeUnresolved == len(unresolved) {
				completedBranches[signature] = true
			}
			delete(path, id)
		}
		return nil
	}
	e = visit(0)
	if e == nil && hasExtra {
		progress(fmt.Sprintf("处理器、硬盘和屏幕组合已核对 %d 项；继续检查其他显卡及内存选项", len(rows)))
		log.Printf("native core baseline finished: verified=%d; exploring GPU/RAM", len(rows))
		basePass = false
		e = visit(0)
	}
	if e == nil && len(unresolved) > 0 {
		e = &nativeQuoteFailure{Reason: fmt.Sprintf("已核对 %d 项；另有 %d 个分支未确认（%s）；剩余可读取组合已继续检查", len(rows), len(unresolved), unresolved[0])}
	}
	if e != nil {
		// Content/selection incompleteness remains retryable; ownership, denial and
		// navigation failures keep their hard failure classification.
		var quote *nativeQuoteFailure
		if !errors.As(e, &quote) && !hardBrowserContentFailure(e.Error()) && !strings.Contains(e.Error(), "窗口") && !strings.Contains(e.Error(), "Windows 界面读取组件") && !strings.Contains(e.Error(), "已切换网页") && !strings.Contains(e.Error(), "其他标签") {
			e = &nativeQuoteFailure{Reason: "定制组合扫描未完成：" + e.Error()}
		}
	}
	return rows, e != nil, e
}
