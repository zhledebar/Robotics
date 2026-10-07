package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/websocket"
)

type browserSession struct {
	cmd     *exec.Cmd
	port    string
	visible bool
	done    chan struct{}
	waitErr error
}
type BrowserManager struct {
	gate     chan struct{}
	mu       sync.Mutex
	sessions map[string]*browserSession
	dir      string
}

func newBrowserManager(dir string) *BrowserManager {
	return &BrowserManager{gate: make(chan struct{}, 1), sessions: map[string]*browserSession{}, dir: dir}
}
func browserExecutable() string {
	if v := os.Getenv("MONITOR_BROWSER"); v != "" {
		return v
	}
	for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if root == "" {
			continue
		}
		for _, x := range []string{`Microsoft/Edge/Application/msedge.exe`, `Google/Chrome/Application/chrome.exe`} {
			p := filepath.Join(root, filepath.FromSlash(x))
			if _, e := os.Stat(p); e == nil {
				return p
			}
		}
	}
	for _, x := range []string{"msedge", "google-chrome", "chromium", "chromium-browser"} {
		if p, e := exec.LookPath(x); e == nil {
			return p
		}
	}
	return ""
}
func pauseContext(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
func (b *BrowserManager) acquire(ctx context.Context) error {
	select {
	case b.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (b *BrowserManager) release() { <-b.gate }
func (b *BrowserManager) session(ctx context.Context, site string, visible bool) (*browserSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.sessions[site]; s != nil {
		if s.alive(ctx) && (!visible || s.visible) {
			return s, nil
		}
		killOwned(s.cmd)
		delete(b.sessions, site)
	}
	exe := browserExecutable()
	if exe == "" {
		return nil, errors.New("未找到 Edge/Chrome；需使用电脑已安装的浏览器读取动态页面")
	}
	profile := filepath.Join(b.dir, "browser_profile_v75", site)
	if err := os.MkdirAll(profile, 0700); err != nil {
		return nil, err
	}
	portFile := filepath.Join(profile, "DevToolsActivePort")
	_ = os.Remove(portFile)
	args := []string{"--user-data-dir=" + profile, "--remote-debugging-port=0", "--remote-debugging-address=127.0.0.1", "--remote-allow-origins=http://localhost", "--no-first-run", "--no-default-browser-check", "about:blank"}
	if !visible {
		args = append([]string{"--headless=new"}, args...)
	}
	cmd := exec.Command(exe, args...)
	hideCommand(cmd, !visible)
	logFile, logErr := os.OpenFile(filepath.Join(profile, "browser_startup.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if logErr == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("采集浏览器启动失败：%w", err)
	}
	s := &browserSession{cmd: cmd, visible: visible, done: make(chan struct{})}
	go func() {
		s.waitErr = cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
		close(s.done)
	}()
	for i := 0; i < 40; i++ {
		if err := pauseContext(ctx, 250*time.Millisecond); err != nil {
			killOwned(cmd)
			return nil, err
		}
		select {
		case <-s.done:
			return nil, fmt.Errorf("采集浏览器启动后退出（%v）；详情见 browser_startup.log", s.waitErr)
		default:
		}
		v, e := os.ReadFile(portFile)
		if e == nil {
			p := strings.Split(strings.TrimSpace(string(v)), "\n")[0]
			if _, e := url.Parse("http://127.0.0.1:" + p); e == nil && p != "" {
				s.port = p
				b.sessions[site] = s
				return s, nil
			}
		}
	}
	killOwned(cmd)
	return nil, errors.New("采集浏览器启动超时；检查 Edge/Chrome 是否被系统策略禁止")
}
func (s *browserSession) alive(ctx context.Context) bool {
	if s.done != nil {
		select {
		case <-s.done:
			return false
		default:
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://127.0.0.1:"+s.port+"/json/version", nil)
	if err != nil {
		return false
	}
	r, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	_ = r.Body.Close()
	return r.StatusCode == 200
}
func (b *BrowserManager) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range b.sessions {
		killOwned(s.cmd)
	}
	b.sessions = map[string]*browserSession{}
}

type cdpClient struct {
	ws  *websocket.Conn
	seq int
}

const browserHTMLExpr = "document.documentElement ? document.documentElement.outerHTML : ''"

type browserPagePending struct{ Reason string }

func (e *browserPagePending) Error() string {
	return "浏览器页面暂未就绪：" + e.Reason + "；下轮自动重验"
}

func transientCDPError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "execution context was destroyed") || strings.Contains(s, "cannot find context") || strings.Contains(s, "cannot find execution context") || strings.Contains(s, "inspected target navigated")
}

func (c *cdpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.seq++
	id := c.seq
	deadline := time.Now().Add(25 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.ws.SetDeadline(deadline)
	if err := websocket.JSON.Send(c.ws, map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		var r struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := websocket.JSON.Receive(c.ws, &r); err != nil {
			return nil, err
		}
		if r.ID == id {
			if r.Error != nil {
				return nil, errors.New(r.Error.Message)
			}
			return r.Result, nil
		}
	}
}
func (c *cdpClient) eval(ctx context.Context, expr string) (string, error) {
	b, e := c.call(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true})
	if e != nil {
		return "", e
	}
	var v struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	if e = json.Unmarshal(b, &v); e != nil {
		return "", e
	}
	if len(v.Exception) > 0 {
		log.Printf("browser script exception: %s", v.Exception)
		if expr == browserHTMLExpr || expr == "location.href" {
			return "", &browserPagePending{Reason: "网页读取遇到脚本异常，详细原因已记入程序日志"}
		}
		if strings.HasPrefix(expr, "("+ownedDOMScript+")(") || strings.HasPrefix(expr, "("+ownedConfirmScript+")(") {
			return "", ownedDOMScriptFailure(v.Exception)
		}
		return "", fmt.Errorf("网页脚本执行失败；详细原因已记入程序日志")
	}
	var s string
	if e = json.Unmarshal(v.Result.Value, &s); e != nil {
		return "", e
	}
	return s, nil
}
func openTarget(ctx context.Context, s *browserSession, raw string) (*cdpClient, string, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodPut, "http://127.0.0.1:"+s.port+"/json/new?"+url.QueryEscape(raw), nil)
	if e != nil {
		return nil, "", e
	}
	client := &http.Client{Timeout: 15 * time.Second}
	r, e := client.Do(req)
	if e != nil {
		return nil, "", e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, "", fmt.Errorf("采集浏览器接口 HTTP %d", r.StatusCode)
	}
	var t struct {
		ID string `json:"id"`
		WS string `json:"webSocketDebuggerUrl"`
	}
	if e = jsonDecode(r.Body, &t); e != nil {
		return nil, "", e
	}
	wcfg, e := websocket.NewConfig(t.WS, "http://localhost")
	if e != nil {
		return nil, "", e
	}
	ws, e := websocket.DialConfig(wcfg)
	if e != nil {
		return nil, "", e
	}
	return &cdpClient{ws: ws}, t.ID, nil
}
func closeTarget(s *browserSession, id string) {
	r, e := (&http.Client{Timeout: 3 * time.Second}).Get("http://127.0.0.1:" + s.port + "/json/close/" + id)
	if e == nil {
		_ = r.Body.Close()
	}
}

func (b *BrowserManager) hasVisible(raw string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[siteName(raw)]
	return s != nil && s.visible
}

func matchingCollectorURL(current, wanted string, family bool) bool {
	a, e := url.Parse(current)
	b, f := url.Parse(wanted)
	if e != nil || f != nil || a.Scheme != b.Scheme || !strings.EqualFold(a.Host, b.Host) {
		return false
	}
	a.Fragment, b.Fragment = "", ""
	if family && siteName(wanted) == "dell" {
		clean := func(u *url.URL) string {
			p := strings.TrimRight(u.Path, "/")
			if offerFromURL(u.String()) != "" {
				p = p[:strings.LastIndex(p, "/")]
			}
			return p
		}
		return clean(a) == clean(b) && strings.Contains(clean(b), "/spd/")
	}
	return strings.TrimRight(a.Path, "/") == strings.TrimRight(b.Path, "/") && a.Query().Encode() == b.Query().Encode()
}

// Only list targets in the browser process that this application started.
// Never discover/debug the user's unrelated browser or attach to another port.
func existingTarget(ctx context.Context, s *browserSession, raw string, family bool) (*cdpClient, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+s.port+"/json/list", nil)
	if err != nil {
		return nil, "", "", err
	}
	r, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, "", "", fmt.Errorf("采集页面列表 HTTP %d", r.StatusCode)
	}
	var targets []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		URL  string `json:"url"`
		WS   string `json:"webSocketDebuggerUrl"`
	}
	if err = jsonDecode(r.Body, &targets); err != nil {
		return nil, "", "", err
	}
	for _, target := range targets {
		if target.Type != "page" || !matchingCollectorURL(target.URL, raw, family) {
			continue
		}
		wsURL, e := url.Parse(target.WS)
		if e != nil || wsURL.Scheme != "ws" || wsURL.Host != "127.0.0.1:"+s.port {
			return nil, "", "", errors.New("采集页面调试地址不是本程序的本机端口")
		}
		cfg, e := websocket.NewConfig(target.WS, "http://localhost")
		if e != nil {
			return nil, "", "", e
		}
		ws, e := websocket.DialConfig(cfg)
		if e != nil {
			return nil, "", "", e
		}
		return &cdpClient{ws: ws}, target.ID, target.URL, nil
	}
	return nil, "", "", nil
}

