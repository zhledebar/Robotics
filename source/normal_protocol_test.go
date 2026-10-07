package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func framedNativeResult(token, body string) string {
	return nativeResultPrefix + token + ":" + base64.StdEncoding.EncodeToString([]byte(body)) + "\r\n"
}

func TestNativeProtocolIgnoresStartupNoise(t *testing.T) {
	noise := "Opening in existing browser session.\r\nWARNING: unrelated output\n" + string([]byte{0xd3, 0xff, '\n'})
	data := noise + framedNativeResult("old", `{"handle":12}`) + framedNativeResult("this-call", `{"handle":99,"url":"https://www.dell.com/en-us/shop/","title":"整机报价","error":"","regions":{}}`) + "trailing child output\n"
	if json.Valid([]byte(data)) {
		t.Fatal("fixture did not reproduce polluted JSON stream")
	}
	page, err := decodeNativeOutput([]byte(data), "this-call")
	if err != nil || page.Handle != 99 || page.Title != "整机报价" {
		t.Fatalf("framed result lost: %+v %v", page, err)
	}
}

func TestNativeProtocolRejectsDamagedOrForeignResults(t *testing.T) {
	for name, output := range map[string]string{
		"plain-json":    `{"handle":99}`,
		"missing-frame": "Opening in existing browser session.\n",
		"foreign-token": framedNativeResult("different", `{"handle":99}`),
		"broken-base64": nativeResultPrefix + "call:" + "!broken!\n",
		"broken-json":   framedNativeResult("call", `{"handle":`),
		"not-object":    framedNativeResult("call", "null"),
		"duplicate":     framedNativeResult("call", `{"handle":99}`) + framedNativeResult("call", `{"handle":100}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeNativeOutput([]byte(output), "call"); err == nil {
				t.Fatal("untrusted result accepted")
			}
		})
	}
	if _, err := decodeNativeOutput([]byte(framedNativeResult("", `{}`)), ""); err == nil {
		t.Fatal("empty request token accepted")
	}
}

func TestNativeProtocolActualChildOutput(t *testing.T) {
	// An actual OS child stdout round trip, not execution of PowerShell/C#.
	if os.Getenv("PRICE_MONITOR_TEST_CHILD") == "native-result" {
		fmt.Print("Opening in existing browser session.\r\n")
		fmt.Print(framedNativeResult("child-token", `{"handle":23,"title":"真实进程管道","regions":{}}`))
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeProtocolActualChildOutput$")
	cmd.Env = append(os.Environ(), "PRICE_MONITOR_TEST_CHILD=native-result")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "Opening") {
		t.Fatal("child did not reproduce leading startup output")
	}
	page, err := decodeNativeOutput(out, "child-token")
	if err != nil || page.Handle != 23 || page.Title != "真实进程管道" {
		t.Fatalf("actual process output did not round trip: %+v %v", page, err)
	}
}
