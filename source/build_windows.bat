@echo off
setlocal
cd /d "%~dp0"
where go >nul 2>nul
if errorlevel 1 (
  echo Build requires Go 1.23 or later. Running the EXE does not require Go.
  exit /b 1
)
go test -buildvcs=false ./...
if errorlevel 1 exit /b 1
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -buildvcs=false -trimpath -ldflags "-H=windowsgui -s -w" -o "..\PriceStockMonitor_Win64_V8.24.exe" .
if errorlevel 1 exit /b 1
echo Built PriceStockMonitor_Win64_V8.24.exe
