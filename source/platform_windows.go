//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"
)

func hideCommand(cmd *exec.Cmd, hide bool) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: hide} }
func killOwned(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		c := exec.Command("taskkill.exe", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		hideCommand(c, true)
		_ = c.Run()
	}
}
func openURL(raw string) {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", raw)
	hideCommand(cmd, true)
	_ = cmd.Start()
}
func showError(s string) { showMessage("价格与库存监控器", s, 0x10) }
func showMessage(title, message string, flags uintptr) {
	u := syscall.NewLazyDLL("user32.dll")
	fn := u.NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(message)
	_, _, _ = fn.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
}
func desktopAlert(title, message string) { showMessage(title, message, 0x40000|0x10000|0x40) }
