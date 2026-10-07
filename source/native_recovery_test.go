package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func nativeTestFrame(token string, p nativePage) string {
	b, _ := json.Marshal(p)
	return nativeResultPrefix + token + ":" + base64.StdEncoding.EncodeToString(b) + "\n"
}
func nativeTestStage(token, stage string) string {
	return nativeStagePrefix + token + ":" + base64.StdEncoding.EncodeToString([]byte(stage)) + "\n"
}

func TestNativeChildHarness(t *testing.T) {
	mode := os.Getenv("PRICE_MONITOR_TEST_CHILD")
	if mode == "" {
		return
	}
	token := os.Getenv("PRICE_MONITOR_TEST_NONCE")
	fmt.Fprint(os.Stdout, nativeTestStage(token, "等待后台商品窗口"))
	fmt.Fprintln(os.Stderr, "test diagnostic retained")
	if mode == "wrong-token" {
		fmt.Fprint(os.Stdout, nativeTestFrame("different", nativePage{Handle: 7, URL: liveXPSURL}))
	}
	if mode == "ready-hold" || mode == "ready-exit" {
		fmt.Fprint(os.Stdout, nativeTestFrame(token, nativePage{Handle: 7, URL: liveXPSURL}))
	}
	if mode == "ready-exit" {
		os.Exit(0)
	}
	time.Sleep(60 * time.Second)
	os.Exit(0)
}
func TestNativeRealChildReturnsFrameWithoutEOF(t *testing.T) {
	for _, mode := range []string{"ready-hold", "ready-exit", "never", "wrong-token"} {
		t.Run(mode, func(t *testing.T) {
			token := "nonced-child"
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			outR, outW, e := os.Pipe()
			if e != nil {
				t.Fatal(e)
			}
			defer outR.Close()
			defer outW.Close()
			errR, errW, e := os.Pipe()
			if e != nil {
				t.Fatal(e)
			}
			defer errR.Close()
			defer errW.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestNativeChildHarness$")
			cmd.Env = append(os.Environ(), "PRICE_MONITOR_TEST_CHILD="+mode, "PRICE_MONITOR_TEST_NONCE="+token)
			cmd.Stdout = outW
			cmd.Stderr = errW
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			defer cmd.Process.Kill()
			stream := newNativeOutputStream(token)
			stderr := &nativeDiagnosticStream{}
			drained := make(chan struct{})
			copied := make(chan struct{}, 2)
			go func() { copyNativeStream(stream, outR, copied); close(drained) }()
			go copyNativeStream(stderr, errR, copied)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			start := time.Now()
			e = awaitNativeResult(ctx, cmd.Process.Kill, done, drained, stream)
			// Parent intentionally keeps both writer handles open. There is no EOF even
			// after the real child exits, mirroring an inherited writer held elsewhere.
			elapsed := time.Since(start)
			outR.Close()
			errR.Close()
			<-copied
			<-copied
			output, stage, complete := stream.snapshot()
			if mode == "ready-hold" || mode == "ready-exit" {
				if e != nil || !complete || elapsed >= 350*time.Millisecond {
					t.Fatalf("valid frame waited for exit/EOF: %v elapsed=%v stage=%s", e, elapsed, stage)
				}
				p, pe := decodeNativeOutput(output, token)
				if pe != nil || p.Handle != 7 {
					t.Fatalf("frame changed: %+v %v", p, pe)
				}
			} else if !errors.Is(e, context.DeadlineExceeded) || complete {
				t.Fatalf("invalid/no result accepted: complete=%v err=%v", complete, e)
			}
			if stage != "等待后台商品窗口" || !strings.Contains(string(stderr.snapshot()), "test diagnostic retained") {
				t.Fatal("timeout stage or stderr lost")
			}
		})
	}
}
func TestNativeStreamSplitFramesAndNonceValidation(t *testing.T) {
	s := newNativeOutputStream("mine")
	data := []byte(nativeTestStage("other", "wrong stage") + nativeTestStage("mine", "编译内置读取组件") + nativeTestFrame("other", nativePage{Handle: 8}) + nativeTestFrame("mine", nativePage{Handle: 9}))
	for _, b := range data {
		if _, e := s.Write([]byte{b}); e != nil {
			t.Fatal(e)
		}
	}
	output, stage, complete := s.snapshot()
	p, e := decodeNativeOutput(output, "mine")
	if e != nil || p.Handle != 9 || !complete || stage != "编译内置读取组件" {
		t.Fatalf("split frame/nonce: %+v %s %v %v", p, stage, complete, e)
	}
}

