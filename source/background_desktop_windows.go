//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var collectorDesktop struct {
	sync.Mutex
	handle  uintptr
	job     uintptr
	name    string
	stopped bool
}
var collectorUser32 = syscall.NewLazyDLL("user32.dll")
var collectorKernel32 = syscall.NewLazyDLL("kernel32.dll")

func currentCollectorThreadID() uintptr {
	id, _, _ := collectorKernel32.NewProc("GetCurrentThreadId").Call()
	return id
}

type collectorJobLimits struct {
	ProcessTime, JobTime                                       int64
	Flags                                                      uint32
	MinimumWorkingSet, MaximumWorkingSet                       uintptr
	ActiveProcessLimit                                         uint32
	Affinity                                                   uintptr
	Priority, Scheduling                                       uint32
	IO                                                         [6]uint64
	ProcessMemory, JobMemory, PeakProcessMemory, PeakJobMemory uintptr
}

// A Win32 desktop is not shown unless explicitly switched to. No collector code
// switches desktops, focuses the user's window, or injects input into it.
func backgroundDesktop() (string, error) {
	collectorDesktop.Lock()
	defer collectorDesktop.Unlock()
	if collectorDesktop.stopped {
		return "", fmt.Errorf("后台采集已停止")
	}
	if collectorDesktop.handle != 0 {
		return collectorDesktop.name, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	old, _, _ := collectorUser32.NewProc("GetThreadDesktop").Call(currentCollectorThreadID())
	name := "PriceStockMonitor_" + newID()
	ptr, _ := syscall.UTF16PtrFromString(name)
	// READ/WRITEOBJECTS, CREATEWINDOW/MENU, ENUMERATE etc.; no SWITCHDESKTOP right.
	h, _, e := collectorUser32.NewProc("CreateDesktopW").Call(uintptr(unsafe.Pointer(ptr)), 0, 0, 0, 0xff, 0)
	if h == 0 {
		return "", fmt.Errorf("无法创建后台采集桌面：%v；没有打开前台浏览器", e)
	}
	if old != 0 {
		collectorUser32.NewProc("SetThreadDesktop").Call(old)
	}
	jobName, _ := syscall.UTF16PtrFromString(name + "_Job")
	job, _, e := collectorKernel32.NewProc("CreateJobObjectW").Call(0, uintptr(unsafe.Pointer(jobName)))
	runtime.KeepAlive(jobName)
	if job == 0 {
		collectorUser32.NewProc("CloseDesktop").Call(h)
		return "", fmt.Errorf("后台进程回收组创建失败：%v", e)
	}
	if e == syscall.ERROR_ALREADY_EXISTS {
		syscall.CloseHandle(syscall.Handle(job))
		collectorUser32.NewProc("CloseDesktop").Call(h)
		return "", fmt.Errorf("后台进程组名称冲突；没有使用其他进程组")
	}
	limits := collectorJobLimits{Flags: 0x2000} // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	ok, _, e := collectorKernel32.NewProc("SetInformationJobObject").Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if ok == 0 {
		syscall.CloseHandle(syscall.Handle(job))
		collectorUser32.NewProc("CloseDesktop").Call(h)
		return "", fmt.Errorf("后台进程回收组设置失败：%v", e)
	}
	collectorDesktop.handle, collectorDesktop.name, collectorDesktop.job = h, name, job
	return name, nil
}
func closeNativeDesktop() {
	collectorDesktop.Lock()
	defer collectorDesktop.Unlock()
	collectorDesktop.stopped = true
	if collectorDesktop.job != 0 {
		collectorKernel32.NewProc("TerminateJobObject").Call(collectorDesktop.job, 1)
		syscall.CloseHandle(syscall.Handle(collectorDesktop.job))
		collectorDesktop.job = 0
	}
	if collectorDesktop.handle != 0 {
		collectorUser32.NewProc("CloseDesktop").Call(collectorDesktop.handle)
		collectorDesktop.handle = 0
	}
}

