package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const nativeStagePrefix = "PSM_UIA_STAGE:"
const nativeOutputLimit = 16 * 1024 * 1024

// Process completion and pipe EOF are not the result boundary. A browser or
// accessibility provider may outlive its reader and keep a pipe open.
type nativeOutputStream struct {
	mu            sync.Mutex
	token         string
	data, pending []byte
	ready         chan struct{}
	complete      bool
	stage         string
	changed       time.Time
	overflow      bool
}

func newNativeOutputStream(token string) *nativeOutputStream {
	return &nativeOutputStream{token: token, ready: make(chan struct{}), stage: "启动后台组件", changed: time.Now()}
}
func (s *nativeOutputStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.overflow || len(s.data)+len(p) > nativeOutputLimit {
		s.overflow = true
		return 0, fmt.Errorf("后台读取输出超过范围")
	}
	s.data = append(s.data, p...)
	s.pending = append(s.pending, p...)
	for {
		n := bytes.IndexByte(s.pending, '\n')
		if n < 0 {
			break
		}
		line := bytes.TrimSpace(s.pending[:n])
		s.pending = s.pending[n+1:]
		marker := []byte(nativeStagePrefix + s.token + ":")
		if s.token != "" && bytes.HasPrefix(line, marker) {
			raw, e := base64.StdEncoding.DecodeString(string(line[len(marker):]))
			stage := string(raw)
			if e == nil && len(raw) > 0 && len(raw) <= 200 && !strings.ContainsAny(stage, "\r\n\x00") {
				s.stage = stage
				s.changed = time.Now()
			}
		}
		if !s.complete && s.token != "" && bytes.HasPrefix(line, []byte(nativeResultPrefix+s.token+":")) {
			if _, e := decodeNativeOutput(s.data, s.token); e == nil {
				s.complete = true
				close(s.ready)
			}
		}
	}
	return len(p), nil
}
func (s *nativeOutputStream) snapshot() ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...), s.stage, s.complete
}
func (s *nativeOutputStream) lastStage() string { _, stage, _ := s.snapshot(); return stage }

// Shared by the Windows launcher and real child-process regression tests.
// The result must be validated again by decodeNativeOutput after return.
func awaitNativeResult(ctx context.Context, stop func() error, done <-chan error, drained <-chan struct{}, out *nativeOutputStream) error {
	var err error
	exited := false
	var exitErr error
	select {
	case <-out.ready:
	case err = <-done:
		exited = true
		// Output can lag process exit. Drain ordinary output, but never wait forever
		// for a descendant that inherited the writer to close it.
		timer := time.NewTimer(time.Second)
		select {
		case <-out.ready:
		case <-drained:
		case <-ctx.Done():
			err = ctx.Err()
		case <-timer.C:
		}
		timer.Stop()
	case <-ctx.Done():
		err = ctx.Err()
	}
	_, _, complete := out.snapshot()
	if !exited {
		_ = stop()
		select {
		case e := <-done:
			if !complete && err == nil {
				err = e
			}
		case <-time.After(2 * time.Second):
			exitErr = fmt.Errorf("后台读取进程未完成退出")
		}
	}
	if exitErr != nil {
		return exitErr
	}
	if complete {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("后台读取组件未返回本轮完整结果（最后阶段：%s）", out.lastStage())
}

// stderr is diagnostic only. Bound it even when a browser produces noisy output.
type nativeDiagnosticStream struct {
	mu   sync.Mutex
	data []byte
}

func (s *nativeDiagnosticStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(p)
	if room := 256*1024 - len(s.data); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		s.data = append(s.data, p...)
	}
	return n, nil
}
func (s *nativeDiagnosticStream) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...)
}
func copyNativeStream(w io.Writer, r io.Reader, done chan<- struct{}) {
	_, _ = io.Copy(w, r)
	done <- struct{}{}
}

func nativeDiagnosticTail(data []byte) string {
	if len(data) > 16384 {
		data = data[len(data)-16384:]
	}
	return string(data)
}
func nativeLastStage(data []byte, token string) string {
	s := newNativeOutputStream(token)
	_, _ = s.Write(data)
	return s.lastStage()
}
