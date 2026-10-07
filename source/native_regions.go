package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

const nativeRegionWaitBudget = 45 * time.Second

func nativeContentPending(p nativePage) bool {
	return p.Error == "普通浏览器没有提供可核对的商品区域" || p.Error == "普通浏览器商品页面尚未加载"
}

func nativeRegionIdentity(p nativePage, r nativeRequest) bool {
	return !p.ResetVerified && p.Handle > 0 && (r.Handle <= 0 || p.Handle == r.Handle) && normalURLMatches(r.URL, p.URL, r.Family)
}

// Missing regions are retried as reads of the same validated owned window.
// Never reopen/refresh on a capture failure or relax access/ownership checks.
func readNativeRegions(ctx context.Context, r nativeRequest, call nativeCaller, delay time.Duration) (nativePage, error) {
	return readNativeRegionsWithin(ctx, r, call, delay, nativeRegionWaitBudget)
}

func readNativeRegionsWithin(ctx context.Context, r nativeRequest, call nativeCaller, delay, budget time.Duration) (nativePage, error) {
	reader := call
	call = func(readCtx context.Context, req nativeRequest) (nativePage, error) {
		page, err := reader(readCtx, req)
		if err == nil && nativeContentPending(page) {
			if titleErr := normalPageError(nativePage{Title: page.Title}); titleErr != nil {
				page.Error = titleErr.Error()
			}
		}
		return page, err
	}
	p, err := call(ctx, r)
	if err != nil || !nativeContentPending(p) || p.ResetVerified {
		return p, err
	}
	if !nativeRegionIdentity(p, r) {
		return p, fmt.Errorf("等待商品内容前窗口或网址未核对；没有继续读取其他页面")
	}
	// A missing document before an operation means that operation did not run.
	// Read-only recovery must never silently turn it into a successful action.
	if p.Error == "普通浏览器商品页面尚未加载" && r.Action != "open" && r.Action != "snapshot" && r.Action != "validate" {
		return p, &nativeQuoteFailure{Reason: "浏览器页面暂未就绪：" + p.Error + "；未执行本次配置或刷新操作"}
	}
	started := time.Now()
	log.Printf("native product content pending: action=%s model=%s handle=%d reason=%s; waiting up to %s in the same owned window", r.Action, dellPlatform(r.URL), p.Handle, p.Error, budget)
	waitCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	firstReason := p.Error
	waitFailure := func(err error) (nativePage, error) {
		if ctx.Err() != nil {
			return p, ctx.Err()
		}
		if waitCtx.Err() != nil {
			log.Printf("native product content wait expired: model=%s handle=%d elapsed=%s reason=%s", dellPlatform(r.URL), p.Handle, time.Since(started).Round(time.Millisecond), firstReason)
			return p, &nativeQuoteFailure{Reason: "浏览器页面暂未就绪：" + firstReason + "；同一窗口等待超时，本轮未接受报价"}
		}
		return p, err
	}
	r.Handle = p.Handle
	// A read count is not a time budget: eight Windows read pairs took only
	// about 26 seconds in V8.22. Continue until ready or the actual deadline.
	for i := 0; ; i++ {
		if err = waitCtx.Err(); err != nil {
			return waitFailure(err)
		}
		validate := r
		validate.Action = "validate"
		validated, validationErr := call(waitCtx, validate)
		if validationErr != nil {
			if validated.ResetVerified {
				p = validated
			}
			return waitFailure(validationErr)
		}
		if (validated.Error != "" && !nativeContentPending(validated)) || validated.ResetVerified {
			return validated, nil
		}
		if !nativeRegionIdentity(validated, r) {
			return p, fmt.Errorf("普通浏览器窗口或机型已变化；没有继续等待商品区域")
		}
		if err = pauseContext(waitCtx, delay); err != nil {
			return waitFailure(err)
		}
		r.Action = "snapshot"
		p, err = call(waitCtx, r)
		if err != nil {
			return waitFailure(err)
		}
		if err = waitCtx.Err(); err != nil {
			return waitFailure(err)
		}
		if p.ResetVerified || (p.Error != "" && !nativeContentPending(p)) {
			return p, nil
		}
		if !nativeRegionIdentity(p, r) {
			return p, fmt.Errorf("普通浏览器窗口或机型已变化；没有继续等待商品内容")
		}
		if p.Error == "" {
			log.Printf("native product content ready: model=%s handle=%d snapshots=%d", dellPlatform(r.URL), p.Handle, i+1)
			return p, nil
		}
		log.Printf("native product content still pending: model=%s handle=%d snapshot=%d elapsed=%s reason=%s", dellPlatform(r.URL), p.Handle, i+1, time.Since(started).Round(time.Millisecond), p.Error)
	}
}