func (b *BrowserManager) readCurrent(ctx context.Context, raw string, family bool, fn func(*cdpClient, string, string) (Observation, error)) (Observation, error) {
	if err := b.acquire(ctx); err != nil {
		return Observation{}, err
	}
	defer b.release()
	b.mu.Lock()
	s := b.sessions[siteName(raw)]
	b.mu.Unlock()
	if s == nil || !s.visible || !s.alive(ctx) {
		return Observation{}, errors.New("请先打开本商品的采集浏览器；没有重试网站请求")
	}
	c, _, current, err := existingTarget(ctx, s, raw, family)
	if err != nil {
		return Observation{}, err
	}
	if c == nil {
		return Observation{}, errors.New("采集窗口未打开目标商品；没有读取其他页面")
	}
	defer c.ws.Close()
	actual, err := c.eval(ctx, "location.href")
	if err != nil {
		return Observation{}, err
	}
	if !matchingCollectorURL(actual, raw, family) {
		return Observation{}, errors.New("采集页面已跳转；未使用其他商品报价")
	}
	current = actual
	body, err := waitBrowserPage(ctx, c, current)
	if err != nil {
		return Observation{}, err
	}
	return fn(c, body, current)
}

func (b *BrowserManager) read(ctx context.Context, raw string, fn func(*cdpClient, string) (Observation, error)) (Observation, error) {
	if e := b.acquire(ctx); e != nil {
		return Observation{}, e
	}
	defer b.release()
	s, e := b.session(ctx, siteName(raw), false)
	if e != nil {
		return Observation{}, e
	}
	if !s.visible {
		// The headless fallback owns its process and profile. Release it after
		// this serialized check, including failures, rather than retain it forever.
		defer func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.sessions[siteName(raw)] == s {
				killOwned(s.cmd)
				delete(b.sessions, siteName(raw))
			}
		}()
	}
	var c *cdpClient
	var id string
	reused := false
	if s.visible {
		c, id, _, e = existingTarget(ctx, s, raw, siteName(raw) == "dell")
		reused = c != nil
		if e != nil {
			return Observation{}, e
		}
	}
	if c == nil {
		c, id, e = openTarget(ctx, s, raw)
	}
	if e != nil {
		b.mu.Lock()
		killOwned(s.cmd)
		delete(b.sessions, siteName(raw))
		b.mu.Unlock()
		return Observation{}, e
	}
	defer c.ws.Close()
	if reused {
		// One normal refresh per scheduled check. Reading a cached page forever
		// would falsely report old quotes as newly checked.
		if _, e = c.call(ctx, "Page.navigate", map[string]any{"url": raw}); e != nil {
			return Observation{}, e
		}
	} else if !s.visible {
		defer closeTarget(s, id)
	}
	body, e := waitBrowserPage(ctx, c, raw)
	if e != nil {
		return Observation{}, e
	}
	return fn(c, body)
}
func waitBrowserPage(ctx context.Context, c *cdpClient, raw string) (string, error) {
	previous := ""
	var body string
	var e error
	var lastParseError error
	for i := 0; i < 40; i++ {
		if e = pauseContext(ctx, 750*time.Millisecond); e != nil {
			return "", e
		}
		body, e = c.eval(ctx, browserHTMLExpr)
		if e != nil {
			var pending *browserPagePending
			if errors.As(e, &pending) || transientCDPError(e) {
				lastParseError = &browserPagePending{Reason: "页面正在加载或切换，尚未取得完整网页"}
				previous = ""
				continue
			}
			return "", e
		}
		if len(body) == 0 {
			lastParseError = &browserPagePending{Reason: "网页根节点尚未加载"}
			previous = ""
			continue
		}
		r, _ := rootHTML(body)
		if e = pageProblem(r); e != nil {
			return "", e
		}
		if vipURL(raw) {
			current, err := c.eval(ctx, "location.href")
			if err != nil {
				var pending *browserPagePending
				if errors.As(err, &pending) || transientCDPError(err) {
					lastParseError = &browserPagePending{Reason: "会员页面导航尚未完成"}
					previous = ""
					continue
				}
				return "", fmt.Errorf("无法核对会员页面所在店铺：%w", err)
			}
			if current == "about:blank" {
				continue
			}
			if !vipURL(current) {
				return "", errors.New("会员商品网址跳转到非会员页面；未使用普通商店价格")
			}
		}
		o, pe := parsePage(body, raw, false)
		lastParseError = pe
		if pe == nil && len(body) > 1000 {
			sig := observationSignature(o)
			if siteName(raw) == "dell" && len(configGroups(body)) > 0 {
				selected, _ := json.Marshal(selectedMap(body))
				sig += string(selected)
			}
			if sig == previous {
				return body, nil
			}
			previous = sig
		} else {
			previous = ""
		}
	}
	detail := ""
	if siteName(raw) == "lenovo" {
		r, _ := rootHTML(body)
		cards := len(nodes(r, func(n *html.Node) bool { return hasClass(n, "dlp-product-card") && !hiddenAncestor(n) }))
		detail = fmt.Sprintf("；目标 %s；型号卡片 %d 项", lenovoURLPart(raw), cards)
		if vipURL(raw) {
			if lenovoMemberStore(r, raw) {
				detail += "；会员店铺标识已加载"
			} else {
				detail += "；会员店铺标识未加载"
			}
		}
	}
	if lastParseError != nil {
		return "", fmt.Errorf("动态网页 30 秒内未出现可信价格：%w%s", lastParseError, detail)
	}
	return "", errors.New("动态网页 30 秒内报价未稳定；保留上次数据" + detail)
}
func (b *BrowserManager) openVisible(ctx context.Context, raw string) error {
	if e := b.acquire(ctx); e != nil {
		return e
	}
	defer b.release()
	s, e := b.session(ctx, siteName(raw), true)
	if e != nil {
		return e
	}
	c, _, _, e := existingTarget(ctx, s, raw, false)
	if e != nil {
		return e
	}
	if c == nil {
		c, _, e = openTarget(ctx, s, raw)
	}
	if c != nil {
		_ = c.ws.Close()
	}
	return e
}

