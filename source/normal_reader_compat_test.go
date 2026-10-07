package main

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
)

// This is a managed-API source contract, not compilation or execution of C#.
// Microsoft documents CachedChildren as a .NET property (UIAutomationClient):
// https://learn.microsoft.com/en-us/dotnet/api/system.windows.automation.automationelement.cachedchildren
// Native COM IUIAutomation methods must not be pasted into this managed helper.
func managedReaderViolations(src string) []string {
	var bad []string
	for _, name := range []string{"LegacyIAccessiblePattern", "GetCachedChildren"} {
		if strings.Contains(src, name) {
			bad = append(bad, name)
		}
	}
	supported := map[string]bool{"ValuePattern": true, "InvokePattern": true, "TogglePattern": true, "SelectionItemPattern": true, "TextPattern": true, "ExpandCollapsePattern": true}
	for _, match := range regexp.MustCompile(`\b([A-Z][A-Za-z]*Pattern)\s*\.\s*Pattern\b`).FindAllStringSubmatch(src, -1) {
		name := match[1]
		if !supported[name] {
			bad = append(bad, name)
		}
	}
	return bad
}

func TestNativeReaderManagedAPIContract(t *testing.T) {
	b, err := os.ReadFile("normal_reader.ps1")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if bad := managedReaderViolations(src); len(bad) != 0 {
		t.Fatalf("unsupported managed API names: %v", bad)
	}
	for _, required := range []string{"e.CachedChildren", "region.GetUpdatedCache(cache)", "$stage = '运行'", "diagnostic=$_.Exception.Message", "pi.RedirectStandardOutput=true", "pi.RedirectStandardError=true", "browserProcess.BeginOutputReadLine()", "browserProcess.BeginErrorReadLine()", "PSM_UIA_RESULT:", "PRICE_MONITOR_UIA_TOKEN", "CleanupOldWindows(false);", "error.handle=h.ToInt64()", "currentOwnerOnly&&tag!=ownerKey", "CleanupOldWindows(true);"} {
		if !strings.Contains(src, required) {
			t.Fatalf("missing source contract %q", required)
		}
	}
	cleanupAt := strings.Index(src, "    CleanupOldWindows(false);")
	launchAt := strings.Index(src, "Process browserProcess=Process.Start(pi)")
	if cleanupAt < 0 || cleanupAt > launchAt {
		t.Fatal("browser launched before reclaiming tagged previous-version windows")
	}
	for _, regression := range []string{"LegacyIAccessiblePattern.Pattern", "e.GetCachedChildren()", "UnknownPattern.Pattern"} {
		if len(managedReaderViolations(src+regression)) == 0 {
			t.Fatalf("checker did not reject regression %q", regression)
		}
	}
}

func TestNativeReaderDiagnosticsDoNotFillUIOrTriggerAlerts(t *testing.T) {
	a := testApp(t)
	p := product("compiler-error")
	p.URL = liveXPSURL
	p.LastPrice = 1000
	p.LastTrusted = true
	p.TargetPrice = 3000
	p.DellResults = []DellResult{{OfferID: "old", Price: 1000, Confirmed: true, Matched: true, Stock: stockIn}}
	a.store.Products = append(a.store.Products, p)
	a.normal.available = true
	calls := 0
	a.normal.call = func(context.Context, nativeRequest) (nativePage, error) {
		calls++
		return nativePage{Error: "Windows 界面读取组件编译失败；详细原因已记入程序日志", Diagnostic: strings.Repeat("CSharp_SOURCE_INTERNAL_DETAIL ", 2000)}, nil
	}
	a.schedule(p.ID)
	waitChecked(t, a, p.ID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if calls != 1 || !p.Stale || p.LastTrusted || p.LastPrice != 1000 || p.DellResults[0].Matched || len(a.alerts) != 0 {
		t.Fatalf("compile failure accepted old data: %+v calls=%d", p, calls)
	}
	if p.NeedsAction == "" || !strings.Contains(p.LastError, "组件编译失败") || len(p.LastError)+len(p.NeedsAction) > 500 || strings.Contains(p.LastError+p.NeedsAction, "CSharp_SOURCE_INTERNAL_DETAIL") {
		t.Fatalf("incorrect UI diagnostics: %q / %q", p.LastError, p.NeedsAction)
	}
}
