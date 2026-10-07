package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed index.html
var assets embed.FS

func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		b, _ := assets.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, map[string]any{"version": version, "pid": processID()})
	})
	mux.HandleFunc("/api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="PriceStockMonitor_diagnostics_V8.27.txt"`)
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, "商品监控诊断 %s\n导出时间：%s\n\n", version, stamp())
		a.mu.RLock()
		var checks []map[string]any
		for _, p := range a.store.Products {
			visibleCustom, hiddenCustom := 0, 0
			hiddenReasons := map[string]int{}
			var customRows []map[string]any
			var offerRows []map[string]any
			if siteName(p.URL) == "dell" {
				for _, row := range p.DellResults {
					if dellResultIsCustom(row) {
						row.Custom = true
						if !p.Stale && customAvailableForProduct(p, row) {
							visibleCustom++
						} else {
							hiddenCustom++
							reason := "未确认"
							switch {
							case p.Stale || row.Stale:
								reason = "上次结果"
							case row.Stock == stockOut:
								reason = "缺货"
							case row.Stock != stockIn:
								reason = "库存未确认"
							case row.Price <= 0:
								reason = "报价未确认"
							}
							hiddenReasons[reason]++
						}
						customRows = append(customRows, map[string]any{"offer_id": row.OfferID, "price": row.Price, "stock": row.Stock, "confirmed": row.Confirmed, "stale": row.Stale, "options": row.Options, "note": row.Note})
					} else {
						offerRows = append(offerRows, map[string]any{"offer_id": row.OfferID, "source": row.Source, "cpu": row.CPU, "gpu": row.GPU, "memory": row.Memory, "storage": row.Storage, "display": row.Display, "price": row.Price, "original": row.Original, "discount": row.Discount, "stock": row.Stock, "confirmed": row.Confirmed, "stale": row.Stale, "url": row.URL})
					}
				}
			}
			checks = append(checks, map[string]any{"name": p.Name, "last_checked": p.LastChecked, "stage": p.CheckState, "last_error": p.LastError, "needs_action": p.NeedsAction, "scan_incomplete": p.ScanIncomplete, "stale": p.Stale, "dell_family_scan": p.DellFamilyScan, "dell_platform": dellPlatform(p.URL), "last_parser": p.LastParser, "dell_offer_rows": offerRows, "dell_custom_visible": visibleCustom, "dell_custom_hidden": hiddenCustom, "dell_custom_hidden_reasons": hiddenReasons, "dell_custom_rows": customRows, "scan_scope": p.ScanScope, "core_pending": p.CorePending, "dell_scan_mode": p.DellScanMode, "next_scan_cursor": p.DellScanCursor})
		}
		meta, _ := json.MarshalIndent(checks, "", "  ")
		a.mu.RUnlock()
		fmt.Fprintf(w, "当前检查状态：\n%s\n\n本次版本日志（末尾最多2 MiB）：\n", meta)
		f, err := os.Open(filepath.Join(filepath.Dir(a.file), "app_v827.log"))
		if err != nil {
			fmt.Fprintln(w, "本次运行尚无可读取日志。请完成一次检查后再次下载诊断。")
			return
		}
		defer f.Close()
		const limit = 2 * 1024 * 1024
		if stat, err := f.Stat(); err == nil && stat.Size() > limit {
			_, _ = f.Seek(stat.Size()-limit, io.SeekStart)
		}
		_, _ = io.Copy(w, io.LimitReader(f, limit))
	})
	mux.HandleFunc("/api/products", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		v := []Product{}
		for _, p := range a.store.Products {
			q := productForDisplay(p)
			v = append(v, q)
		}
		jsonReply(w, v)
	})
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		jsonReply(w, map[string]any{"global_paused": a.store.Settings.GlobalPaused, "save_error": a.writeError})
	})
	mux.HandleFunc("/api/save", func(w http.ResponseWriter, r *http.Request) {
		var p Product
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		if e := jsonDecode(r.Body, &p); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.DellScanMode == "" {
			p.DellScanMode = "quick"
		}
		if p.DellScanMode != "quick" && p.DellScanMode != "full" {
			http.Error(w, "Dell检查方式无效", 400)
			return
		}
		p.URL = strings.TrimSpace(p.URL)
		p.BrowserFeed = false        // no extension setup is accepted or required
		p.CustomDiscountOnly = false // accept old clients, but never exclude ordinary offers
		if p.Name == "" || len(p.Name) > 200 {
			http.Error(w, "商品名称不能为空或超过 200 字符", 400)
			return
		}
		if e := validateURL(p.URL); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if p.IntervalMin < 1 || p.IntervalMin > 10080 || p.CooldownMin < 0 || p.MinDiscount < 0 || p.MinDiscount > 100 || p.TargetPrice < 0 || p.CooldownMin > 525600 {
			http.Error(w, "间隔至少 1 分钟；折扣 0–100%；阈值和冷却不能为负", 400)
			return
		}
		if p.DellFamilyScan && siteName(p.URL) != "dell" {
			http.Error(w, "机型扫描仅支持 Dell", 400)
			return
		}
		a.mu.Lock()
		if p.ID == "" {
			p.DellScanCursor = 0
			p.ID = newID()
			p.revision = 1
			p.nextCheck = time.Now()
			a.store.Products = append(a.store.Products, &p)
		} else if old := a.findLocked(p.ID); old != nil {
			// Cancel old work before editing; never let a deleted/changed URL receive late results.
			if cancel := a.cancels[p.ID]; cancel != nil {
				cancel()
				delete(a.cancels, p.ID)
			}
			changed := old.URL != p.URL || old.DellFamilyScan != p.DellFamilyScan || old.DellScanMode != p.DellScanMode
			modeChanged := old.CustomDiscountOnly != p.CustomDiscountOnly
			old.Name = p.Name
			old.URL = p.URL
			old.IntervalMin = p.IntervalMin
			old.TargetPrice = p.TargetPrice
			old.MinDiscount = p.MinDiscount
			old.CustomDiscountOnly = p.CustomDiscountOnly
			old.BrowserFeed = false
			old.AlertOnDiscountIncrease = p.AlertOnDiscountIncrease
			old.CooldownMin = p.CooldownMin
			old.AlertOnRestock = p.AlertOnRestock
			old.AlertOnPriceDrop = p.AlertOnPriceDrop
			old.Active = p.Active
			old.DellFamilyScan = p.DellFamilyScan
			old.DellScanMode = p.DellScanMode
			old.CorePending = false
			delete(a.corePending, p.ID)
			old.revision++
			old.Checking = false
			old.CheckState = ""
			old.nextCheck = time.Now()
			if modeChanged {
				markStale(old)
			}
			if changed {
				old.DellScanCursor = 0
				old.ScanScope = ""
				old.NeedsAction = ""
				old.ScanIncomplete = false
				old.LastTrusted = false
				old.Stale = true
				old.LastPrice = 0
				old.LastOriginal = 0
				old.LastDiscount = 0
				old.LastStock = ""
				old.LastSuccess = ""
				old.LastParser = ""
				old.LastSignature = ""
				old.DellResults = nil
				old.LenovoResults = nil
				old.LastError = "商品网址或扫描模式已修改，等待重新检查"
			}
			for i := range old.DellResults {
				old.DellResults[i].Matched = matches(old, old.DellResults[i])
			}
			for i := range old.LenovoResults {
				old.LenovoResults[i].Matched = matches(old, old.LenovoResults[i])
			}
		} else {
			a.mu.Unlock()
			http.Error(w, "商品不存在", 404)
			return
		}
		a.mu.Unlock()
		if e := a.persist(); e != nil {
			http.Error(w, "保存失败："+e.Error(), 500)
			return
		}
		jsonReply(w, map[string]any{"id": p.ID, "ok": true})
	})
	mux.HandleFunc("/api/delete", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		a.mu.Lock()
		delete(a.corePending, id)
		found := false
		for i, p := range a.store.Products {
			if p.ID == id {
				found = true
				if cancel := a.cancels[id]; cancel != nil {
					cancel()
					delete(a.cancels, id)
				}
				a.store.Products = append(a.store.Products[:i], a.store.Products[i+1:]...)
				break
			}
		}
		a.mu.Unlock()
		if !found {
			http.Error(w, "商品不存在", 404)
			return
		}
		if e := a.persist(); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/check", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		a.mu.RLock()
		exists := a.findLocked(id) != nil
		a.mu.RUnlock()
		if !exists {
			http.Error(w, "商品不存在", 404)
			return
		}
		queued := a.schedule(id)
		jsonReply(w, map[string]bool{"queued": queued})
	})
	mux.HandleFunc("/api/read-current", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		a.mu.RLock()
		exists := a.findLocked(id) != nil
		a.mu.RUnlock()
		if !exists {
			http.Error(w, "商品不存在", 404)
			return
		}
		jsonReply(w, map[string]bool{"queued": a.scheduleWith(id, a.readCurrentObservation)})
	})
	mux.HandleFunc("/api/checkall", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		ids := []string{}
		for _, p := range a.store.Products {
			ids = append(ids, p.ID)
		}
		a.mu.RUnlock()
		n := 0
		for _, id := range ids {
			if a.schedule(id) {
				n++
			}
		}
		jsonReply(w, map[string]int{"queued": n})
	})
	for _, path := range []string{"pauseall", "resumeall"} {
		path := path
		mux.HandleFunc("/api/"+path, func(w http.ResponseWriter, r *http.Request) {
			a.mu.Lock()
			a.store.Settings.GlobalPaused = path == "pauseall"
			if path == "pauseall" {
				for _, cancel := range a.cancels {
					cancel()
				}
			} else {
				for _, p := range a.store.Products {
					p.nextCheck = time.Now()
				}
			}
			a.mu.Unlock()
			if e := a.persist(); e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			jsonReply(w, map[string]bool{"ok": true})
		})
	}
	for _, path := range []string{"history", "dell-results", "model-results"} {
		path := path
		mux.HandleFunc("/api/"+path, func(w http.ResponseWriter, r *http.Request) {
			a.mu.RLock()
			defer a.mu.RUnlock()
			p := a.findLocked(r.URL.Query().Get("id"))
			if p == nil {
				http.Error(w, "商品不存在", 404)
				return
			}
			if path == "history" {
				v := p.History
				if v == nil {
					v = []HistoryPoint{}
				}
				jsonReply(w, v)
			} else {
				v := p.DellResults
				if path == "model-results" {
					v = modelResults(p)
				}
				v = visibleModelResults(p, v)
				jsonReply(w, v)
			}
		})
	}
	mux.HandleFunc("/api/alerts", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		defer a.mu.RUnlock()
		v := []Alert{}
		for _, x := range a.alerts {
			if !x.Acknowledged {
				v = append(v, x)
			}
		}
		jsonReply(w, v)
	})
	mux.HandleFunc("/api/ack", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		a.mu.Lock()
		for i := range a.alerts {
			if a.alerts[i].ID == id {
				a.alerts[i].Acknowledged = true
			}
		}
		a.mu.Unlock()
		jsonReply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/open-browser", func(w http.ResponseWriter, r *http.Request) {
		a.mu.RLock()
		p := a.findLocked(r.URL.Query().Get("id"))
		raw := ""
		if p != nil {
			raw = p.URL
		}
		a.mu.RUnlock()
		if raw == "" {
			http.Error(w, "商品不存在", 404)
			return
		}
		if a.normal.available && dellPlatform(raw) != "" {
			jsonReply(w, map[string]bool{"ok": true, "queued": a.schedule(r.URL.Query().Get("id"))})
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
			defer cancel()
			if e := a.browser.openVisible(ctx, raw); e != nil {
				a.mu.Lock()
				if p := a.findLocked(r.URL.Query().Get("id")); p != nil && p.URL == raw {
					p.LastError = "采集浏览器打开失败：" + e.Error()
				}
				a.mu.Unlock()
				_ = a.persist()
			}
		}()
		jsonReply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/heartbeat", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, map[string]bool{"ok": true}) })
	mux.HandleFunc("/api/exit", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, map[string]bool{"ok": true})
		go func() { time.Sleep(200 * time.Millisecond); a.cancel() }()
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Local API remains usable without exposing it to other websites or network hosts.
		host, _, e := net.SplitHostPort(r.Host)
		if e != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "仅允许本机访问", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host {
				http.Error(w, "拒绝跨网站请求", 403)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/products" && r.URL.Path != "/api/settings" && r.URL.Path != "/api/version" && r.URL.Path != "/api/history" && r.URL.Path != "/api/dell-results" && r.URL.Path != "/api/model-results" && r.URL.Path != "/api/alerts" && r.URL.Path != "/api/diagnostics" && r.Method != "POST" {
			http.Error(w, "需要 POST", 405)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		defer func() {
			if x := recover(); x != nil {
				http.Error(w, fmt.Sprint(x), 500)
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