type ConfigOption struct {
	ID, Label          string
	Selected, Disabled bool
	Delta              float64
}
type ConfigGroup struct {
	ID, Label string
	Options   []ConfigOption
}

func configGroups(body string) []ConfigGroup {
	r, _ := rootHTML(body)
	for _, n := range nodes(r, func(n *html.Node) bool { return n.Data == "body" }) {
		// Unified Dell pages retain both views; CSS uses this body flag.
		if hasClass(n, "upd-superconfig") && !hasClass(n, "show-custom-order") {
			return nil
		}
	}
	var groups []ConfigGroup
	for _, n := range nodes(r, func(n *html.Node) bool { return attr(n, "data-module-id") != "" && attr(n, "role") == "group" }) {
		g := ConfigGroup{ID: attr(n, "data-module-id"), Label: attr(n, "aria-label")}
		for _, x := range nodes(n, func(n *html.Node) bool { return attr(n, "data-option-id") != "" && attr(n, "data-is-selected") != "" }) {
			id := attr(x, "data-option-id")
			selected := attr(x, "data-is-selected") == "true"
			status := attr(x, "data-status")
			label := ""
			for _, title := range nodes(x, func(n *html.Node) bool { return attr(n, "data-test-id") == "option-title" }) {
				label = textOf(title)
				break
			}
			if label == "" {
				label = attr(x, "aria-label")
			}
			d := amount(attr(x, "aria-label"))
			if strings.Contains(attr(x, "aria-label"), "– $") || strings.Contains(attr(x, "aria-label"), "− $") || strings.Contains(attr(x, "aria-label"), "- $") {
				d = -d
			}
			if selected {
				d = 0
			}
			g.Options = append(g.Options, ConfigOption{ID: id, Label: label, Selected: selected, Disabled: attr(x, "aria-disabled") == "true" || status == "unavailable" || status == "disabled", Delta: d})
		}
		if len(g.Options) > 0 {
			groups = append(groups, g)
		}
	}
	return groups
}
func coreGroup(label string) bool {
	switch strings.ToLower(label) {
	case "processor", "storage", "displays", "display", "memory", "graphics card":
		return true
	}
	return false
}
func selectedMap(body string) map[string]string {
	v := map[string]string{}
	for _, g := range configGroups(body) {
		for _, o := range g.Options {
			if o.Selected {
				v[g.ID] = o.ID
			}
		}
	}
	return v
}
func currentBYO(body, raw string) (DellResult, error) {
	o, e := parseDell(body, raw, false)
	if e != nil {
		return DellResult{}, e
	}
	r := DellResult{OfferID: offerFromURL(raw), Source: "BYO 已核价", Price: o.Price, Original: o.Original, Discount: o.Discount, Stock: o.Stock, URL: raw, Confirmed: o.Stock != stockUnknown, Options: selectedMap(body)}
	r.Custom = true
	r.DiscountConfirmed = o.Price > 0 && o.Original >= o.Price
	for _, g := range configGroups(body) {
		for _, q := range g.Options {
			if q.Selected {
				switch strings.ToLower(g.Label) {
				case "processor":
					r.CPU = q.Label
				case "graphics card":
					r.GPU = q.Label
				case "memory":
					r.Memory = q.Label
				case "storage":
					r.Storage = q.Label
				case "displays", "display":
					r.Display = q.Label
				}
			}
		}
	}
	root, _ := rootHTML(body)
	for _, s := range nodes(root, func(n *html.Node) bool {
		return hasClass(n, "add-to-cart-stack") || attr(n, "id") == "add-to-cart-stack"
	}) {
		r.Stock = scopedStock(s)
		r.Confirmed = r.Stock != stockUnknown
		break
	}
	if len(r.Options) == 0 {
		return DellResult{}, errors.New("BYO 选项未加载")
	}
	return r, nil
}

