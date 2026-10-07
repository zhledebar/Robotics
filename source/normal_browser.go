package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// No extension. Only a window opened by this application may be
// refreshed or clicked; a changed URL/tab or access-denied page stops the round.
type nativeNode struct {
	Name        string        `json:"name"`
	ID          string        `json:"id"`
	Class       string        `json:"class"`
	Kind        string        `json:"kind"`
	Enabled     bool          `json:"enabled"`
	Selected    bool          `json:"selected"`
	ReadingText string        `json:"reading_text,omitempty"`
	Expanded    *bool         `json:"expanded,omitempty"`
	Children    []*nativeNode `json:"children"`
}

// A fallback quote uses the current visible DOM total only after multiple
// matching owned snapshots. UIA-derived original price and stock are withheld.
type nativeDOMQuote struct{ Price float64 }
type nativePage struct {
	DOMQuote         *nativeDOMQuote        `json:"-"`
	DOMStock         string                 `json:"-"`
	ResetVerified    bool                   `json:"-"`
	DOMConfiguration bool                   `json:"-"`
	Handle           int64                  `json:"handle"`
	URL              string                 `json:"url"`
	Title            string                 `json:"title"`
	Error            string                 `json:"error"`
	Diagnostic       string                 `json:"diagnostic,omitempty"`
	Launched         bool                   `json:"launched,omitempty"`
	Regions          map[string]*nativeNode `json:"regions"`
	CanOrdinary      bool                   `json:"can_ordinary"`
	CanCustom        bool                   `json:"can_custom"`
}
type nativeRequest struct {
	ConfirmAction string  `json:"confirm_action,omitempty"`
	GroupID       string  `json:"group_id,omitempty"`
	OptionName    string  `json:"option_name,omitempty"`
	OptionID      string  `json:"option_id,omitempty"`
	DOMPhase      string  `json:"dom_phase,omitempty"`
	PointX        float64 `json:"point_x,omitempty"`
	PointY        float64 `json:"point_y,omitempty"`
	Owner         string  `json:"owner"`
	Action        string  `json:"action"`
	Handle        int64   `json:"handle"`
	URL           string  `json:"url"`
	Family        bool    `json:"family"`
	EXE           string  `json:"exe"`
}
type nativeCaller func(context.Context, nativeRequest) (nativePage, error)
type NormalBrowser struct {
	gate                 chan struct{}
	window               nativeWindow
	used                 bool
	uncertain            bool
	uncertainReason      string
	reset                func(context.Context) error
	idleTTL              time.Duration
	idleTimer            *time.Timer
	generation           uint64
	pollDelay            time.Duration
	selectionStallBudget time.Duration
	scanCore             bool
	call                 nativeCaller
	available            bool
}

type nativeWindow struct {
	Handle int64
	Owner  string
	URL    string
}

func newNormalBrowser() *NormalBrowser {
	b := &NormalBrowser{gate: make(chan struct{}, 1), pollDelay: 750 * time.Millisecond, selectionStallBudget: 10 * time.Second, scanCore: true, call: runNativeReader, available: nativeReaderAvailable()}
	if b.available {
		b.idleTTL = 20 * time.Second
		b.reset = resetNativeCollector
	}
	return b
}

func (b *NormalBrowser) stopIdleLocked() {
	b.generation++
	if b.idleTimer != nil {
		b.idleTimer.Stop()
		b.idleTimer = nil
	}
}

func (b *NormalBrowser) scheduleIdleLocked() {
	if b.idleTTL <= 0 || b.window.Handle <= 0 {
		return
	}
	generation := b.generation
	b.idleTimer = time.AfterFunc(b.idleTTL, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		select {
		case b.gate <- struct{}{}:
			defer func() { <-b.gate }()
		case <-ctx.Done():
			return
		}
		if b.generation != generation {
			return
		}
		b.idleTimer = nil
		if err := b.closeWindow(ctx); err != nil {
			log.Printf("idle collector cleanup: %v", err)
		}
	})
}

// The gate protects one shared window slot across every Dell product. A new
// target cannot open until the previous owned window has actually closed.
func (b *NormalBrowser) closeWindow(ctx context.Context) error {
	if b.window.Handle <= 0 {
		b.window = nativeWindow{}
		return nil
	}
	r := nativeRequest{Action: "close", Handle: b.window.Handle, Owner: b.window.Owner, URL: b.window.URL, Family: true}
	p, err := b.call(ctx, r)
	if p.ResetVerified {
		b.window = nativeWindow{}
		b.uncertain = false
		b.uncertainReason = ""
	}
	if err != nil {
		return err
	}
	if err = normalPageError(p); err != nil {
		return err
	}
	if p.Handle != 0 {
		return errors.New("旧采集窗口尚未关闭；没有打开更多窗口")
	}
	b.window = nativeWindow{}
	return nil
}

