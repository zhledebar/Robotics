package main

import (
	"crypto/sha256"
	"os"
	"sync"
)

// Cache only assemblies compiled during this process. A saved filename alone
// never authorizes loading a DLL, and changed bytes force a fresh compilation.
var readerAssemblies = struct {
	sync.Mutex
	digests map[string][32]byte
}{digests: make(map[string][32]byte)}

func readerAssemblyReady(path string) bool {
	readerAssemblies.Lock()
	expected, ok := readerAssemblies.digests[path]
	readerAssemblies.Unlock()
	if !ok {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && sha256.Sum256(data) == expected
}

func rememberReaderAssembly(path string) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return
	}
	readerAssemblies.Lock()
	readerAssemblies.digests[path] = sha256.Sum256(data)
	readerAssemblies.Unlock()
}