// Each branch is queried after the preceding choice. Disabled/incompatible choices are skipped.
// No arithmetic estimate is eligible for alerts or trusted history.
func scanBYO(ctx context.Context, c *cdpClient, body, raw string, progress func(string)) ([]DellResult, bool, error) {
	groups := configGroups(body)
	var ids []string
	for _, g := range groups {
		if coreGroup(g.Label) {
			ids = append(ids, g.ID)
		}
	}
	if len(ids) == 0 {
		return nil, false, errors.New("未识别到 BYO 核心选项")
	}
	var results []DellResult
	seen := map[string]bool{}
	partial := false
	var visit func(int) error
	visit = func(level int) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(results) >= 250 {
			partial = true
			return nil
		}
		body, e := c.eval(ctx, browserHTMLExpr)
		if e != nil {
			return e
		}
		rt, _ := rootHTML(body)
		if e = pageProblem(rt); e != nil {
			return e
		}
		if level == len(ids) {
			r, e := currentBYO(body, raw)
			if e != nil {
				return e
			}
			keys := make([]string, 0, len(r.Options))
			for k, v := range r.Options {
				keys = append(keys, k+"="+v)
			}
			sortStrings(keys)
			key := strings.Join(keys, "&")
			if seen[key] {
				return nil
			}
			seen[key] = true
			r.OfferID = "BYO-" + shortHash(key)
			r.URL = raw
			r.Note = "已核对页面选项与现价；打开网页需按此行选项重新选择"
			results = append(results, r)
			progress(fmt.Sprintf("BYO 已核价 %d 项", len(results)))
			return nil
		}
		var group ConfigGroup
		for _, g := range configGroups(body) {
			if g.ID == ids[level] {
				group = g
				break
			}
		}
		if len(group.Options) == 0 {
			partial = true
			return nil
		}
		for _, q := range group.Options {
			if q.Disabled {
				continue
			}
			idJSON, _ := json.Marshal(q.ID)
			// First ensure this option still exists in the current dependent configuration.
			v, e := c.eval(ctx, `(()=>{const e=[...document.querySelectorAll('[data-option-id]')].find(e=>e.dataset.optionId===`+string(idJSON)+`);if(!e||e.getAttribute('aria-disabled')==='true'||e.dataset.status==='unavailable')return 'skip';if(e.dataset.isSelected==='true')return 'selected';e.click();return 'clicked'})()`)
			if e != nil {
				return e
			}
			if v == "skip" {
				continue
			}
			if v == "clicked" {
				stable := ""
				settled := false
				for i := 0; i < 18; i++ {
					if e = pauseContext(ctx, 500*time.Millisecond); e != nil {
						return e
					}
					now, e := c.eval(ctx, browserHTMLExpr)
					if e != nil {
						return e
					}
					selected := selectedMap(now)
					if selected[group.ID] != q.ID {
						continue
					}
					o, pe := parseDell(now, raw, false)
					if pe != nil {
						continue
					}
					sig := observationSignature(o) + fmt.Sprint(selected)
					if sig == stable {
						settled = true
						break
					}
					stable = sig
				}
				if !settled {
					partial = true
					return errors.New("BYO 选项切换未得到稳定确认")
				}
			}
			if e = visit(level + 1); e != nil {
				return e
			}
			if len(results) >= 250 {
				partial = true
				return nil
			}
		}
		return nil
	}
	e := visit(0)
	if e != nil && len(results) == 0 {
		return nil, true, e
	}
	if e != nil {
		partial = true
	}
	return results, partial, e
}
