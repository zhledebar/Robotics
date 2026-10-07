//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

func readOwnedContentStatus(ctx context.Context, p nativePage, r nativeRequest) (string, error) {
	raw, err := os.ReadFile(filepath.Join(ownedDOMProfile(r), "DevToolsActivePort"))
	if err != nil {
		return "", err
	}
	port := strings.Split(strings.TrimSpace(string(raw)), "\n")[0]
	c, err := ownedDOMTarget(ctx, port, r.URL, r.Family)
	if err != nil {
		return "", err
	}
	defer c.ws.Close()
	status, err := c.eval(ctx, ownedContentStatusExpression(p))
	if err != nil {
		return "", err
	}
	return verifyOwnedContentStatus(p, status)
}
