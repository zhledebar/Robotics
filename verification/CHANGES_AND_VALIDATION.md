# PriceStockMonitor V8.27

V8.26's diagnostic showed that Dell 16 Pro still had a pending configuration scan while XPS 16 repeatedly occupied the shared browser collector. XPS configuration passes also switched to the ordinary offers view and back before checking one representative choice.

V8.27 rotates pending configuration work across products. Each XPS representative pass stays on the custom configurator, verifies one coupled selection and its current quote, then yields the shared collector. A core-only observation preserves the ordinary quote and ordinary offer rows. Diagnostic exports now include each ordinary Dell offer's parsed component fields, price, stock, URL and freshness so blank fields can be traced.

## Validation

- Full Go suite with local Chromium integration: 337 passed, 1 skipped, 0 failed. The skipped test requires an additional captured page.
- Chromium ran the production `collect` path against a local XPS DOM model: zero ordinary-view switches, one real browser selection, one coupled-change confirmation, two verified result rows.
- Targeted race run for scheduler rotation, quote priority, core-only application and Chromium traversal passed.
- `go vet ./...`, Linux process/UI/runtime checks and four Node page-model checks passed.
- Windows amd64 GUI executable cross-compiled successfully.

This Linux cloud environment cannot run Windows UIAutomation/PowerShell. Requests to the live Dell product pages are blocked by the network proxy, and the supplied V8.26 diagnostic shows the Pro configuration scan was still pending. Thus scheduler fairness and XPS traversal were locally tested, but Dell 16 Pro's live component extraction remains unverified. See `verification_v827.json`, `go-tests-v827.jsonl`, and `coupled-browser-v827.log` for evidence.

Install by exiting the previous version, extracting the Windows ZIP and launching `PriceStockMonitor_Win64_V8.27.exe`. Existing products and history are preserved.
