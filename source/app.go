package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/net/html"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

func sortStrings(v []string)    { sort.Strings(v) }
func shortHash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:6]) }
func newID() string             { v := make([]byte, 12); _, _ = rand.Read(v); return hex.EncodeToString(v) }

type App struct {
	mu         sync.RWMutex
	persistMu  sync.Mutex
	store      Store
	extra      map[string]json.RawMessage
	file       string
	ctx        context.Context
	cancel     context.CancelFunc
	sem        chan struct{}
	cancels    map[string]context.CancelFunc
	alerts     []Alert
	alertQueue chan Alert
	normal     *NormalBrowser
	browser    *BrowserManager
	client     *http.Client
	fetch      func(context.Context, Product, func(string)) (Observation, error)
	noDesktop  bool
	writeError string
}

func newApp(dir string, noDesktop bool) (*App, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	jar, _ := cookiejar.New(nil)
	a := &App{file: filepath.Join(dir, "monitor_data.json"), ctx: ctx, cancel: cancel, sem: make(chan struct{}, 2), cancels: map[string]context.CancelFunc{}, alerts: []Alert{}, alertQueue: make(chan Alert, 200), browser: newBrowserManager(dir), normal: newNormalBrowser(), noDesktop: noDesktop, client: &http.Client{Timeout: 25 * time.Second, Jar: jar}}
	a.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("网页重定向超过 5 次")
		}
		if len(via) > 0 && siteName(req.URL.String()) != siteName(via[0].URL.String()) {
			return errors.New("商品网址跳转到了其他网站")
		}
		return nil
	}
	a.store.Products = []*Product{}
	if data, e := os.ReadFile(a.file); e == nil {
		if e = json.Unmarshal(data, &a.store); e != nil {
			cancel()
			return nil, fmt.Errorf("已有数据文件损坏；没有覆盖：%w", e)
		}
		var m map[string]json.RawMessage
		_ = json.Unmarshal(data, &m)
		delete(m, "products")
		delete(m, "settings")
		delete(m, "schema")
		a.extra = m
		backup := filepath.Join(dir, "monitor_data_before_v810_"+time.Now().Format("20060102_150405")+"_"+newID()[:8]+".json")
		if e = os.WriteFile(backup, data, 0600); e != nil {
			cancel()
			return nil, fmt.Errorf("无法备份旧数据；未修改原文件：%w", e)
		}
	} else if !os.IsNotExist(e) {
		cancel()
		return nil, e
	}
	for _, p := range a.store.Products {
		migrateDellCustomRows(p)
		// V7.8's exclusive switch was a mistaken interpretation. Both types
		// are always retained; each row now chooses its own alert rule.
		wasExtension := p.BrowserFeed || strings.Contains(p.NeedsAction, "助手")
		p.BrowserFeed = false
		p.CustomDiscountOnly = false
		p.Checking = false
		p.CheckState = ""
		markStale(p)
		p.LastTrusted = false
		if strings.Contains(p.LastError, "未完整完成") {
			p.ScanIncomplete = true
		}
		if action := actionForFailure(p.URL, p.LastError); action != "" {
			p.NeedsAction = action
		}
		if siteName(p.URL) == "lenovo" && (strings.Contains(p.LastError, "网页脚本执行失败") || strings.Contains(p.LastError, "浏览器页面暂未就绪")) && !hardBrowserContentFailure(p.LastError) {
			p.NeedsAction = ""
			p.LastError = "等待软件自动重新读取；上次网页脚本执行失败"
		}
		if wasExtension || (a.normal.available && dellPlatform(p.URL) != "") {
			p.NeedsAction = ""
			p.ScanIncomplete = false
			p.LastError = "等待软件自动重新读取；无需扩展或配对"
		}
		p.revision = 1
		if p.IntervalMin < 1 {
			p.IntervalMin = 10
		}
		p.nextCheck = time.Now()
	}
	a.store.Schema = 90
	a.fetch = a.fetchObservation
	return a, nil
}
func (a *App) persist() error {
	a.persistMu.Lock()
	defer a.persistMu.Unlock()
	a.mu.RLock()
	b, e := json.Marshal(a.store)
	extras := a.extra
	a.mu.RUnlock()
	if e != nil {
		return e
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	for k, v := range extras {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	b, e = json.MarshalIndent(m, "", "  ")
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(a.file), "monitor_write_*.tmp")
	if e != nil {
		return a.recordWriteError(e)
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, e = tmp.Write(b); e != nil {
		tmp.Close()
		return a.recordWriteError(e)
	}
	if e = tmp.Sync(); e != nil {
		tmp.Close()
		return a.recordWriteError(e)
	}
	if e = tmp.Close(); e != nil {
		return a.recordWriteError(e)
	}
	if e = os.Rename(path, a.file); e != nil {
		return a.recordWriteError(e)
	}
	a.mu.Lock()
	a.writeError = ""
	a.mu.Unlock()
	return nil
}
func (a *App) recordWriteError(e error) error {
	a.mu.Lock()
	a.writeError = e.Error()
	a.mu.Unlock()
	log.Printf("save failed: %v", e)
	return e
}
func (a *App) findLocked(id string) *Product {
	for _, p := range a.store.Products {
		if p.ID == id {
			return p
		}
	}
	return nil
}
func (a *App) progress(id string, revision uint64, s string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p := a.findLocked(id); p != nil && p.revision == revision && p.Checking {
		p.CheckState = s
	}
}
func markStale(p *Product) {
	p.Stale = true
	for i := range p.DellResults {
		p.DellResults[i].Stale = true
		p.DellResults[i].Matched = false
	}
	for i := range p.LenovoResults {
		p.LenovoResults[i].Stale = true
		p.LenovoResults[i].Matched = false
	}
}

// A usable current offer does not make a blocked full scan safe to retry.
// Use the same recovery state for complete failures, partial scans and saved data.
func actionForFailure(raw, detail string) string {
	if dellPlatform(raw) != "" && !hardBrowserContentFailure(detail) {
		for _, reason := range []string{"配置入口未完成视图切换", "普通浏览器没有提供可核对的商品区域", "普通浏览器商品页面尚未加载"} {
			if strings.Contains(detail, reason) {
				return "" // Transient content/view failures retain old results and retry.
			}
		}
	}
	if strings.HasPrefix(detail, "普通浏览器自动采集失败：后台读取超时") && strings.Contains(detail, "软件后台进程已确认回收") {
		return ""
	}
	if strings.Contains(detail, "浏览器页面暂未就绪：") && !hardBrowserContentFailure(detail) {
		return ""
	}
	if strings.HasPrefix(detail, "普通浏览器自动采集失败：商品报价未识别：") || strings.HasPrefix(detail, "普通浏览器自动采集失败：报价、库存或所选配置连续变化；") {
		return "" // Content not ready: keep stale data and retry at the configured interval.
	}
	if strings.Contains(detail, "普通浏览器自动采集失败") {
		return "普通浏览器采集未完成；自动检查已暂停"
	}
	if strings.Contains(detail, "未找到 Edge/Chrome") || strings.Contains(detail, "采集浏览器启动失败") || strings.Contains(detail, "采集浏览器启动后退出") || strings.Contains(detail, "采集浏览器启动超时") || strings.Contains(detail, "采集浏览器接口 HTTP") {
		return "采集浏览器不可用；自动检查已暂停"
	}
	if strings.Contains(detail, "网页访问被限制") {
		return "等待网页验证；自动检查已暂停"
	}
	if vipURL(raw) {
		if strings.Contains(detail, "跳转到非会员") {
			return "会员商店跳转异常；自动检查已暂停"
		}
		if strings.Contains(detail, "登录") || strings.Contains(detail, "验证") {
			return "等待会员页面验证；自动检查已暂停"
		}
		if strings.Contains(detail, "当前售价") || strings.Contains(detail, "VIP") || strings.Contains(detail, "会员") {
			return "会员报价未确认；自动检查已暂停"
		}
	}
	return ""
}

func hardBrowserContentFailure(detail string) bool {
	s := strings.ToLower(detail)
	for _, reason := range []string{"网页访问被限制", "access denied", "跳转到非会员", "登录", "网页验证", "verify you are human", "其他网站", "其他页面", "其他机型", "窗口或机型已变化", "窗口已变化", "标识已改变", "其他标签页", "回收未确认", "窗口状态未确认", "已跳转", "采集浏览器启动", "采集浏览器接口 http", "未找到 edge/chrome"} {
		if strings.Contains(s, reason) {
			return true
		}
	}
	return false
}

func failureStatusText(detail, needsAction string) string {
	if needsAction != "" {
		detail = strings.ReplaceAll(detail, "下轮自动重验", "自动检查已暂停，等待处理")
		detail = strings.ReplaceAll(detail, "下轮自动重试", "自动检查已暂停，等待处理")
	}
	return detail
}
func (a *App) schedule(id string) bool {
	return a.scheduleWith(id, a.fetch)
}
func (a *App) scheduleWith(id string, fetch func(context.Context, Product, func(string)) (Observation, error)) bool {
	a.mu.Lock()
	p := a.findLocked(id)
	if p == nil || p.Checking {
		a.mu.Unlock()
		return false
	}
	snapshot := *p
	snapshot.History = nil
	snapshot.DellResults = append([]DellResult(nil), p.DellResults...)
	snapshot.LenovoResults = append([]DellResult(nil), p.LenovoResults...)
	revision := p.revision
	budget := 5 * time.Minute
	if a.normal.available && p.DellFamilyScan && dellPlatform(p.URL) != "" {
		budget = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(a.ctx, budget)
	ctx = context.WithValue(ctx, scanObservationKey{}, func(o Observation) {
		a.applyScanProgress(ctx, id, revision, o)
	})
	a.cancels[id] = cancel
	p.Checking = true
	p.CheckStarted = stamp()
	p.CheckState = "排队等待"
	a.mu.Unlock()
	go func() {
		defer cancel()
		var o Observation
		var e error
		select {
		case a.sem <- struct{}{}:
			defer func() { <-a.sem }()
			a.progress(id, revision, "正在读取商品网页")
			func() {
				defer func() {
					if r := recover(); r != nil {
						e = fmt.Errorf("检查异常：%v", r)
					}
				}()
				o, e = fetch(ctx, snapshot, func(s string) { a.progress(id, revision, s) })
			}()
		case <-ctx.Done():
			e = ctx.Err()
		}
		if e == nil && ctx.Err() != nil {
			e = ctx.Err()
		}
		if e == nil && o.Price <= 0 && o.Stock != stockOut {
			e = errors.New("未取得可信现价或明确缺货状态")
		}
		a.mu.Lock()
		p := a.findLocked(id)
		if p == nil || p.revision != revision {
			a.mu.Unlock()
			return
		}
		delete(a.cancels, id)
		p.Checking = false
		p.CheckState = ""
		p.LastChecked = stamp()
		p.CheckSeq++
		p.nextCheck = time.Now().Add(time.Duration(p.IntervalMin * float64(time.Minute)))
		if e != nil {
			if errors.Is(e, context.DeadlineExceeded) {
				p.LastError = "检查超时，已结束本轮；保留上次数据"
			} else if errors.Is(e, context.Canceled) {
				p.LastError = "本轮检查已取消；保留上次数据"
			} else {
				p.LastError = e.Error()
			}
			markStale(p)
			p.LastTrusted = false
			if errors.Is(e, context.DeadlineExceeded) && a.normal.available && dellPlatform(p.URL) != "" {
				p.LastError = "普通浏览器自动采集失败：检查超时；保留上次数据"
			}
			p.NeedsAction = actionForFailure(p.URL, p.LastError)
			p.LastError = failureStatusText(p.LastError, p.NeedsAction)
			log.Printf("check %s failed: %v", p.ID, e)
		} else {
			// Streaming is display progress, not the previous observation used
			// for change/discount alerts. Compare the final result to scan start.
			p.DellResults = append([]DellResult(nil), snapshot.DellResults...)
			p.LastTrusted = snapshot.LastTrusted
			a.applyLocked(p, o)
		}
		a.mu.Unlock()
		_ = a.persist()
	}()
	return true
}
func matches(p *Product, c DellResult) bool {
	if c.Custom {
		return customAvailableForProduct(p, c) && c.DiscountConfirmed && !c.Stale && c.Price > 0 && p.MinDiscount > 0 && c.Discount >= p.MinDiscount
	}
	return c.Confirmed && !c.Stale && c.Stock == stockIn && c.Price > 0 && (p.TargetPrice <= 0 || c.Price <= p.TargetPrice) && (p.MinDiscount <= 0 || c.Discount >= p.MinDiscount)
}
func (a *App) applyLocked(p *Product, o Observation) {
	previous := *p
	previous.DellResults = append([]DellResult(nil), p.DellResults...)
	previous.LenovoResults = append([]DellResult(nil), p.LenovoResults...)
	if len(o.Results) > 0 {
		for i := range o.Results {
			o.Results[i].Matched = matches(p, o.Results[i])
		}
		// A partial tree never makes absent old combinations disappear or generates removal alerts.
		if o.Partial || o.VerifiedNative {
			known := map[string]bool{}
			for _, c := range o.Results {
				known[c.OfferID] = true
			}
			for _, c := range modelResults(p) {
				if !known[c.OfferID] {
					c.Stale = true
					c.Matched = false
					c.Note = "本轮未覆盖；上次结果"
					o.Results = append(o.Results, c)
				}
			}
		}
		var selected *DellResult
		rank := func(c *DellResult) int {
			if c.Custom {
				return 4
			}
			if c.Matched {
				return 0
			}
			if c.Confirmed && c.Stock == stockIn {
				return 1
			}
			if c.Stock == stockOut {
				return 2
			}
			return 3
		}
		for i := range o.Results {
			c := &o.Results[i]
			if c.Stale {
				continue
			}
			better := selected == nil
			if selected != nil {
				better = rank(c) < rank(selected) || (rank(c) == rank(selected) && c.Price > 0 && (selected.Price <= 0 || c.Price < selected.Price))
			}
			if better {
				selected = c
			}
		}
		if selected != nil {
			o.Price = selected.Price
			o.Original = selected.Original
			o.Discount = selected.Discount
			o.Stock = selected.Stock
		}
	}
	p.LastPrice = o.Price
	p.LastOriginal = o.Original
	p.LastDiscount = o.Discount
	p.LastStock = o.Stock
	p.LastParser = o.Parser
	if siteName(p.URL) == "lenovo" {
		p.LenovoResults = append([]DellResult(nil), o.Results...)
	} else {
		p.DellResults = append([]DellResult(nil), o.Results...)
	}
	p.LastSuccess = stamp()
	p.LastError = ""
	p.NeedsAction = ""
	p.Stale = false
	p.LastTrusted = true
	p.ScanIncomplete = o.Partial
	if o.Partial {
		p.LastError = "本轮扫描未完整完成；未覆盖的配置已标记为上次结果。" + o.Note
		p.NeedsAction = actionForFailure(p.URL, o.Note)
		p.LastError = failureStatusText(p.LastError, p.NeedsAction)
	}
	sig := observationSignature(o)
	if sig != p.LastSignature {
		note := o.Note
		if note == "" {
			note = o.Parser
		}
		h := HistoryPoint{CheckedAt: p.LastSuccess, Price: o.Price, Original: o.Original, Discount: o.Discount, Stock: o.Stock, Changed: true, Note: note}
		if len(o.Results) > 0 {
			onlyCustom := true
			for _, row := range o.Results {
				if !row.Custom && !row.Stale {
					onlyCustom = false
					break
				}
			}
			if onlyCustom {
				h.Price = 0
				h.Custom = true
				for _, row := range o.Results {
					if row.Custom && !row.Stale && row.DiscountConfirmed {
						h.DiscountConfirmed = true
						break
					}
				}
			}
		}
		p.History = append([]HistoryPoint{h}, p.History...)

		p.LastSignature = sig
	}
	if !o.Partial || o.VerifiedNative {
		a.maybeAlertLocked(p, previous, o)
	}
}
func (a *App) maybeAlertLocked(p *Product, previous Product, o Observation) {
	if !p.Active || a.store.Settings.GlobalPaused {
		return
	}
	var reasons []string
	threshold := p.TargetPrice > 0 || p.MinDiscount > 0
	if len(o.Results) > 0 {
		old := map[string]DellResult{}
		for _, c := range modelResults(&previous) {
			old[c.OfferID] = c
		}
		for _, c := range o.Results {
			if c.Custom {
				if !customAvailableForProduct(p, c) || !c.DiscountConfirmed || c.Stale || c.Price <= 0 {
					continue
				}
				if matches(p, c) {
					reasons = append(reasons, fmt.Sprintf("%s 定制折扣达到条件：优惠 %.2f%%", c.OfferID, c.Discount))
				}
				q, ok := old[c.OfferID]
				if p.AlertOnDiscountIncrease && previous.LastTrusted && ok && q.Custom && customAvailableForProduct(p, q) && q.DiscountConfirmed && !q.Stale && !q.ConfigUnbound && !c.ConfigUnbound && c.Discount > q.Discount+0.005 {
					reasons = append(reasons, fmt.Sprintf("%s 定制折扣提高：%.2f%% → %.2f%%", c.OfferID, q.Discount, c.Discount))
				}
				continue
			}
			if !c.Confirmed || c.Stale || c.Stock != stockIn {
				continue
			}
			q, ok := old[c.OfferID]
			if threshold && matches(p, c) {
				reasons = append(reasons, fmt.Sprintf("%s 达到条件：$%.2f / %.2f%%", c.OfferID, c.Price, c.Discount))
			}
			if previous.LastTrusted && ok && q.Confirmed && !q.Stale {
				if p.AlertOnRestock && q.Stock == stockOut {
					reasons = append(reasons, c.OfferID+" 补货")
				}
				if p.AlertOnPriceDrop && q.Price > c.Price+0.005 {
					reasons = append(reasons, fmt.Sprintf("%s 降价 $%.2f → $%.2f", c.OfferID, q.Price, c.Price))
				}
			}
		}
	} else if o.Stock == stockIn && o.Price > 0 {
		if threshold && (p.TargetPrice <= 0 || o.Price <= p.TargetPrice) && (p.MinDiscount <= 0 || o.Discount >= p.MinDiscount) {
			reasons = append(reasons, fmt.Sprintf("达到提醒条件：$%.2f / %.2f%%", o.Price, o.Discount))
		}
		if previous.LastTrusted {
			if p.AlertOnRestock && previous.LastStock == stockOut {
				reasons = append(reasons, "商品已补货")
			}
			if p.AlertOnPriceDrop && previous.LastPrice > o.Price+0.005 {
				reasons = append(reasons, fmt.Sprintf("降价 $%.2f → $%.2f", previous.LastPrice, o.Price))
			}
		}
	}
	if len(reasons) == 0 {
		return
	}
	last, _ := time.ParseInLocation("2006-01-02 15:04:05", p.LastAlertAt, time.Local)
	if !last.IsZero() && time.Since(last) < time.Duration(p.CooldownMin*float64(time.Minute)) {
		return
	}
	alert := Alert{ID: newID(), ProductID: p.ID, Name: p.Name, Message: strings.Join(reasons, "\n"), Created: stamp(), URL: p.URL}
	p.LastAlertAt = alert.Created
	a.alerts = append(a.alerts, alert)
	if len(a.alerts) > 200 {
		a.alerts = a.alerts[len(a.alerts)-200:]
	}
	select {
	case a.alertQueue <- alert:
	default:
		log.Printf("desktop alert queue full")
	}
}
func (a *App) start() {
	go func() {
		for {
			select {
			case <-a.ctx.Done():
				return
			case x := <-a.alertQueue:
				if !a.noDesktop {
					desktopAlert(x.Name, x.Message)
				}
			}
		}
	}()
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-tick.C:
				a.mu.RLock()
				var ids []string
				if !a.store.Settings.GlobalPaused {
					for _, p := range a.store.Products {
						if p.Active && p.NeedsAction == "" && !p.Checking && !time.Now().Before(p.nextCheck) {
							ids = append(ids, p.ID)
						}
					}
				}
				a.mu.RUnlock()
				for _, id := range ids {
					a.schedule(id)
				}
			}
		}
	}()
}
func (a *App) shutdown() {
	a.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := a.normal.close(ctx); err != nil {
		log.Printf("normal collector cleanup: %v", err)
	}
	a.browser.close()
	closeNativeDesktop()
	_ = a.persist()
}

