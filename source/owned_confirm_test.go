package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type confirmTransport struct {
	clickTransportModel
	action string
}

func (m *confirmTransport) eval(ctx context.Context, expr string) (string, error) {
	m.plans++
	hit := strings.Contains(expr, `"dom_phase":"hit"`)
	if hit && m.mode == "covered" {
		return "", errors.New("配置确认按钮被遮挡")
	}
	group, option, target := "moduleCPU", "CPU-B", "selection-modal-change-btn"
	if m.action != "select" {
		group, option, target = "", "", "scrm-continue"
	}
	p := map[string]any{"url": liveXPSURL, "groupID": group, "optionID": option, "targetID": target, "modalID": "modal-one", "needed": m.mode != "absent", "point": map[string]float64{"x": 50, "y": 40}}
	if hit && m.mode == "replaced" {
		p["modalID"] = "modal-two"
	}
	if m.mode == "purchase" {
		p["targetID"] = "AddToCart"
	}
	if m.mode == "foreign-option" {
		p["optionID"] = "CPU-A"
	}
	if m.mode == "foreign-url" {
		p["url"] = "https://example.com"
	}
	data, _ := json.Marshal(p)
	return string(data), nil
}

func TestOwnedConfirmationClicksOnlyBoundUnchangedModal(t *testing.T) {
	for _, action := range []string{"select", "ordinary", "custom"} {
		for _, mode := range []string{"valid", "absent", "covered", "replaced", "purchase", "foreign-option", "foreign-url"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				m := &confirmTransport{action: action, clickTransportModel: clickTransportModel{mode: mode}}
				r := nativeRequest{Action: action, URL: liveXPSURL, Family: true}
				if action == "select" {
					r.GroupID, r.OptionID, r.OptionName = "moduleCPU", "CPU-B", "CPU B"
				}
				clicked, err := ownedDOMConfirmation(context.Background(), m, r)
				actual := strings.Join(m.actions, ",")
				switch mode {
				case "valid":
					if err != nil || !clicked || actual != "mouseMoved,mousePressed,mouseReleased" || m.plans != 2 {
						t.Fatalf("confirmation not dispatched: %v %v %s", clicked, err, actual)
					}
				case "absent":
					if err != nil || clicked || actual != "" {
						t.Fatal("absent modal clicked")
					}
				default:
					if err == nil || clicked || strings.Contains(actual, "mousePressed") {
						t.Fatalf("unsafe modal clicked: %v %v %s", clicked, err, actual)
					}
				}
			})
		}
	}
}

func TestNativeMissingRegionsRetriesOnlySameWindow(t *testing.T) {
	for _, mode := range []string{"recovered", "persistent", "denied", "wrong-url", "wrong-handle", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, snapshots := 0, 0
			missing := nativePage{Handle: 99, URL: liveXPSURL, Error: "普通浏览器没有提供可核对的商品区域"}
			got, err := readNativeRegionsWithin(ctx, nativeRequest{Action: "open", URL: liveXPSURL, Family: true}, func(_ context.Context, r nativeRequest) (nativePage, error) {
				calls++
				if calls == 1 {
					if mode == "canceled" {
						cancel()
					}
					return missing, nil
				}
				if r.Handle != 99 || r.Action != "validate" && r.Action != "snapshot" {
					t.Fatal("reopened/refreshed unverified window")
				}
				if r.Action == "validate" {
					p := nativePage{Handle: 99, URL: liveXPSURL}
					if mode == "denied" {
						p.Error = "网页访问被限制（access denied）"
					}
					if mode == "wrong-url" {
						p.URL = "https://example.com"
					}
					if mode == "wrong-handle" {
						p.Handle = 88
					}
					return p, nil
				}
				snapshots++
				if mode == "recovered" && snapshots == 2 {
					return capturedNative(t, true), nil
				}
				return missing, nil
			}, time.Millisecond, 50*time.Millisecond)
			if mode == "recovered" {
				if err != nil || got.Error != "" || snapshots != 2 {
					t.Fatalf("did not recover: %+v %v", got, err)
				}
			} else if mode == "persistent" {
				var pending *nativeQuoteFailure
				if !errors.As(err, &pending) || !strings.Contains(err.Error(), "等待超时") || actionForFailure(liveXPSURL, normalReaderError(err).Error()) != "" {
					t.Fatal("persistent loading paused or accepted")
				}
			} else if mode == "denied" {
				if got.Error == "" || snapshots != 0 {
					t.Fatal("denial retried")
				}
			} else if err == nil || snapshots != 0 {
				t.Fatalf("unsafe read continued: %+v %v", got, err)
			}
		})
	}
}

func TestLoggedDellTransientFailuresKeepAutomaticSchedule(t *testing.T) {
	for _, reason := range []string{"普通浏览器自动采集失败：普通浏览器商品页面尚未加载", "普通浏览器自动采集失败：普通浏览器没有提供可核对的商品区域", "本轮扫描未完整完成；普通浏览器自动采集失败：配置入口未完成视图切换；核心选项切换未确认（处理器）；下轮自动重验"} {
		if actionForFailure(liveXPSURL, reason) != "" {
			t.Fatal("logged transient failure paused automatic monitoring")
		}
		for _, hard := range []string{"网页访问被限制（access denied）", "窗口或机型已变化", "回收未确认"} {
			if actionForFailure(liveXPSURL, reason+"；"+hard) == "" {
				t.Fatalf("hard failure softened: %s", hard)
			}
		}
	}
}

func TestPausedFailureTextDoesNotPromiseAutomaticRetry(t *testing.T) {
	detail := "核心切换未确认；下轮自动重验；下轮自动重试"
	if got := failureStatusText(detail, "自动检查已暂停"); strings.Contains(got, "下轮自动") {
		t.Fatal("paused product promises automatic retry")
	}
	if failureStatusText(detail, "") != detail {
		t.Fatal("retryable status changed")
	}
}

func TestViewSwitchWaitsForActualViewAndRetainsConfirmationRequest(t *testing.T) {
	for _, stale := range []bool{false, true} {
		b := newNormalBrowser()
		b.pollDelay = time.Millisecond
		reads := 0
		b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
			reads++
			if r.Action != "snapshot" || r.ConfirmAction != "ordinary" {
				t.Fatal("confirmation context lost during polling")
			}
			return capturedNative(t, stale || reads < 2), nil
		}
		_, err := b.stable(context.Background(), capturedNative(t, true), nativeRequest{Action: "ordinary", Handle: 99, URL: liveXPSURL, Family: true})
		if stale {
			var pending *nativeQuoteFailure
			if !errors.As(err, &pending) || !strings.Contains(err.Error(), "视图切换") {
				t.Fatal("unchanged custom view accepted as ordinary")
			}
		} else if err != nil || reads != 3 {
			t.Fatal(fmt.Sprintf("view switch not waited for: reads=%d err=%v", reads, err))
		}
	}
}