func TestNativeConfirmedTimeoutRecoveryDoesNotBlockOtherDell(t *testing.T) {
	b := newNormalBrowser()
	b.available = true
	b.pollDelay = time.Millisecond
	b.window = nativeWindow{Handle: 88, Owner: "previous", URL: liveXPSURL}
	n := 0
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		n++
		if n == 1 {
			return nativePage{ResetVerified: true, Error: "后台读取超时（最后阶段：等待后台商品窗口）；软件后台进程已确认回收；下轮自动重试"}, nil
		}
		if (r.Action == "open" && r.Handle != 0) || (r.Action == "snapshot" && r.Handle != 90) || (r.Action != "open" && r.Action != "snapshot") {
			t.Fatalf("reused a killed window: %+v", r)
		}
		p := windowTestPage(t, r.URL)
		p.Handle = 90
		return p, nil
	}
	p := product("first")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	_, e := b.collect(context.Background(), *p, func(string) {})
	var recovered *nativeRecoveredFailure
	if !errors.As(e, &recovered) || b.uncertain || b.window.Handle != 0 {
		t.Fatalf("reset state wrong: %v %+v", e, b.window)
	}
	if actionForFailure(p.URL, normalReaderError(e).Error()) != "" {
		t.Fatal("confirmed cleanup unnecessarily paused monitoring")
	}
	p = product("other")
	p.URL = "https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250/pc16250_fixed_22"
	p.DellFamilyScan = true
	o, e := b.collect(context.Background(), *p, func(string) {})
	if e != nil || len(o.Results) == 0 {
		t.Fatalf("other Dell blocked by recovered timeout: %+v %v", o, e)
	}
}
func TestNativeUnconfirmedRecoveryKeepsFirstCauseAndBlocksNewWindow(t *testing.T) {
	b := newNormalBrowser()
	b.uncertain = true
	b.uncertainReason = "后台读取超时（最后阶段：读取后台窗口地址栏）"
	b.reset = func(context.Context) error { return errors.New("active processes still running") }
	calls := 0
	b.call = func(context.Context, nativeRequest) (nativePage, error) { calls++; return nativePage{}, nil }
	p := product("retry")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	_, e := b.collect(context.Background(), *p, func(string) {})
	if e == nil || !strings.Contains(e.Error(), "读取后台窗口地址栏") || !strings.Contains(e.Error(), "active processes") || calls != 0 || !b.uncertain {
		t.Fatalf("unconfirmed cleanup lost cause or reopened: %v calls=%d", e, calls)
	}
	if actionForFailure(p.URL, normalReaderError(e).Error()) == "" {
		t.Fatal("unconfirmed cleanup allowed automatic retries")
	}
}
func TestNativeUncertainLaunchRecoversOnlyAfterVerifiedOwnedJobEmpty(t *testing.T) {
	b := newNormalBrowser()
	b.uncertain = true
	b.uncertainReason = "first timeout"
	b.window = nativeWindow{Owner: "old", URL: liveXPSURL}
	cleaned := false
	b.reset = func(context.Context) error { cleaned = true; return nil }
	b.call = func(_ context.Context, r nativeRequest) (nativePage, error) {
		if !cleaned || (r.Action == "open" && r.Handle != 0) || (r.Action == "snapshot" && r.Handle != 99) || (r.Action != "open" && r.Action != "snapshot") {
			t.Fatal("open before verified cleanup")
		}
		p := windowTestPage(t, r.URL)
		p.Handle = 99
		return p, nil
	}
	p := product("new")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	if _, e := b.collect(context.Background(), *p, func(string) {}); e != nil {
		t.Fatal(e)
	}
	if b.uncertain || b.uncertainReason != "" {
		t.Fatal("stale cause remains after verified reset")
	}
}

func TestNativeRecoveredTimeoutKeepsOldOffersStaleWithoutPausing(t *testing.T) {
	a := testApp(t)
	a.normal.available = true
	p := product("timeout")
	p.URL = liveXPSURL
	p.DellFamilyScan = true
	p.LastPrice = 1000
	p.LastTrusted = true
	p.TargetPrice = 5000
	p.DellResults = []DellResult{{OfferID: "old", GPU: "RTX Pro", Price: 1000, Confirmed: true, Matched: true, Stock: stockIn}}
	a.store.Products = append(a.store.Products, p)
	a.normal.call = func(context.Context, nativeRequest) (nativePage, error) {
		return nativePage{ResetVerified: true, Error: "后台读取超时（最后阶段：等待后台商品窗口）；软件后台进程已确认回收；下轮自动重试"}, nil
	}
	a.schedule(p.ID)
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if p.NeedsAction != "" || !p.Stale || p.LastTrusted || p.LastPrice != 1000 || p.DellResults[0].GPU != "RTX Pro" || !p.DellResults[0].Stale || p.DellResults[0].Matched || len(a.alerts) != 0 || !strings.Contains(p.LastError, "等待后台商品窗口") {
		t.Fatalf("recovered timeout accepted stale offer or paused: %+v alerts=%v", p, a.alerts)
	}
}
