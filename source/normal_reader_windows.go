//go:build windows

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed normal_reader.ps1
var nativeScript string

func nativeReaderAvailable() bool { return true }
func normalBrowserExecutable() string {
	// Honor the Windows HTTPS browser choice when it is Chrome or Edge. No
	// profile files/cookies are read, copied, or modified by the application.
	preference := "chrome"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "reg.exe", "query", `HKCU\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice`, "/v", "ProgId")
	hideCommand(cmd, true)
	if b, e := cmd.Output(); e == nil && strings.Contains(strings.ToLower(string(b)), "msedge") {
		preference = "msedge"
	}
	names := []string{`Google/Chrome/Application/chrome.exe`, `Microsoft/Edge/Application/msedge.exe`}
	if preference == "msedge" {
		names[0], names[1] = names[1], names[0]
	}
	for _, name := range names {
		for _, root := range []string{os.Getenv("LOCALAPPDATA"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if root != "" {
				p := filepath.Join(root, filepath.FromSlash(name))
				if _, e := os.Stat(p); e == nil {
					return p
				}
			}
		}
	}
	return ""
}
func runUIAReader(ctx context.Context, r nativeRequest) (nativePage, error) {
	started := time.Now()
	defer func() {
		log.Printf("native reader elapsed: action=%s model=%s duration=%s", r.Action, dellPlatform(r.URL), time.Since(started).Round(time.Millisecond))
	}()
	child, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	path := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	// Static embedded code via stdin: no script installation, no execution-policy
	// change, no third-party executable download.
	args := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-MTA", "-Command", "[Console]::InputEncoding = New-Object System.Text.UTF8Encoding($false); [Console]::WriteLine('PSM_UIA_STAGE:'+$env:PRICE_MONITOR_UIA_TOKEN+':6K+75Y+W5YaF572u6YeH6ZuG6ISa5pys'); & ([scriptblock]::Create([Console]::In.ReadToEnd()))"}
	request, _ := json.Marshal(r)
	token := newID()
	profile := ownedDOMProfile(r)
	if err := os.MkdirAll(profile, 0700); err != nil {
		return nativePage{}, fmt.Errorf("后台浏览器目录创建失败：%w", err)
	}
	assembly := filepath.Join(profile, "reader.dll")
	cacheReady := "0"
	if readerAssemblyReady(assembly) {
		cacheReady = "1"
	} else {
		// Never load an unrecognized or modified cached assembly.
		if err := os.Remove(assembly); err != nil && !os.IsNotExist(err) {
			return nativePage{}, fmt.Errorf("后台读取组件缓存无法更新：%w", err)
		}
	}
	if r.Action == "open" {
		_ = os.Remove(filepath.Join(profile, "DevToolsActivePort"))
	}
	env := append(os.Environ(), "PRICE_MONITOR_UIA_REQUEST="+string(request), "PRICE_MONITOR_UIA_TOKEN="+token, "PRICE_MONITOR_BROWSER_PROFILE="+profile, "PRICE_MONITOR_READER_ASSEMBLY="+assembly, "PRICE_MONITOR_READER_CACHE_READY="+cacheReady)
	output, stderr, e := runDesktopCommand(child, path, args, env, nativeScript, token)
	// Always retain partial stdout/stderr before classifying timeout. V8.7 lost
	// the only evidence of the stage that stalled here.
	if e != nil || child.Err() != nil {
		log.Printf("native reader incomplete: action=%s model=%s error=%v last-output=%q stderr=%q", r.Action, dellPlatform(r.URL), e, nativeDiagnosticTail(output), nativeDiagnosticTail(stderr))
	}
	if ctx.Err() != nil {
		if _, frameErr := decodeNativeOutput(output, token); e != nil || frameErr != nil {
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 6*time.Second)
			cleanErr := resetNativeCollector(cleanCtx)
			cleanCancel()
			if cleanErr == nil {
				return nativePage{ResetVerified: true}, ctx.Err()
			}
			log.Printf("canceled native reader cleanup not confirmed: %v", cleanErr)
		}
		return nativePage{}, ctx.Err()
	}
	if errors.Is(e, context.DeadlineExceeded) || errors.Is(child.Err(), context.DeadlineExceeded) {
		// A complete nonce-validated frame remains usable without process EOF.
		if page, parseErr := decodeNativeOutput(output, token); e == nil && parseErr == nil {
			return finalizeNativeClose(ctx, r, page, resetNativeCollector)
		}
		stage := nativeLastStage(output, token)
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cleanCancel()
		if cleanErr := resetNativeCollector(cleanCtx); cleanErr == nil {
			log.Printf("native reader timeout recovered: action=%s model=%s stage=%s private job active-processes=0", r.Action, dellPlatform(r.URL), stage)
			return nativePage{ResetVerified: true, Error: "后台读取超时（最后阶段：" + stage + "）；软件后台进程已确认回收；下轮自动重试"}, nil
		} else {
			log.Printf("native reader timeout cleanup failed: %v", cleanErr)
			return nativePage{}, fmt.Errorf("后台读取超时（最后阶段：%s）；后台回收未确认：%v", stage, cleanErr)
		}
	}
	if e != nil {
		log.Printf("Windows reader process failed: %v; stderr: %s", e, strings.TrimSpace(string(stderr)))
		return nativePage{}, fmt.Errorf("Windows 界面读取组件运行失败；详细原因已记入程序日志")
	}
	page, e := decodeNativeOutput(output, token)
	if e != nil {
		detail := output
		if len(detail) > 16384 {
			detail = detail[:16384]
		}
		log.Printf("Windows reader invalid result: %v; stdout prefix: %q; stderr: %s", e, detail, strings.TrimSpace(string(stderr)))
		return page, fmt.Errorf("Windows 界面读取组件返回异常；详细原因已记入程序日志")
	}
	if !strings.Contains(page.Error, "Windows 界面读取组件") {
		rememberReaderAssembly(assembly)
	}
	if page.Diagnostic != "" {
		log.Printf("Windows reader diagnostic: %s", page.Diagnostic)
	}
	page, e = finalizeNativeClose(ctx, r, page, resetNativeCollector)
	if e != nil {
		log.Printf("native close cleanup not confirmed: action=%s error=%v", r.Action, e)
	}
	return page, e
}
