// Package events emits a structured JSONL event stream for a run, so an operator
// (human or agent) can watch reliably by tailing/parsing it — instead of grepping
// human-readable output. One JSON object per line, e.g.
//
//	{"t":"bash","cmd":"segmentstream init","exit":0}
//	{"t":"login_url","url":"https://accounts.google.com/..."}
//	{"t":"gate","needs":"a browser login is required"}
//	{"t":"outcome","outcome":"goal_reached","goal_reached":true}
//
// A package-global sink keeps emit sites free of plumbing; it is io.Discard until
// SetOutput is called (so events cost nothing unless --events is set).
package events

import (
	"encoding/json"
	"io"
	"sync"
)

var (
	mu  sync.Mutex
	out io.Writer = io.Discard
)

// SetOutput directs the event stream to w (e.g. a --events file).
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	out = w
}

// Emit writes one JSON event: {"t":<type>, <kv pairs>}. kv is a flat list of
// string keys and values: Emit("bash", "cmd", c, "exit", code).
func Emit(t string, kv ...any) {
	m := make(map[string]any, len(kv)/2+1)
	m["t"] = t
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m[k] = kv[i+1]
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	out.Write(append(b, '\n'))
}
