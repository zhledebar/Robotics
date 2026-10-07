# PriceStockMonitor V8.27 source

V8.27 rotates pending Dell configuration checks across products, keeps XPS representative passes on the configurator page, preserves ordinary quotes during those passes, and exports parsed ordinary Dell offer fields for diagnosis.

Build the GUI on Windows with `build_windows.bat`, or run `go test ./...` from this directory. The optional Chromium integration test uses the local fixture and can be run with `MONITOR_BROWSER_TEST=1 go test ./... -run '^TestChromiumCoupledRepresentativeTraversal$' -count=1`.

Validation evidence is in the repository's `verification` directory. Windows UIAutomation and a live Dell Pro page could not be exercised in the Linux cloud environment, so the Pro component fields remain an open live-page verification item.
