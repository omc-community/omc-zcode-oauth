package main

import (
	"os"
	"path/filepath"
	"sync"
)

// A real request's shape, kept so a failing one can be replayed byte for byte
// and diffed against what the official client sends. Off unless ZCODE_DUMP_DIR
// is set; the files hold the whole conversation, so they are written 0600 and
// each request overwrites the previous one for its model and direction.
var dumpState struct {
	sync.Mutex
	dir string
}

func init() {
	dumpState.dir = os.Getenv("ZCODE_DUMP_DIR")
}

// dumpRequest writes the outgoing body when a dump directory is configured.
func dumpRequest(model, tag string, body []byte) {
	dir := dumpState.dir
	if dir == "" || len(body) == 0 {
		return
	}
	dumpState.Lock()
	defer dumpState.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	name := filepath.Join(dir, "req-"+model+tag+".json")
	_ = os.WriteFile(name, body, 0o600)
}