type collectorStartup struct {
	syscall.StartupInfo
	attributes unsafe.Pointer
}

// Launch the reader on the SAME private desktop as its browser. Launching first
// and hiding afterwards would allow a visible flash, so there is no such path.
func runDesktopCommand(ctx context.Context, exe string, args, env []string, input, token string) ([]byte, []byte, error) {
	desktop, err := backgroundDesktop()
	if err != nil {
		return nil, nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, nil, err
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer inR.Close()
	defer inW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer outR.Close()
	defer outW.Close()
	errR, errW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	defer errR.Close()
	defer errW.Close()
	process, _ := syscall.GetCurrentProcess()
	handles := make([]syscall.Handle, 3)
	for i, f := range []*os.File{inR, outW, errW} {
		if err = syscall.DuplicateHandle(process, syscall.Handle(f.Fd()), process, &handles[i], 0, true, syscall.DUPLICATE_SAME_ACCESS); err != nil {
			for _, h := range handles {
				if h != 0 {
					syscall.CloseHandle(h)
				}
			}
			return nil, nil, err
		}
	}
	defer func() {
		for _, h := range handles {
			if h != 0 {
				syscall.CloseHandle(h)
			}
		}
	}()
	var size uintptr
	init := collectorKernel32.NewProc("InitializeProcThreadAttributeList")
	init.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return nil, nil, fmt.Errorf("后台采集启动属性不可用；没有打开前台浏览器")
	}
	list := make([]byte, size)
	ok, _, e := init.Call(uintptr(unsafe.Pointer(&list[0])), 1, 0, uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return nil, nil, e
	}
	defer func() {
		collectorKernel32.NewProc("DeleteProcThreadAttributeList").Call(uintptr(unsafe.Pointer(&list[0])))
		runtime.KeepAlive(list)
	}()
	// PROC_THREAD_ATTRIBUTE_HANDLE_LIST: only these three pipe handles are inherited.
	ok, _, e = collectorKernel32.NewProc("UpdateProcThreadAttribute").Call(uintptr(unsafe.Pointer(&list[0])), 0, 0x20002, uintptr(unsafe.Pointer(&handles[0])), uintptr(len(handles))*unsafe.Sizeof(handles[0]), 0, 0)
	if ok == 0 {
		return nil, nil, e
	}
	dp, _ := syscall.UTF16PtrFromString(desktop)
	si := collectorStartup{StartupInfo: syscall.StartupInfo{Cb: uint32(unsafe.Sizeof(collectorStartup{})), Desktop: dp, Flags: syscall.STARTF_USESTDHANDLES | syscall.STARTF_USESHOWWINDOW, StdInput: handles[0], StdOutput: handles[1], StdErr: handles[2]}, attributes: unsafe.Pointer(&list[0])}
	app, _ := syscall.UTF16PtrFromString(exe)
	words := []string{syscall.EscapeArg(exe)}
	for _, arg := range args {
		words = append(words, syscall.EscapeArg(arg))
	}
	line, _ := syscall.UTF16PtrFromString(strings.Join(words, " "))
	env = append(env, "PRICE_MONITOR_PRIVATE_DESKTOP="+desktop, "PRICE_MONITOR_PRIVATE_JOB="+desktop+"_Job")
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	var pi syscall.ProcessInformation
	// EXTENDED_STARTUPINFO_PRESENT | CREATE_UNICODE_ENVIRONMENT | CREATE_NO_WINDOW.
	err = syscall.CreateProcess(app, line, nil, nil, true, 0x80000|0x400|0x8000000|0x4, &block[0], nil, &si.StartupInfo, &pi)
	runtime.KeepAlive(list)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(block)
	if err != nil {
		return nil, nil, fmt.Errorf("后台采集进程启动失败：%w；没有打开前台浏览器", err)
	}
	collectorDesktop.Lock()
	job := collectorDesktop.job
	ok, _, assignErr := collectorKernel32.NewProc("AssignProcessToJobObject").Call(job, uintptr(pi.Process))
	collectorDesktop.Unlock()
	if ok == 0 {
		syscall.TerminateProcess(pi.Process, 1)
		syscall.CloseHandle(pi.Thread)
		syscall.CloseHandle(pi.Process)
		return nil, nil, fmt.Errorf("后台进程回收组绑定失败：%v", assignErr)
	}
	resumed, _, resumeErr := collectorKernel32.NewProc("ResumeThread").Call(uintptr(pi.Thread))
	if resumed == 0xffffffff {
		syscall.TerminateProcess(pi.Process, 1)
		syscall.CloseHandle(pi.Thread)
		syscall.CloseHandle(pi.Process)
		return nil, nil, resumeErr
	}
	syscall.CloseHandle(pi.Thread)
	syscall.CloseHandle(pi.Process)
	for i, h := range handles {
		syscall.CloseHandle(h)
		handles[i] = 0
	}
	inR.Close()
	outW.Close()
	errW.Close()
	child, err := os.FindProcess(int(pi.ProcessId))
	if err != nil {
		return nil, nil, err
	}
	out := newNativeOutputStream(token)
	stderr := &nativeDiagnosticStream{}
	copied := make(chan struct{}, 2)
	outDrained := make(chan struct{})
	go func() { _, _ = io.Copy(out, outR); close(outDrained); copied <- struct{}{} }()
	go func() { copyNativeStream(stderr, errR, copied) }()
	go func() { _, _ = io.WriteString(inW, input); _ = inW.Close() }()
	done := make(chan error, 1)
	go func() {
		state, e := child.Wait()
		if e == nil && !state.Success() {
			e = fmt.Errorf("采集组件退出：%s", state.String())
		}
		done <- e
	}()
	err = awaitNativeResult(ctx, child.Kill, done, outDrained, out)
	// A valid frame is complete even when a descendant retains a writer. Close
	// only our pipe readers; the browser belongs to the collector's private job.
	_ = outR.Close()
	_ = errR.Close()
	_ = inW.Close()
	for i := 0; i < 2; i++ {
		<-copied
	}
	output, stage, _ := out.snapshot()
	if err != nil {
		err = fmt.Errorf("%w（最后阶段：%s）", err, stage)
	}
	return output, stderr.snapshot(), err
}