func (b *NormalBrowser) close(ctx context.Context) error {
	select {
	case b.gate <- struct{}{}:
		defer func() { <-b.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if !b.used {
		return nil
	}
	b.stopIdleLocked()
	owner := b.window.Owner
	if err := b.closeWindow(ctx); err != nil {
		return err
	}
	// Also reclaim a tagged window if an interrupted launch never returned its
	// handle. The helper skips untagged windows and windows containing user tabs.
	if owner != "" {
		p, err := b.call(ctx, nativeRequest{Action: "cleanup", Owner: owner})
		if err != nil {
			return err
		}
		if err := normalPageError(p); err != nil {
			return err
		}
	}
	b.used = false
	b.uncertain = false
	b.uncertainReason = ""
	return nil
}
func dellPlatform(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || strings.ToLower(u.Host) != "www.dell.com" || u.User != nil || !strings.HasPrefix(u.Path, "/en-us/shop/") {
		return ""
	}
	s := strings.Split(u.Path, "/")
	for i, p := range s {
		if p == "spd" && i+1 < len(s) {
			return strings.ToLower(s[i+1])
		}
	}
	return ""
}
func normalURLMatches(requested, current string, family bool) bool {
	if dellPlatform(requested) == "" || dellPlatform(requested) != dellPlatform(current) {
		return false
	}
	if family {
		return true
	}
	return offerFromURL(requested) != "" && offerFromURL(requested) == offerFromURL(current)
}
func normalPageError(p nativePage) error {
	if p.Error != "" {
		if p.ResetVerified {
			return &nativeRecoveredFailure{Reason: p.Error}
		}
		return errors.New(p.Error)
	}
	s := strings.ToLower(p.Title)
	if strings.Contains(s, "access denied") || strings.Contains(s, "verify you are human") || strings.Contains(s, "checking your browser") {
		return errors.New("普通浏览器网页访问被限制（access denied）；本轮停止，旧报价不参与提醒")
	}
	return nil
}
func (b *NormalBrowser) request(ctx context.Context, r nativeRequest) (nativePage, error) {
	if strings.ContainsAny(r.URL, "\"\\\r\n\x00") {
		return nativePage{}, errors.New("商品网址含无效字符；没有启动浏览器")
	}
	if e := ctx.Err(); e != nil {
		return nativePage{}, e
	}
	p, e := b.call(ctx, r)
	if p.ResetVerified {
		b.window = nativeWindow{}
		b.uncertain = false
		b.uncertainReason = ""
	}
	if ctx.Err() != nil {
		return nativePage{}, ctx.Err()
	}
	if e != nil {
		return p, e
	}
	if e = normalPageError(p); e != nil {
		return p, e
	}
	if p.Handle <= 0 || !normalURLMatches(r.URL, p.URL, r.Family) {
		return p, errors.New("普通浏览器窗口或机型已变化；没有读取其他页面")
	}
	return p, nil
}
func nativeSignature(p nativePage, o Observation) string {
	// Compare the monitored quote, not source labels, notes, URL fragments or
	// delivery prose. Dell custom availability now gates display and alerts too.
	type quote struct {
		ID                                 string
		Price, Original                    float64
		Custom, DiscountConfirmed, Unbound bool
		Stock                              string
		Confirmed                          bool
		CPU, GPU, Memory, Storage, Display string
		Options                            map[string]string
	}
	var rows []quote
	for _, c := range o.Results {
		q := quote{ID: c.OfferID, Price: c.Price, Original: c.Original, Custom: c.Custom, DiscountConfirmed: c.DiscountConfirmed, Unbound: c.ConfigUnbound, Options: c.Options}
		q.Stock, q.Confirmed = c.Stock, c.Confirmed
		if !c.Custom {
			q.CPU, q.GPU, q.Memory, q.Storage, q.Display = normalSpace(c.CPU), normalSpace(c.GPU), normalSpace(c.Memory), normalSpace(c.Storage), normalSpace(c.Display)
		}
		rows = append(rows, q)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	v, _ := json.Marshal(rows)
	return dellPlatform(p.URL) + nativeView(p) + string(v)
}

type nativeQuoteFailure struct{ Reason string }

func (e *nativeQuoteFailure) Error() string { return e.Reason + "；下轮自动重验" }

func nativeViewFailureNote(err error) string {
	var quoteFailure *nativeQuoteFailure
	if errors.As(err, &quoteFailure) {
		return err.Error()
	}
	return "普通浏览器自动采集失败：" + err.Error()
}

func (b *NormalBrowser) stable(ctx context.Context, p nativePage, r nativeRequest) (nativePage, error) {
	if r.Action == "ordinary" || r.Action == "custom" {
		wanted := r.Action
		return b.stableWhen(ctx, p, r, func(next nativePage) bool { return nativeView(next) == wanted }, "配置入口未完成视图切换")
	}
	return b.stableWhen(ctx, p, r, nil, "")
}

// Selection must reach the requested value before a stable quote can be accepted.
// Two unchanged snapshots of the old configuration are not selection evidence.
func (b *NormalBrowser) stableWhen(ctx context.Context, p nativePage, r nativeRequest, ready func(nativePage) bool, reason string) (nativePage, error) {
	if r.Action == "select" || r.Action == "ordinary" || r.Action == "custom" {
		r.ConfirmAction = r.Action
	}
	last := ""
	var lastParse error
	parsed := false
	rejectedSignature := ""
	rejectedSince := time.Now()
	rejectedReads := 0
	for i := 0; i < 8; i++ {
		if e := normalPageError(p); e != nil {
			return p, e
		}
		if o, e := nativeObservation(p, r.URL, r.Family); e == nil {
			parsed = true
			lastParse = nil
			sig := nativeSignature(p, o)
			if ready != nil && !ready(p) {
				last = ""
				if sig != rejectedSignature {
					rejectedSignature, rejectedSince, rejectedReads = sig, time.Now(), 1
				} else {
					rejectedReads++
				}
				group, _ := nativeGroupByID(p, r.GroupID)
				// A settled unchanged memory selection is a failed branch, never
				// an accepted old quote. Allow delayed transitions, then continue
				// other branches instead of spending eight expensive read rounds.
				if r.ConfirmAction == "select" && p.DOMConfiguration && group.Label == "memory" && b.selectionStallBudget > 0 && rejectedReads >= 3 && time.Since(rejectedSince) >= b.selectionStallBudget {
					log.Printf("native memory selection stalled: group=%s wanted-id=%s elapsed=%s reads=%d", r.GroupID, r.OptionID, time.Since(rejectedSince).Round(time.Millisecond), rejectedReads)
					break
				}
			} else {
				rejectedSignature, rejectedReads = "", 0
				if sig == last {
					return p, nil
				}
				last = sig
			}
		} else {
			lastParse = e
			last = ""
			rejectedSignature, rejectedReads = "", 0
		}
		if i == 7 {
			break
		}
		if e := pauseContext(ctx, b.pollDelay); e != nil {
			return p, e
		}
		r.Action = "snapshot"
		r.Handle = p.Handle
		var e error
		p, e = b.request(ctx, r)
		if e != nil {
			return p, e
		}
	}
	if ready != nil && !ready(p) {
		group, _ := nativeGroupByID(p, r.GroupID)
		detail, _ := json.Marshal(group)
		var selectedIDs []string
		for _, choice := range group.Options {
			if choice.Selected {
				selectedIDs = append(selectedIDs, choice.ID)
			}
		}
		log.Printf("native selection unresolved: action=%s view=%s group=%s label=%q expected=%q expected-id=%q selected-ids=%q actual=%q scope=%s dom=%s", r.ConfirmAction, nativeView(p), r.GroupID, nativeGroupLabel(p, r.GroupID), nativeOptionKey(r.OptionName), r.OptionID, selectedIDs, nativeOptions(p.Regions["configuration-section"])[r.GroupID], nativeDiagnosticTail(detail), nativeDiagnosticTail([]byte(p.Diagnostic)))
		return p, &nativeQuoteFailure{Reason: reason + "；未把旧配置当成切换成功"}
	}
	// Keep the actual failure visible; a parsing failure is not a changing quote.
	if lastParse != nil {
		detail, _ := json.Marshal(p)
		log.Printf("native quote unavailable: %v; scoped snapshot=%s", lastParse, detail)
		return p, &nativeQuoteFailure{Reason: "商品报价未识别：" + lastParse.Error()}
	}
	if parsed {
		log.Printf("native quote changed across snapshots; model=%s view=%s final=%s", dellPlatform(p.URL), nativeView(p), last)
	}
	return p, &nativeQuoteFailure{Reason: "报价、库存或所选配置连续变化；本轮未接受报价"}
}
func (b *NormalBrowser) collect(ctx context.Context, p Product, progress func(string)) (Observation, error) {
	progress("等待共用采集窗口；尚未读取商品页面")
	select {
	case b.gate <- struct{}{}:
		defer func() { b.scheduleIdleLocked(); <-b.gate }()
	case <-ctx.Done():
		return Observation{}, ctx.Err()
	}
	b.stopIdleLocked()
	if strings.ContainsAny(p.URL, "\"\\\r\n\x00") {
		return Observation{}, errors.New("商品网址含无效字符；没有启动浏览器")
	}
	if b.uncertain {
		if b.reset == nil {
			return Observation{}, fmt.Errorf("上次后台采集失败：%s；窗口状态未确认；没有打开更多窗口", b.uncertainReason)
		}
		cleanCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		cleanErr := b.reset(cleanCtx)
		cancel()
		if cleanErr != nil {
			return Observation{}, fmt.Errorf("上次后台采集失败：%s；回收未确认：%v；没有打开更多窗口", b.uncertainReason, cleanErr)
		}
		b.window = nativeWindow{}
		b.uncertain = false
		b.uncertainReason = ""
		progress("已确认回收软件后台进程；重新建立采集窗口")
	}
	if b.window.URL != "" && b.window.URL != p.URL {
		progress("轮流检查商品；先释放上一个采集窗口")
		if err := b.closeWindow(ctx); err != nil {
			return Observation{}, err
		}
	}
	h := b.window.Handle
	owner := b.window.Owner
	if owner == "" {
		owner = newID()
	}
	b.window.Owner, b.window.URL = owner, p.URL
	b.used = true
	r := nativeRequest{Owner: owner, Action: "open", Handle: h, URL: p.URL, Family: p.DellFamilyScan, EXE: normalBrowserExecutable()}
	if h > 0 {
		r.Action = "refresh"
	}
	progress("正在后台读取商品报价（无需扩展，不抢前台焦点）")
	page, e := b.request(ctx, r)
	if page.Handle > 0 {
		b.window.Handle = page.Handle // Keep ownership even if capture failed.
	}
	if r.Action == "open" && e != nil && !page.ResetVerified && page.Handle <= 0 && (page.Launched || page.Error == "") {
		b.uncertain = true
		b.uncertainReason = e.Error()
	}
	if e != nil {
		// Only a definitely closed owned window may be recreated. A denied page or
		// changed live tab never causes a retry, navigation, or alternate route.
		if h > 0 && strings.Contains(e.Error(), "监控商品窗口已关闭") {
			r.Handle = 0
			r.Action = "open"
			page, e = b.request(ctx, r)
			if page.Handle > 0 {
				b.window.Handle = page.Handle
			}
			if e != nil && !page.ResetVerified && page.Handle <= 0 && (page.Launched || page.Error == "") {
				b.uncertain = true
				b.uncertainReason = e.Error()
			}
		}
		if e != nil {
			return Observation{}, e
		}
	}
	b.window.Handle = page.Handle
	r.Handle = page.Handle
	page, e = b.stable(ctx, page, r)
	var first Observation
	var firstErr error = e
	if firstErr == nil {
		first, firstErr = nativeObservation(page, p.URL, p.DellFamilyScan)
	}
	if firstErr != nil {
		var quoteFailure *nativeQuoteFailure
		// Only a quote/content failure may switch the same owned product view.
		// Access-denied, changed URL/window and canceled reads stop immediately.
		if !p.DellFamilyScan || !errors.As(firstErr, &quoteFailure) {
			return Observation{}, firstErr
		}
	}
	out := first
	var customPage *nativePage
	currentView := nativeView(page)
	scanAllowed := true
	if firstErr == nil && nativeView(page) == "custom" {
		cp := page
		customPage = &cp
	}
	if p.corePhase && firstErr == nil && currentView == "custom" {
		out.CoreOnly = true
	}
	if p.DellFamilyScan {
		action := ""
		if p.corePhase {
			// A representative slice only needs the configurator. Switching to
			// ordinary offers and back adds two expensive page waits.
			if currentView != "custom" && page.CanCustom {
				action = "custom"
			}
		} else if page.CanOrdinary {
			action = "ordinary"
		} else if page.CanCustom {
			action = "custom"
		}
		if action != "" {
			if p.corePhase {
				progress("进入定制配置；核对一项代表配置")
			} else {
				progress("自动读取普通配置与定制款；随后核对核心配置组合")
			}
			r.Action = action
			other, err := b.request(ctx, r)
			if err == nil {
				currentView = nativeView(other)
			}
			if err == nil {
				other, err = b.stable(ctx, other, r)
				if err == nil {
					currentView = nativeView(other)
				}
			}
			var second Observation
			if err == nil {
				second, err = nativeObservation(other, p.URL, true)
			}
			if err != nil {
				var quoteFailure *nativeQuoteFailure
				if !errors.As(err, &quoteFailure) {
					scanAllowed = false
				}
				if firstErr != nil {
					return Observation{}, fmt.Errorf("当前视图：%v；另一类配置：%w", firstErr, err)
				}
				out.Partial = true
				out.Note = "当前页面已读取；另一类配置未确认：" + nativeViewFailureNote(err)
			} else {
				// A control that did not change the view must never turn cached quotes into
				// independent ordinary/custom observations.
				if nativeView(page) == nativeView(other) {
					out.Partial = true
					out.Note = "普通浏览器自动采集失败：配置入口未完成视图切换；未覆盖配置保留为上次数据"
				} else if p.corePhase {
					if nativeView(other) != "custom" {
						out.Partial = true
						out.Note = "代表配置检查未进入定制配置页面；下轮重新检查"
					} else {
						out = second
						out.CoreOnly = true
						customPage = &other
					}
				} else {
					if firstErr != nil {
						out = second
						out.Partial = true
						out.Note = "已核对另一类配置；初始视图未确认：" + nativeViewFailureNote(firstErr)
					} else {
						out.Results = append(out.Results, second.Results...)
					}
					if nativeView(other) == "custom" {
						cp := other
						customPage = &cp
					}
				}
			}
		} else if firstErr != nil {
			return Observation{}, firstErr
		}
	}
	if len(out.Results) == 0 {
		if firstErr != nil {
			return Observation{}, firstErr
		}
		return Observation{}, errors.New("没有成功核对本轮商品报价")
	}
	out.Parser = "普通浏览器自动采集（免扩展）"
	if p.DellFamilyScan && b.scanCore && !p.quoteOnly && scanAllowed && customPage != nil {
		if currentView != "custom" {
			req := r
			req.Action = "custom"
			next, err := b.request(ctx, req)
			if err == nil {
				next, err = b.stable(ctx, next, req)
			}
			if err != nil {
				out.Partial = true
				out.Note += "；定制组合未扫描：" + nativeViewFailureNote(err)
				out.VerifiedNative = true
				return out, nil
			}
			if nativeView(next) != "custom" {
				out.Partial = true
				out.Note += "；定制组合未扫描：配置入口未完成视图切换；下轮自动重验"
				out.VerifiedNative = true
				return out, nil
			}
			customPage = &next
		}
		scanCtx := context.WithValue(ctx, scanRowsKey{}, func(rows []DellResult) {
			if publish, ok := ctx.Value(scanObservationKey{}).(func(Observation)); ok {
				current := Observation{VerifiedNative: true, Partial: true}
				for _, row := range out.Results {
					if !row.Custom {
						current.Results = append(current.Results, row)
					}
				}
				current.Results = append(current.Results, rows...)
				publish(current)
			}
		})
		var rows []DellResult
		var partial bool
		var corePending bool
		var err error
		if p.DellScanMode == "full" {
			rows, partial, err = b.scanCustomCore(scanCtx, *customPage, r, progress)
			out.ScanScope = "full"
		} else {
			rows, partial, out.ScanCursor, corePending, err = b.scanRepresentativeCore(scanCtx, *customPage, r, p.DellScanCursor, progress)
			out.CorePending = corePending
			out.ScanScope = "representative"
			out.Parser = "普通浏览器：代表性实际配置（未穷举，分轮检查）"
		}
		if len(rows) > 0 {
			var combined []DellResult
			for _, row := range out.Results {
				if !row.Custom {
					combined = append(combined, row)
				}
			}
			out.Results = append(combined, rows...)
		}
		if partial || err != nil {
			out.Partial = true
			note := "定制核心组合未全部核对"
			if p.DellScanMode != "full" {
				note = "代表配置部分选项未确认（本轮未穷举）"
			}
			if err != nil {
				note += "：" + nativeViewFailureNote(err)
			}
			if out.Note != "" {
				out.Note += "；"
			}
			out.Note += note
		}
		if p.DellScanMode == "full" {
			out.Parser += " / 核心定制组合"
		}
	}
	// Historical offers are retained separately by applyLocked. Their absence
	// alone does not make a successful current-view scan incomplete.
	out.VerifiedNative = true
	if p.quoteOnly {
		out.ScanScope = "quote"
		out.Parser = "普通报价及当前定制已更新；代表配置排队检查"
	}
	return out, nil
}
func nativeView(p nativePage) string {
	if p.CanOrdinary {
		return "custom"
	}
	if p.CanCustom {
		return "ordinary"
	}
	// A missing switch button must not turn an actual CTO hero into an
	// ordinary page. Require its own visible current quote, not hidden IDs.
	ids := nativeOfferIDs(p.Regions["hero-section"])
	if len(ids) == 1 && (strings.Contains(ids[0], "_reg_") || strings.Contains(ids[0], "_cto_")) {
		price, _ := nativeCustomPrice(p)
		if price > 0 {
			return "custom"
		}
	}
	return "ordinary"
}
func walkNative(n *nativeNode, fn func(*nativeNode)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.Children {
		walkNative(c, fn)
	}
}
func nativeText(n *nativeNode) string {
	var s []string
	walkNative(n, func(x *nativeNode) {
		if x.Name != "" && (len(x.Children) == 0 || x.Kind == "Text" || x.Kind == "Button" || x.Kind == "CheckBox" || x.Kind == "RadioButton") {
			s = append(s, x.Name)
		}
	})
	return strings.Join(s, " ")
}
func nativeButtons(n *nativeNode, match func(string) bool) bool {
	found := false
	walkNative(n, func(x *nativeNode) {
		if x.Enabled && (x.Kind == "Button" || x.Kind == "RadioButton" || x.Kind == "CheckBox") && match(strings.TrimSpace(x.Name)) {
			found = true
		}
	})
	return found
}
func nativeStock(n *nativeNode) string {
	s := strings.ToLower(nativeText(n))
	if strings.Contains(s, "out of stock") || strings.Contains(s, "currently unavailable") {
		return stockOut
	}
	if nativeButtons(n, func(s string) bool { return strings.EqualFold(s, "Add to Cart") }) {
		return stockIn
	}
	if strings.Contains(s, "get it as soon as") && nativeButtons(n, func(s string) bool { return strings.EqualFold(s, "Select Configuration") }) {
		return stockIn
	}
	return stockUnknown
}

var nativePriceRE = regexp.MustCompile(`(?i)Dell\s+Price\s*[: ]*\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
var nativeOriginalRE = regexp.MustCompile(`(?i)(?:Estimated\s+Value|Original\s+Price|List\s+Price)\s*[: ]*\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)

func nativePrice(n *nativeNode) (float64, float64) {
	prices, originals := nativeLabeledAmounts(n)
	price, original := 0.0, 0.0
	if len(prices) == 1 {
		for value := range prices {
			price = value
		}
	}
	if len(originals) == 1 {
		for value := range originals {
			original = value
		}
	}
	if original < price {
		original = 0
	}
	return price, original
}

func normalSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func nativeLabeledAmounts(n *nativeNode) (map[float64]bool, map[float64]bool) {
	prices, originals := map[float64]bool{}, map[float64]bool{}
	read := func(s string) {
		for _, hit := range nativePriceRE.FindAllStringSubmatch(s, -1) {
			if value := number(hit[1]); value > 0 {
				prices[value] = true
			}
		}
		for _, hit := range nativeOriginalRE.FindAllStringSubmatch(s, -1) {
			if value := number(hit[1]); value > 0 {
				originals[value] = true
			}
		}
	}
	read(nativeText(n))
	if n != nil {
		read(n.ReadingText)
	}
	// UIA can attach a complete price label to a non-leaf Group/Custom element.
	// C# supplies names only from control-visible nodes in this own region.
	walkNative(n, func(c *nativeNode) { read(c.Name) })
	return prices, originals
}

var nativeStandaloneMoneyRE = regexp.MustCompile(`^\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)$`)
var nativeAnyMoneyRE = regexp.MustCompile(`\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
var nativeAuxMoneyRE = regexp.MustCompile(`(?i)\b(?:save|savings?|cashback|rewards?|financing|monthly|shipping|starting at|upgrade|add-on)\b|/\s*(?:mo|month)\b|\bfor\s+[0-9]+[- ]?mo\b`)
var nativeFollowingMonthlyRE = regexp.MustCompile(`(?i)^(?:/\s*(?:mo|month)\b|for\s+[0-9]+[- ]?mo\b|per\s+month\b)`)

func nativeStandaloneAmounts(root *nativeNode) map[float64]bool {
	values := map[float64]bool{}
	var visit func(*nativeNode, []*nativeNode)
	visit = func(n *nativeNode, parents []*nativeNode) {
		if n == nil {
			return
		}
		candidate := normalSpace(n.Name)
		if candidate == "" && len(n.Children) > 0 && len(n.Children) <= 3 {
			var tokens []string
			for _, c := range n.Children {
				if c.Kind != "Text" || len(c.Children) != 0 {
					tokens = nil
					break
				}
				tokens = append(tokens, c.Name)
			}
			candidate = normalSpace(strings.Join(tokens, " "))
		}
		if hit := nativeStandaloneMoneyRE.FindStringSubmatch(candidate); len(hit) > 1 {
			value := number(hit[1])
			auxiliary := false
			for _, parent := range parents {
				var names []string
				walkNative(parent, func(c *nativeNode) {
					if c.Name != "" {
						names = append(names, c.Name)
					}
				})
				text := strings.Join(names, " ")
				if !nativeAuxMoneyRE.MatchString(text) {
					continue
				}
				amounts := map[float64]bool{}
				for _, amount := range nativeAnyMoneyRE.FindAllStringSubmatch(text, -1) {
					amounts[number(amount[1])] = true
				}
				// A narrow block with one amount and a finance/saving label is not
				// an entire-machine quote. A broad mixed block proves neither.
				if len(amounts) == 1 && amounts[value] {
					auxiliary = true
					break
				}
			}
			if value > 0 && !auxiliary {
				values[value] = true
			}
		}
		for _, child := range n.Children {
			visit(child, append(parents, n))
		}
	}
	visit(root, nil)
	if root != nil {
		// TextPattern is a region-scoped reading view. Only standalone lines,
		// or a currency sign followed by a number line, can supply unlabeled totals.
		for value := range nativeReadingAmounts(root.ReadingText) {
			values[value] = true
		}
	}
	return values
}

func nativeReadingAmounts(text string) map[float64]bool {
	values := map[float64]bool{}
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	for i := 0; i < len(lines); i++ {
		start := i
		line := normalSpace(lines[i])
		if line == "$" && i+1 < len(lines) {
			i++
			line += normalSpace(lines[i])
		}
		hit := nativeStandaloneMoneyRE.FindStringSubmatch(line)
		if len(hit) < 2 {
			continue
		}
		previous, next := "", ""
		if start > 0 {
			previous = normalSpace(lines[start-1])
		}
		if i+1 < len(lines) {
			next = normalSpace(lines[i+1])
		}
		if nativeAuxMoneyRE.MatchString(previous) || nativeFollowingMonthlyRE.MatchString(next) {
			continue
		}
		if value := number(hit[1]); value > 0 {
			values[value] = true
		}
	}
	return values
}

func nativeCustomPrice(page nativePage) (float64, float64) {
	if page.DOMConfiguration && page.DOMQuote != nil {
		return page.DOMQuote.Price, 0
	}
	hero := page.Regions["hero-section"]
	price, original := nativePrice(hero)
	if price > 0 {
		return price, original
	}
	labeled, _ := nativeLabeledAmounts(hero)
	if len(labeled) > 1 {
		return 0, 0
	} // Do not hide contradictory current labels.
	ids := nativeOfferIDs(hero)
	if len(ids) != 1 || (!strings.Contains(ids[0], "_reg_") && !strings.Contains(ids[0], "_cto_")) {
		return 0, 0
	}
	cart := page.Regions["add-to-cart-stack"]
	cartIDs := nativeOfferIDs(cart)
	if len(cartIDs) > 1 || len(cartIDs) == 1 && cartIDs[0] != ids[0] {
		return 0, 0
	}
	a, c := nativeStandaloneAmounts(hero), nativeStandaloneAmounts(cart)
	common := map[float64]bool{}
	for value := range a {
		if c[value] {
			common[value] = true
		}
	}
	// Some browsers omit the visually hidden "Dell Price" label. Accept an
	// unlabeled total only when two current product regions uniquely agree.
	if len(common) != 1 {
		return 0, 0
	}
	for value := range common {
		price = value
	}
	if original < price {
		original = 0
	}
	return price, original
}
func nativeOfferIDs(n *nativeNode) []string {
	m := map[string]bool{}
	// Chrome may expose the label and its value as separate Text elements.
	labelRE := regexp.MustCompile(`(?i)Offer\s+ID\s*[: ]*(` + offerRE.String() + `)`)
	for _, hit := range labelRE.FindAllStringSubmatch(nativeText(n), -1) {
		m[strings.ToLower(hit[1])] = true
	}
	if n != nil {
		for _, hit := range labelRE.FindAllStringSubmatch(normalSpace(n.ReadingText), -1) {
			m[strings.ToLower(hit[1])] = true
		}
	}
	walkNative(n, func(x *nativeNode) {
		for _, id := range offerRE.FindAllString(x.ID, -1) {
			m[strings.ToLower(id)] = true
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(x.Name)), "offer id") {
			for _, id := range offerRE.FindAllString(x.Name, -1) {
				m[strings.ToLower(id)] = true
			}
		}
	})
	v := []string{}
	for id := range m {
		v = append(v, id)
	}
	sort.Strings(v)
	return v
}
func nativeCardNodes(n *nativeNode) []*nativeNode {
	if n == nil {
		return nil
	}
	ids := nativeOfferIDs(n)
	price, _ := nativePrice(n)
	// Prefer a complete single-offer card, keeping its stock and specifications.
	if len(ids) == 1 && price > 0 && (strings.Contains(n.Class, "card-deck-item") || n.Kind == "Group" && nativeStock(n) != stockUnknown) {
		return []*nativeNode{n}
	}
	var child []*nativeNode
	for _, c := range n.Children {
		child = append(child, nativeCardNodes(c)...)
	}
	if len(child) > 0 {
		return child
	}
	if len(ids) == 1 && price > 0 {
		return []*nativeNode{n}
	}
	return nil
}
func nativeSpecs(row *DellResult, n *nativeNode) {
	var texts []string
	walkNative(n, func(x *nativeNode) {
		if x.Name != "" && (len(x.Children) == 0 || x.Kind == "Text") {
			texts = append(texts, strings.TrimSpace(x.Name))
		}
	})
	for i, s := range texts {
		for label, dst := range map[string]*string{"Processor": &row.CPU, "Graphics Card": &row.GPU, "Memory": &row.Memory, "Storage": &row.Storage, "Displays": &row.Display} {
			if s == label && i+1 < len(texts) {
				*dst = texts[i+1]
			}
			if strings.HasPrefix(s, label+" ") {
				*dst = strings.TrimSpace(strings.TrimPrefix(s, label))
			}
		}
	}
}
func nativeOptions(root *nativeNode) map[string]string {
	options := map[string]string{}
	headers := map[string]*nativeNode{}
	nodesByID := map[string]*nativeNode{}
	walkNative(root, func(n *nativeNode) {
		if n.ID != "" {
			nodesByID[n.ID] = n
		}
		if strings.HasPrefix(n.ID, "label-module") {
			headers[strings.TrimPrefix(n.ID, "label-")] = n
		}
	})
	for id, header := range headers {
		var value string
		scope := nodesByID[id]
		choices := 0
		selected := map[string]string{}
		walkNative(scope, func(n *nativeNode) {
			if nativeChoiceNode(n) && n.Name != "" {
				choices++
				if n.Selected {
					selected[nativeOptionKey(n.Name)] = n.Name
				}
			}
		})
		if len(selected) > 1 {
			continue
		} // Never choose the last of conflicting states.
		for _, name := range selected {
			value = name
		}
		if value == "" {
			value = nativeCollapsedSummary(root, header)
		}
		if value == "" && choices == 0 {
			bestLen := int(^uint(0) >> 1)
			walkNative(root, func(n *nativeNode) {
				if n == header {
					return
				}
				count := 0
				contains := false
				walkNative(n, func(c *nativeNode) {
					if strings.HasPrefix(c.ID, "label-module") {
						count++
						if c.ID == header.ID {
							contains = true
						}
					}
				})
				if !contains || count != 1 || n.Kind == "Button" {
					return
				}
				s := strings.TrimSpace(nativeText(n))
				s = strings.TrimSpace(strings.TrimPrefix(s, normalSpace(header.Name)))
				if s != "" && len(s) < bestLen {
					bestLen = len(s)
					value = s
				}
			})
		}
		if value != "" {
			options[id] = nativeOptionKey(value)
		}
	}
	return options
}
func nativeObservation(page nativePage, requested string, family bool) (Observation, error) {
	if e := normalPageError(page); e != nil {
		return Observation{}, e
	}
	if !normalURLMatches(requested, page.URL, family) {
		return Observation{}, errors.New("普通浏览器当前页面机型不一致")
	}
	if nativeView(page) == "custom" {
		hero := page.Regions["hero-section"]
		price, original := nativeCustomPrice(page)
		if price <= 0 {
			reason := "报价区与购买栏的整机总价未唯一对应"
			if hero == nil {
				reason = "商品主报价区未加载"
			} else if page.Regions["add-to-cart-stack"] == nil {
				reason = "购买栏未加载"
			} else if len(nativeStandaloneAmounts(hero)) == 0 {
				reason = "商品主报价区没有可核对总价"
			}
			return Observation{}, errors.New("普通浏览器定制整机现价未识别：" + reason)
		}
		ids := nativeOfferIDs(hero)
		if len(ids) != 1 || !nativeOfferMatchesModel(requested, ids[0]) || (!strings.Contains(ids[0], "_reg_") && !strings.Contains(ids[0], "_cto_")) {
			return Observation{}, errors.New("普通浏览器定制 Offer ID 未核对；未借用其他款报价")
		}
		options := nativeOptions(page.Regions["configuration-section"])
		keys := make([]string, 0, len(options))
		for k := range options {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sig := ""
		for _, k := range keys {
			sig += k + "=" + options[k] + ";"
		}
		row := DellResult{OfferID: ids[0] + "-BYO-" + shortHash(sig), URL: page.URL, Source: "普通浏览器：当前定制", Custom: true, Price: price, Original: original, Discount: discount(price, original), DiscountConfirmed: original >= price, Stock: nativeStock(page.Regions["add-to-cart-stack"]), Options: options, Note: "只监控实际配置的整机折扣；不按总价提醒"}
		if !row.DiscountConfirmed {
			row.Note += "；折扣待公布"
		}
		if page.DOMQuote != nil {
			row.Stock = stockUnknown
			row.Note += "；当前可见报价多次核对，界面读取滞后；原价待同步"
			if page.DOMStock == "" {
				row.Note += "；库存待同步"
			}
		}
		if page.DOMStock != "" {
			row.Stock = page.DOMStock
			row.Note += "；库存由同一当前配置的购买按钮核对（未执行购买）"
		}
		row.Confirmed = row.Stock != stockUnknown
		// If collapsed UIA options could not be fully bound, thresholds still use
		// this own quote, but separate readings cannot imply a same-config increase.
		headers := map[string]bool{}
		walkNative(page.Regions["configuration-section"], func(n *nativeNode) {
			if strings.HasPrefix(n.ID, "label-module") {
				headers[n.ID] = true
			}
		})
		if len(options) == 0 || len(options) < len(headers) {
			row.OfferID += "-UNBOUND"
			row.ConfigUnbound = true
			row.Note += "；未完整绑定所选项，不判断同配置折扣提高"
		}
		bindNativeCoreSpecs(&row, page)
		return Observation{Price: price, Original: original, Discount: row.Discount, Stock: row.Stock, Results: []DellResult{row}}, nil
	}
	var rows []DellResult
	seen := map[string]bool{}
	cards := nativeCardNodes(page.Regions["offers-container"])
	if len(cards) == 0 {
		cards = nativeCardNodes(page.Regions["configuration-section"])
	}
	for _, card := range cards {
		ids := nativeOfferIDs(card)
		if len(ids) != 1 || seen[ids[0]] {
			continue
		}
		id := ids[0]
		if !nativeOfferMatchesModel(requested, id) {
			continue
		}
		if strings.Contains(id, "_reg_") || strings.Contains(id, "_cto_") {
			continue
		}
		if !family && id != offerFromURL(requested) {
			continue
		}
		price, original := nativePrice(card)
		row := DellResult{OfferID: id, Source: "普通浏览器：预配置", URL: nativeOfferURL(page.URL, id), Price: price, Original: original, Discount: discount(price, original), Stock: nativeStock(card)}
		row.Confirmed = price > 0 && row.Stock != stockUnknown
		nativeSpecs(&row, card)
		rows = append(rows, row)
		seen[id] = true
	}
	if len(rows) == 0 {
		hero := page.Regions["hero-section"]
		ids := nativeOfferIDs(hero)
		price, original := nativePrice(hero)
		if price > 0 && len(ids) == 1 && nativeOfferMatchesModel(requested, ids[0]) && !strings.Contains(ids[0], "_reg_") && !strings.Contains(ids[0], "_cto_") && (family || ids[0] == offerFromURL(requested)) {
			stock := nativeStock(page.Regions["add-to-cart-stack"])
			row := DellResult{OfferID: ids[0], Source: "普通浏览器：当前预配置", URL: page.URL, Price: price, Original: original, Discount: discount(price, original), Stock: stock, Confirmed: stock != stockUnknown}
			nativeSpecs(&row, page.Regions["configuration-section"])
			rows = []DellResult{row}
		}
	}
	if len(rows) == 0 {
		return Observation{}, errors.New("普通浏览器未识别该机型自身预配置报价；未读取月供或其他商品价格")
	}
	out := Observation{Results: rows, Stock: stockUnknown}
	for _, row := range rows {
		if out.Price == 0 || row.Price < out.Price {
			out.Price = row.Price
			out.Original = row.Original
			out.Discount = row.Discount
		}
		if row.Stock == stockIn {
			out.Stock = stockIn
		}
	}
	return out, nil
}
func normalReaderError(err error) error {
	return fmt.Errorf("普通浏览器自动采集失败：%w", err)
}

func nativeOfferMatchesModel(requested, id string) bool {
	if own := offerFromURL(requested); own != "" {
		return strings.Split(own, "_")[0] == strings.Split(id, "_")[0]
	}
	return strings.Contains(dellPlatform(requested), strings.Split(id, "_")[0])
}

func nativeOfferURL(raw, id string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
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

// Bind displayed selections to their own core group, including the GPU.
func bindNativeCoreSpecs(row *DellResult, page nativePage) {
	for _, g := range nativeCoreGroups(page) {
		v := nativeOptionKey(row.Options[g.ID])
		if v == "" {
			continue
		}
		switch g.Label {
		case "processor":
			row.CPU = v
		case "graphics card":
			row.GPU = v
		case "memory":
			row.Memory = v
		case "storage":
			row.Storage = v
		case "display", "displays":
			row.Display = v
		}
	}
}

type nativeRecoveredFailure struct{ Reason string }

func (e *nativeRecoveredFailure) Error() string { return e.Reason }
