//go:build !windows

package main

import (
	"context"
	"errors"
)

func nativeReaderAvailable() bool     { return false }
func normalBrowserExecutable() string { return "" }
func runNativeReader(context.Context, nativeRequest) (nativePage, error) {
	return nativePage{}, errors.New("普通浏览器界面读取仅在 Windows 中可用")
}

func closeNativeDesktop() {}

func resetNativeCollector(context.Context) error {
	return errors.New("Windows 后台进程回收不可用")
}
