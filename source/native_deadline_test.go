package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNativeLoadingCanRecoverAfterEightReads(t *testing.T) {
	page := capturedNative(t, true)
	pending := nativePage{Handle: page.Handle, URL: page.URL, Error: "普通浏览器商品页面尚未加载"}
	reads, opens := 0, 0
	got, err := readNativeRegionsWithin(context.Background(), nativeRequest{Action: "open", URL: liveXPSURL, Family: true}, func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "open" {
			opens++
		} else if r.Handle != page.Handle {
			t.Fatal("window changed")
		}
		if r.Action == "snapshot" {
			reads++
			if reads == 12 {
				return page, nil
			}
		}
		return pending, nil
	}, time.Millisecond, time.Second)
	if err != nil || got.Error != "" || reads != 12 || opens != 1 {
		t.Fatalf("late document rejected: reads=%d opens=%d page=%+v err=%v", reads, opens, got, err)
	}
}

func TestNativeLoadingCanceledSnapshotCannotBecomeSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page := capturedNative(t, true)
	_, err := readNativeRegionsWithin(ctx, nativeRequest{Action: "open", URL: liveXPSURL, Family: true}, func(_ context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "snapshot" {
			cancel()
			return page, nil
		}
		p := page
		p.Error = "普通浏览器商品页面尚未加载"
		return p, nil
	}, time.Millisecond, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot accepted: %v", err)
	}
}

func TestNativeLoadingStopsAtActualBudgetWithoutAcceptingOldQuote(t *testing.T) {
	page := capturedNative(t, true)
	page.Error = "普通浏览器商品页面尚未加载"
	start := time.Now()
	budget := 35 * time.Millisecond
	reads := 0
	got, err := readNativeRegionsWithin(context.Background(), nativeRequest{Action: "open", URL: liveXPSURL, Family: true}, func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "snapshot" {
			reads++
		}
		return page, nil
	}, time.Millisecond, budget)
	var q *nativeQuoteFailure
	if !errors.As(err, &q) || !strings.Contains(err.Error(), "等待超时") || got.Error == "" || time.Since(start) < budget || reads == 0 {
		t.Fatalf("budget ignored or old regions accepted: reads=%d elapsed=%s err=%v", reads, time.Since(start), err)
	}
}

func TestNativeContentProbeCannotTurnDOMReadinessIntoSuccess(t *testing.T) {
	for _, mode := range []string{"ready-dom", "probe-error", "wrong-handle", "wrong-url", "unperformed-action"} {
		t.Run(mode, func(t *testing.T) {
			req := nativeRequest{Action: "snapshot", Handle: 99, URL: liveXPSURL, Family: true}
			pending := nativePage{Handle: 99, URL: liveXPSURL, Error: "普通浏览器商品页面尚未加载"}
			if mode == "wrong-handle" {
				pending.Handle = 100
			}
			if mode == "wrong-url" {
				pending.URL = "https://example.com"
			}
			if mode == "unperformed-action" {
				req.Action = "custom"
			}
			probes := 0
			reader := withNativeContentProbe(func(context.Context, nativeRequest) (nativePage, error) { return pending, nil }, func(ctx context.Context, p nativePage, r nativeRequest) (string, error) {
				probes++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded diagnostic")
				}
				if mode == "probe-error" {
					return "", errors.New("DOM unavailable")
				}
				return `{"readyState":"complete","regions":{"hero-section":{"present":true,"visible":true}}}`, nil
			})
			for i := 0; i < 15; i++ {
				got, err := reader(context.Background(), req)
				if err != nil || got.Error != pending.Error || got.DOMQuote != nil || len(got.Regions) != 0 {
					t.Fatalf("diagnostic evidence became quote/success: %+v %v", got, err)
				}
			}
			want := 0
			if mode == "ready-dom" || mode == "probe-error" {
				want = 2
			}
			if probes != want {
				t.Fatalf("unsafe/unbounded probe count %d expected %d", probes, want)
			}
		})
	}
}

func TestNativeContentStatusRejectsNavigationAndDiscardsQuoteFields(t *testing.T) {
	p := nativePage{URL: liveXPSURL}
	status, err := verifyOwnedContentStatus(p, `{"url":"`+liveXPSURL+`?variant=selected#configuration","readyState":"complete","price":100,"stock":"有货","confirmed":true}`)
	if err != nil || strings.Contains(status, "price") || strings.Contains(status, "stock") || strings.Contains(status, "confirmed") {
		t.Fatalf("diagnostic leaked quote: %s %v", status, err)
	}
	if _, err = verifyOwnedContentStatus(p, `{"url":"https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250/pc16250_fixed_22","readyState":"complete"}`); err == nil {
		t.Fatal("wrong document diagnosed as owned page")
	}
}
