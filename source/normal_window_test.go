package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func windowTestPage(t *testing.T, raw string) nativePage {
	t.Helper()
	p := capturedNative(t, false)
	if dellPlatform(raw) != dellPlatform(liveXPSURL) {
		for _, root := range p.Regions {
			walkNative(root, func(n *nativeNode) {
				n.Name = strings.ReplaceAll(n.Name, "da16260", "pc16250")
				n.ID = strings.ReplaceAll(n.ID, "da16260", "pc16250")
			})
		}
	}
	p.CanCustom = false
	p.URL = raw
	return p
}

func TestNormalBrowserOneWindowAcrossProductsAndExit(t *testing.T) {
	b := newNormalBrowser()
	a, p := product("xps"), product("pro")
	a.URL, a.DellFamilyScan = liveXPSURL, true
	p.URL, p.DellFamilyScan = "https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250/pc16250_fixed_22", true
	active, maximum, opens, refreshes, closes, cleanups := 0, 0, 0, 0, 0, 0
	var current nativePage
	var currentOwner string
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		switch r.Action {
		case "open":
			if active != 0 || r.Handle != 0 {
				t.Fatal("second live collection window opened")
			}
			active++
			opens++
			if active > maximum {
				maximum = active
			}
			current = windowTestPage(t, r.URL)
			current.Handle = int64(100 + opens)
			currentOwner = r.Owner
		case "refresh":
			refreshes++
			if active != 1 || r.Handle != current.Handle || r.URL != current.URL {
				t.Fatal("refresh targets another product or window")
			}
		case "close":
			if active != 1 || r.Handle != current.Handle || r.URL != current.URL {
				t.Fatal("unowned or wrong window closed")
			}
			active--
			closes++
			return nativePage{}, nil
		case "cleanup":
			if active != 0 || r.Owner != currentOwner {
				t.Fatal("exit cleanup did not preserve ownership scope")
			}
			cleanups++
			return nativePage{}, nil
		case "snapshot":
		default:
			t.Fatalf("unexpected action %q", r.Action)
		}
		return current, nil
	}
	for _, item := range []*Product{a, p, a} {
		if _, err := b.collect(context.Background(), *item, func(string) {}); err != nil {
			t.Fatal(err)
		}
	}
	duplicate := *a
	duplicate.ID = "same-url-another-product"
	if _, err := b.collect(context.Background(), duplicate, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if err := b.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if maximum != 1 || active != 0 || opens != 3 || closes != 3 || refreshes != 1 || cleanups != 1 {
		t.Fatalf("window lifecycle max=%d active=%d open=%d close=%d refresh=%d cleanup=%d", maximum, active, opens, closes, refreshes, cleanups)
	}
}

func TestNormalBrowserFailedCloseNeverOpensMoreWindows(t *testing.T) {
	for _, reason := range []string{"采集窗口有其他标签页", "旧窗口尚未关闭", "call-error"} {
		t.Run(reason, func(t *testing.T) {
			b := newNormalBrowser()
			b.window = nativeWindow{Handle: 99, Owner: "owner", URL: liveXPSURL}
			calls := 0
			b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
				calls++
				if r.Action != "close" {
					t.Fatal("opened another window after failed close")
				}
				if reason == "call-error" {
					return nativePage{}, errors.New("process failure")
				}
				return nativePage{Handle: 99, Error: reason}, nil
			}
			p := product("other")
			p.URL = strings.Replace(liveXPSURL, "da16260_reg_01", "da16260_fixed_71", 1)
			if _, err := b.collect(context.Background(), *p, func(string) {}); err == nil || calls != 1 || b.window.Handle != 99 {
				t.Fatalf("close failure lost ownership or retried: %v %d %+v", err, calls, b.window)
			}
		})
	}
}

func TestNormalBrowserCaptureErrorRetainsOwnedWindow(t *testing.T) {
	b := newNormalBrowser()
	page := windowTestPage(t, liveXPSURL)
	opens, refreshes := 0, 0
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		switch r.Action {
		case "open":
			opens++
			return nativePage{Handle: 99, Error: "商品区域尚未加载"}, nil
		case "refresh":
			refreshes++
			if r.Handle != 99 {
				t.Fatal("failed capture lost the tagged handle")
			}
		}
		return page, nil
	}
	p := product("failed")
	p.URL, p.DellFamilyScan = liveXPSURL, true
	if _, err := b.collect(context.Background(), *p, func(string) {}); err == nil {
		t.Fatal("failed capture accepted")
	}
	if _, err := b.collect(context.Background(), *p, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if opens != 1 || refreshes != 1 {
		t.Fatalf("failure created repeated browser windows: open=%d refresh=%d", opens, refreshes)
	}
}

func TestNormalBrowserUnconfirmedLaunchNeverOpensAgain(t *testing.T) {
	for _, mode := range []string{"transport-error", "opened-but-unidentified"} {
		t.Run(mode, func(t *testing.T) {
			b := newNormalBrowser()
			calls := 0
			b.call = func(context.Context, nativeRequest) (nativePage, error) {
				calls++
				if mode == "transport-error" {
					return nativePage{}, errors.New("unexpected startup text")
				}
				return nativePage{Launched: true, Error: "商品窗口打开后未能识别"}, nil
			}
			p := product("one")
			p.URL, p.DellFamilyScan = liveXPSURL, true
			if _, err := b.collect(context.Background(), *p, func(string) {}); err == nil {
				t.Fatal("missing result accepted")
			}
			p.ID = "two"
			p.URL = strings.Replace(liveXPSURL, "da16260_reg_01", "da16260_fixed_71", 1)
			if _, err := b.collect(context.Background(), *p, func(string) {}); err == nil || calls != 1 {
				t.Fatalf("uncertain launch allowed another window: %v calls=%d", err, calls)
			}
		})
	}
}

func TestNormalBrowserIdleReleasesWindow(t *testing.T) {
	b := newNormalBrowser()
	b.idleTTL = 20 * time.Millisecond
	p := product("idle")
	p.URL, p.DellFamilyScan = liveXPSURL, true
	page := windowTestPage(t, p.URL)
	closed := make(chan struct{})
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "close" {
			close(closed)
			return nativePage{}, nil
		}
		if r.Action == "cleanup" {
			return nativePage{}, nil
		}
		return page, nil
	}
	if _, err := b.collect(context.Background(), *p, func(string) {}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("idle window was left consuming resources")
	}
	if err := b.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.window.Handle != 0 {
		t.Fatal("idle window handle retained")
	}
}

func TestNormalBrowserConcurrentChecksShareOneWindow(t *testing.T) {
	b := newNormalBrowser()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	active := 0
	var page nativePage
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "close" {
			active--
			return nativePage{}, nil
		}
		if r.Action == "open" {
			active++
			if active != 1 {
				return nativePage{}, errors.New("parallel window overlap")
			}
			page = windowTestPage(t, r.URL)
		}
		return page, nil
	}
	results := make(chan error, 2)
	for i, raw := range []string{liveXPSURL, "https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250/pc16250_fixed_22"} {
		p := product(string(rune('a' + i)))
		p.URL, p.DellFamilyScan = raw, true
		go func(p Product) {
			_, err := b.collect(ctx, p, func(string) {})
			results <- err
		}(*p)
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if active != 1 {
		t.Fatalf("incorrect number of live windows %d", active)
	}
}
