package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOwnedDOMHandshakeObeysContentDiagnosticDeadline(t *testing.T) {
	var server *httptest.Server
	entered := make(chan struct{})
	stop := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{{"type": "page", "url": liveXPSURL, "webSocketDebuggerUrl": "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/page/owned"}})
	})
	mux.HandleFunc("/devtools/page/owned", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-stop // An owned browser that never completes the handshake.
	})
	server = httptest.NewServer(mux)
	defer server.Close()
	defer close(stop)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		c, err := ownedDOMTarget(ctx, strings.TrimPrefix(server.URL, "http://127.0.0.1:"), liveXPSURL, true)
		if c != nil {
			c.ws.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("handshake not reached")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owned diagnostic connection ignored cancellation")
	}
}
