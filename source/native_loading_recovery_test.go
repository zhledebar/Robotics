package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeMissingDocumentWaitsInVerifiedWindowAndRecovers(t *testing.T) {
	page := capturedNative(t, true)
	req := nativeRequest{Action: "open", URL: liveXPSURL, Family: true}
	pending := nativePage{Handle: page.Handle, URL: page.URL, Error: "普通浏览器商品页面尚未加载"}
	opens, snapshots, validations := 0, 0, 0
	got, err := readNativeRegions(context.Background(), req, func(ctx context.Context, r nativeRequest) (nativePage, error) {
		if r.Action == "open" {
			opens++
			return pending, nil
		}
		if r.Handle != pending.Handle {
			t.Fatal("changed owned handle during loading")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("loading reads have no time budget")
		}
		switch r.Action {
		case "validate":
			validations++
			return pending, nil // Ownership/URL proved even while the document is absent.
		case "snapshot":
			snapshots++
			if snapshots == 5 {
				return page, nil
			}
			if snapshots >= 3 {
				return nativePage{Handle: pending.Handle, URL: pending.URL, Error: "普通浏览器没有提供可核对的商品区域"}, nil
			}
			return pending, nil
		default:
			t.Fatalf("repeated launch/action during loading: %s", r.Action)
		}
		return nativePage{}, nil
	}, time.Millisecond)
	if err != nil || got.Error != "" || opens != 1 || validations != 5 || snapshots != 5 {
		t.Fatalf("loading recovery failed: %+v %v calls=%d/%d/%d", got, err, opens, validations, snapshots)
	}
}

func TestNativeDocumentLoadingNeverRelaxesOwnershipOrActions(t *testing.T) {
	for _, mode := range []string{"no-url", "wrong-initial-handle", "wrong-url", "wrong-handle", "denied", "initial-title-denied", "title-denied", "ambiguous-document", "reset", "transport", "unperformed-refresh", "unperformed-custom", "canceled", "persistent"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := nativeRequest{Action: "open", Handle: 0, URL: liveXPSURL, Family: true}
			if mode == "unperformed-refresh" {
				req.Action = "refresh"
			}
			if mode == "unperformed-custom" {
				req.Action = "custom"
			}
			if mode == "wrong-initial-handle" {
				req.Handle = 100
			}
			pending := nativePage{Handle: 99, URL: liveXPSURL, Error: "普通浏览器商品页面尚未加载"}
			calls, snapshots := 0, 0
			got, err := readNativeRegionsWithin(ctx, req, func(_ context.Context, r nativeRequest) (nativePage, error) {
				calls++
				if calls == 1 {
					if mode == "no-url" {
						pending.URL = ""
					}
					if mode == "canceled" {
						cancel()
					}
					if mode == "initial-title-denied" {
						pending.Title = "Access Denied"
					}
					return pending, nil
				}
				if r.Action == "validate" {
					x := pending
					switch mode {
					case "wrong-url":
						x.URL = "https://example.com"
					case "wrong-handle":
						x.Handle = 100
					case "denied":
						x.Error = "普通浏览器网页访问被限制（access denied）"
					case "title-denied":
						x.Title = "Access Denied"
					case "ambiguous-document":
						x.Error = "后台当前商品文档不唯一；本轮没有接受报价"
					case "reset":
						x.ResetVerified = true
					case "transport":
						return x, errors.New("reader transport failed")
					}
					return x, nil
				}
				if r.Action != "snapshot" {
					t.Fatalf("unexpected action=%s", r.Action)
				}
				snapshots++
				return pending, nil
			}, time.Millisecond, 50*time.Millisecond)
			if mode == "persistent" {
				var q *nativeQuoteFailure
				if !errors.As(err, &q) || !strings.Contains(err.Error(), "等待超时") || actionForFailure(liveXPSURL, normalReaderError(err).Error()) != "" {
					t.Fatal("loading accepted, unbounded or paused")
				}
			} else if snapshots != 0 || (err == nil && got.Error == "" && !got.ResetVerified) {
				t.Fatalf("unsafe recovery: %+v %v snapshots=%d", got, err, snapshots)
			}
		})
	}
}

func TestNativeLoadingSnapshotIdentityCheckedBeforeSuccess(t *testing.T) {
	for _, wrong := range []string{"url", "handle"} {
		t.Run(wrong, func(t *testing.T) {
			calls := 0
			_, err := readNativeRegions(context.Background(), nativeRequest{Action: "open", URL: liveXPSURL, Family: true}, func(_ context.Context, r nativeRequest) (nativePage, error) {
				calls++
				p := nativePage{Handle: 99, URL: liveXPSURL}
				if calls == 1 {
					p.Error = "普通浏览器商品页面尚未加载"
				}
				if r.Action == "snapshot" {
					if wrong == "url" {
						p.URL = "https://example.com"
					} else {
						p.Handle = 100
					}
				}
				return p, nil
			}, time.Millisecond)
			if err == nil || calls != 3 {
				t.Fatalf("wrong successful snapshot accepted: %v calls=%d", err, calls)
			}
		})
	}
}

func TestNativePendingDocumentSourceKeepsIdentityAndUniqueDocumentBoundary(t *testing.T) {
	raw, err := os.ReadFile("normal_reader.ps1")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	start, end := strings.Index(s, "private static PMPage PendingPage("), strings.Index(s, "private static PMPage Capture(")
	if start < 0 || end <= start {
		t.Fatal("missing pending document identity helper")
	}
	for _, required := range []string{"Validate(h,expected,family)", "handle=h.ToInt64()", "url=Address(root)", "error=reason"} {
		if !strings.Contains(s[start:end], required) {
			t.Fatal("unverified pending document:", required)
		}
	}
	if strings.Count(s, `if(doc==null)return PendingPage(h,expected,family,"普通浏览器商品页面尚未加载");`) != 2 || !strings.Contains(s, "if(document!=null)throw new Exception(\"后台当前商品文档不唯一") {
		t.Fatal("missing document confused with ambiguous document")
	}
}

func TestNativeCustomStockMustStabilizeWithTheQuote(t *testing.T) {
	p := capturedNative(t, true)
	a, err := nativeObservation(p, liveXPSURL, true)
	if err != nil {
		t.Fatal(err)
	}
	b := a
	b.Results = append([]DellResult(nil), a.Results...)
	b.Results[0].Stock, b.Results[0].Confirmed = stockUnknown, false
	if nativeSignature(p, a) == nativeSignature(p, b) {
		t.Fatal("custom availability changed without resetting stable quote evidence")
	}
}
