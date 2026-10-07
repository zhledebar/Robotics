package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
)

const nativeResultPrefix = "PSM_UIA_RESULT:"

// Browser startup text, PowerShell warnings and legacy code-page output are
// unrelated to the result. Only this invocation's nonce-tagged ASCII frame is
// accepted, never an arbitrary JSON substring printed by a child process.
func decodeNativeOutput(output []byte, token string) (nativePage, error) {
	var page nativePage
	if token == "" || len(output) > 16*1024*1024 {
		return page, errors.New("读取组件结果标记无效或结果超出范围")
	}
	marker := []byte(nativeResultPrefix + token + ":")
	var payload []byte
	found := false
	for _, line := range bytes.Split(output, []byte{'\n'}) {
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte{0xef, 0xbb, 0xbf}))
		if !bytes.HasPrefix(line, marker) {
			continue
		}
		if found {
			return page, errors.New("读取组件返回多个结果；本轮没有接受报价")
		}
		found = true
		payload = line[len(marker):]
	}
	if !found {
		return page, errors.New("读取组件没有返回本轮结果标记")
	}
	body, err := base64.StdEncoding.DecodeString(string(payload))
	if err != nil {
		return page, errors.New("读取组件结果编码损坏")
	}
	if len(body) == 0 || body[0] != '{' {
		return page, errors.New("读取组件结果不是有效对象")
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nativePage{}, errors.New("读取组件结果格式损坏")
	}
	return page, nil
}
