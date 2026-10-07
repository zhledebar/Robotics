//go:build !windows

package main

import (
	"log"
	"os/exec"
)

func hideCommand(cmd *exec.Cmd, hide bool) {}
func killOwned(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func openURL(raw string)                 { _ = exec.Command("xdg-open", raw).Start() }
func showError(s string)                 { log.Print(s) }
func desktopAlert(title, message string) { log.Print(title, " ", message) }