// Assert native x64 layouts at compilation, before passing them to Win32.
var _ [144 - unsafe.Sizeof(collectorJobLimits{})]byte
var _ [unsafe.Sizeof(collectorJobLimits{}) - 144]byte
var _ [112 - unsafe.Sizeof(collectorStartup{})]byte
var _ [unsafe.Sizeof(collectorStartup{}) - 112]byte

type collectorJobAccounting struct {
	Times                             [4]int64
	Faults, Total, Active, Terminated uint32
}

var _ [48 - unsafe.Sizeof(collectorJobAccounting{})]byte
var _ [unsafe.Sizeof(collectorJobAccounting{}) - 48]byte

func resetNativeCollector(ctx context.Context) error {
	collectorDesktop.Lock()
	defer collectorDesktop.Unlock()
	if collectorDesktop.stopped || collectorDesktop.job == 0 {
		return fmt.Errorf("软件后台进程组尚未建立，无法确认回收")
	}
	job := collectorDesktop.job
	ok, _, e := collectorKernel32.NewProc("TerminateJobObject").Call(job, 1)
	if ok == 0 {
		return fmt.Errorf("软件后台进程回收失败：%v", e)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var accounting collectorJobAccounting
		ok, _, e = collectorKernel32.NewProc("QueryInformationJobObject").Call(job, 1, uintptr(unsafe.Pointer(&accounting)), unsafe.Sizeof(accounting), 0)
		if ok == 0 {
			return fmt.Errorf("软件后台进程回收状态核对失败：%v", e)
		}
		if accounting.Active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("软件后台进程仍未全部退出：%w", ctx.Err())
		case <-ticker.C:
		}
	}
}
