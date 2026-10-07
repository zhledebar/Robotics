package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

func processID() int { return os.Getpid() }
func main() {
	port := flag.Int("port", 38840, "local UI port; 0 chooses a free port")
	dataDir := flag.String("data-dir", "", "data folder")
	noOpen := flag.Bool("no-open", false, "do not open UI")
	noDesktop := flag.Bool("no-desktop-alerts", false, "disable OS popups for automated verification")
	flag.Parse()
	l, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if e != nil {
		if *port == 38840 {
			r, e := (&http.Client{Timeout: 2 * time.Second}).Get("http://127.0.0.1:38840/api/version")
			if e == nil {
				var v map[string]any
				_ = jsonDecode(r.Body, &v)
				_ = r.Body.Close()
				if v["version"] == version {
					openURL("http://127.0.0.1:38840/")
					return
				}
			}
		}
		showError("端口被占用。请先在旧版本界面点击“退出程序”，然后打开 V8.27。没有关闭其他程序或修改数据。")
		return
	}
	defer l.Close()
	if *dataDir == "" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base, _ = os.UserConfigDir()
		}
		*dataDir = filepath.Join(base, "PriceStockMonitor")
	}
	a, e := newApp(*dataDir, *noDesktop)
	if e != nil {
		showError(e.Error())
		return
	}
	f, e := os.OpenFile(filepath.Join(*dataDir, "app_v827.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e == nil {
		defer f.Close()
		log.SetOutput(f)
	}
	defer a.shutdown() // Clean up owned windows before closing the diagnostic log.
	url := "http://" + l.Addr().String() + "/"
	log.Printf("%s started %s", version, url)
	fmt.Println(url)
	srv := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	a.start()
	go func() {
		if e := srv.Serve(l); e != nil && e != http.ErrServerClosed {
			log.Print(e)
			a.cancel()
		}
	}()
	if !*noOpen {
		openURL(url)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	select {
	case <-a.ctx.Done():
	case <-sig:
		a.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
