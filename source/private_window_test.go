package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// This is a source guard, not a native Windows test. It checks that removing the
// incorrect TabItem-wide rejection does not remove the window ownership boundary.
func TestNativePrivateWindowOwnershipAndDocumentBoundaryContract(t *testing.T) {
	read := func(name string) string {
		b, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	ps := read("normal_reader.ps1")
	function := func(signature, end string) string {
		start := strings.Index(ps, signature)
		if start < 0 {
			t.Fatalf("missing %s", signature)
		}
		rest := ps[start:]
		n := strings.Index(rest, end)
		if n < 0 {
			t.Fatalf("missing function end %s", end)
		}
		return rest[:n]
	}
	auth := function("private static void PrivateWindow(", "private class MSChoice")
	for _, required := range []string{"PrivateDesktop();", "GetProp(h,tag)", "jobName!=desktop+\"_Job\"", "OpenJobObject(4,false,jobName)", "OpenProcess(0x1000,false,pid)", "IsProcessInJob(process,job,out inside)", "!inside", "finally", "CloseHandle(process)", "CloseHandle(job)"} {
		if !strings.Contains(auth, required) {
			t.Fatalf("missing ownership requirement %s", required)
		}
	}
	closeFn := function("private static void CloseTagged(", "private static void CleanupOldWindows")
	if strings.Index(closeFn, "PrivateWindow(h,tag);") < 0 || strings.Index(closeFn, "PrivateWindow(h,tag);") > strings.Index(closeFn, "PostMessage(h,") {
		t.Fatal("closing a window before exact ownership verification")
	}
	validate := function("private static AutomationElement Validate(", "private static AutomationElement Document")
	if !strings.Contains(validate, "PrivateWindow(h,ownerKey);") || !strings.Contains(validate, "SameURL(expected,Address(root),family)") {
		t.Fatal("reading without private job and URL boundary")
	}
	document := function("private static AutomationElement Document(", "private static void CheckBlocked")
	for _, required := range []string{"ControlType.Document", "if(document!=null)throw", "document=e;continue;", "count>1500", "Budget();"} {
		if !strings.Contains(document, required) {
			t.Fatalf("unbounded or ambiguous document read: %s", required)
		}
	}
	for _, regression := range []string{"tabs>1", "ControlType.TabItem)tabs++", "监控商品窗口中出现其他标签页", "采集窗口有其他标签页"} {
		if strings.Contains(ps, regression) {
			t.Fatalf("blanket browser tab rejection restored: %s", regression)
		}
	}
	launcher := read("background_desktop_windows.go")
	for _, required := range []string{"CreateJobObjectW\").Call(0, uintptr(unsafe.Pointer(jobName)))", "PRICE_MONITOR_PRIVATE_JOB=", "syscall.ERROR_ALREADY_EXISTS", "AssignProcessToJobObject", "JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE"} {
		if !strings.Contains(launcher, required) {
			t.Fatalf("launcher lost private job requirement %s", required)
		}
	}
}

func TestNativeCloseReclaimsPrivateDescendantsOnlyAfterConfirmedWindowClose(t *testing.T) {
	for _, tc := range []struct {
		name, action                       string
		page                               nativePage
		resetErr                           error
		wantReset, wantVerified, wantError bool
	}{
		{name: "closed", action: "close", wantReset: true, wantVerified: true},
		{name: "cleanup", action: "cleanup", wantReset: true, wantVerified: true},
		{name: "failed-close", action: "close", page: nativePage{Handle: 99, Error: "窗口归属未确认"}},
		{name: "still-open", action: "close", page: nativePage{Handle: 99}},
		{name: "read", action: "snapshot"},
		{name: "unconfirmed-process-exit", action: "close", resetErr: errors.New("still active"), wantReset: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			p, e := finalizeNativeClose(context.Background(), nativeRequest{Action: tc.action}, tc.page, func(context.Context) error { called = true; return tc.resetErr })
			if called != tc.wantReset || p.ResetVerified != tc.wantVerified || (e != nil) != tc.wantError {
				t.Fatalf("wrong cleanup boundary: called=%v page=%+v err=%v", called, p, e)
			}
		})
	}
}