func (a *App) getPage(ctx context.Context, raw string) (string, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return "", e
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	r, e := a.client.Do(req)
	if e != nil {
		return "", e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d；本轮未取得商品数据", r.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024+1))
	if e != nil {
		return "", e
	}
	if len(b) > 10*1024*1024 {
		return "", errors.New("网页数据超过 10MB；未使用截断内容")
	}
	if siteName(r.Request.URL.String()) != siteName(raw) {
		return "", errors.New("商品网页跳转到了其他网站")
	}
	if vipURL(raw) && !vipURL(r.Request.URL.String()) {
		return "", errors.New("会员商品网址跳转到非会员商店；未使用普通商店价格")
	}
	return string(b), nil
}
func byoURL(body, raw string) string {
	if len(configGroups(body)) > 0 {
		return raw
	}
	r, _ := rootHTML(body)
	base, _ := url.Parse(raw)
	for _, n := range nodes(r, func(n *html.Node) bool { return n.Type == html.ElementNode }) {
		if hasClass(n, "customize") && strings.Contains(attr(n, "data-offer-id"), "_reg_") && !hiddenAncestor(n) {
			return cardURL(raw, attr(n, "data-offer-id"), n)
		}
		for _, k := range []string{"href", "data-url"} {
			v := attr(n, k)
			if strings.Contains(v, "_reg_") && !strings.Contains(v, "shopapi/") {
				u, e := base.Parse(v)
				if e == nil && siteName(u.String()) == "dell" {
					return u.String()
				}
			}
		}
	}
	return ""
}
func (a *App) fetchObservation(ctx context.Context, p Product, progress func(string)) (Observation, error) {
	if a.normal.available && dellPlatform(p.URL) != "" {
		o, e := a.normal.collect(ctx, p, progress)
		if e != nil {
			return Observation{}, normalReaderError(e)
		}
		return o, nil
	}
	// Once the user opens our collector, use that owned session directly.
	// Avoid an extra HTTP request and another isolated tab/session.
	if a.browser.hasVisible(p.URL) {
		return a.browser.read(ctx, p.URL, func(c *cdpClient, body string) (Observation, error) {
			return a.parseAndComplete(ctx, p, body, c, progress)
		})
	}
	body, httpErr := a.getPage(ctx, p.URL)
	var o Observation
	var parseErr error
	if httpErr == nil {
		o, parseErr = parsePage(body, p.URL, p.DellFamilyScan)
	}
	if httpErr == nil && parseErr == nil && siteName(p.URL) == "dell" && (o.Stock != stockUnknown || len(configGroups(body)) > 0 || browserExecutable() == "") {
		return a.completeDellMixed(ctx, p, body, o, nil, progress)
	}
	if httpErr == nil && parseErr == nil && siteName(p.URL) == "lenovo" && lenovoFamilyRE.MatchString(lenovoURLPart(p.URL)) {
		return a.completeLenovoCustom(ctx, p.URL, o, nil, progress)
	}
	if httpErr == nil && parseErr == nil && (o.Stock != stockUnknown || browserExecutable() == "") {
		return o, nil
	}
	progress("正在使用 Edge/Chrome 读取动态页面")
	bo, be := a.browser.read(ctx, p.URL, func(c *cdpClient, b string) (Observation, error) {
		return a.parseAndComplete(ctx, p, b, c, progress)
	})
	if be == nil {
		return bo, nil
	}
	if httpErr == nil && parseErr == nil {
		o.Note = "价格已读取；库存未确认。" + be.Error()
		return o, nil
	}
	var detail []string
	if httpErr != nil {
		detail = append(detail, "网页请求："+httpErr.Error())
	}
	if parseErr != nil {
		detail = append(detail, "页面解析："+parseErr.Error())
	}
	if be != nil {
		detail = append(detail, "采集浏览器："+be.Error())
	}
	return Observation{}, errors.New(strings.Join(detail, "；"))
}

func (a *App) parseAndComplete(ctx context.Context, p Product, body string, c *cdpClient, progress func(string)) (Observation, error) {
	o, err := parsePage(body, p.URL, p.DellFamilyScan)
	if err != nil {
		return Observation{}, err
	}
	if siteName(p.URL) == "dell" {
		return a.completeDellMixed(ctx, p, body, o, c, progress)
	}
	if siteName(p.URL) == "lenovo" && lenovoFamilyRE.MatchString(lenovoURLPart(p.URL)) {
		return a.completeLenovoCustom(ctx, p.URL, o, c, progress)
	}
	return o, nil
}

// Explicit recovery reads only the page already open in our visible collector.
// It does not retry HTTP, reload the page, or traverse other configurations.
func (a *App) readCurrentObservation(ctx context.Context, p Product, progress func(string)) (Observation, error) {
	if a.normal.available && dellPlatform(p.URL) != "" {
		return a.fetchObservation(ctx, p, progress)
	}
	progress("正在读取已打开页面；不刷新、不重试网页请求")
	return a.browser.readCurrent(ctx, p.URL, p.DellFamilyScan, func(_ *cdpClient, body string, current string) (Observation, error) {
		o, err := parsePage(body, current, p.DellFamilyScan)
		if err != nil {
			return Observation{}, err
		}
		if siteName(current) == "dell" && len(configGroups(body)) > 0 {
			o, err = a.completeDellDiscount(ctx, current, body, o, nil, progress)
		}
		if err != nil {
			return Observation{}, err
		}
		seen := map[string]bool{}
		for _, row := range o.Results {
			seen[row.OfferID] = true
		}
		for _, row := range modelResults(&p) {
			if !seen[row.OfferID] {
				o.Partial = true
				o.Note = "仅重验当前页面，其他型号保留为上次结果"
				break
			}
		}
		return o, nil
	})
}

// Both HTTP and browser fallback must use the same family completion path.
// A current BYO page price is retained as a partial result when enumeration cannot run.
func (a *App) completeDellFamily(ctx context.Context, raw, body string, o Observation, c *cdpClient, progress func(string)) (Observation, error) {
	byo := byoURL(body, raw)
	if byo == "" {
		return o, nil
	}
	progress("正在逐项核对 Dell BYO 配置")
	var rs []DellResult
	var partial bool
	var scanErr error
	if c != nil {
		byoBody := body
		if byo != raw {
			_, scanErr = c.call(ctx, "Page.navigate", map[string]any{"url": byo})
			if scanErr == nil {
				byoBody, scanErr = waitBrowserPage(ctx, c, byo)
			}
		}
		if scanErr == nil {
			rs, partial, scanErr = scanBYO(ctx, c, byoBody, byo, progress)
		}
	} else {
		_, scanErr = a.browser.read(ctx, byo, func(c *cdpClient, b string) (Observation, error) {
			rs, partial, scanErr = scanBYO(ctx, c, b, byo, progress)
			return Observation{}, scanErr
		})
	}
	if scanErr != nil {
		partial = true
	}
	if len(rs) > 0 {
		// Replace the current-page placeholder only after genuine configurations exist.
		if len(configGroups(body)) > 0 {
			o.Results = nil
		}
		o.Results = append(o.Results, rs...)
	}
	o.Partial = partial
	o.Parser = "Dell Offer + BYO 逐配置核价"
	if scanErr != nil {
		o.Note = "当前页面报价已读取；全量配置未完成：" + scanErr.Error()
	}
	if len(o.Results) == 0 {
		return Observation{}, errors.New("未取得任何可核对的 Dell 配置：" + o.Note)
	}
	return o, nil
}
