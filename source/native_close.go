package main

import (
	"context"
	"fmt"
	"time"
)

// A closed owned window is not proof that every browser descendant has exited.
// Reclaim the private job before returning the shared slot to another product.
func finalizeNativeClose(ctx context.Context, r nativeRequest, page nativePage, reset func(context.Context) error) (nativePage, error) {
	if (r.Action != "close" && r.Action != "cleanup") || page.Error != "" || page.Handle != 0 {
		return page, nil
	}
	cleanCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := reset(cleanCtx); err != nil {
		return page, fmt.Errorf("后台窗口关闭后进程回收未确认：%w", err)
	}
	page.ResetVerified = true
	return page, nil
}
