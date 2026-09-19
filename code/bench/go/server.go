package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
)

type HookPayload struct {
	ToolName  string                 `json:"tool_name"`
	ToolInput map[string]interface{} `json:"tool_input"`
}

type Features struct {
	mu    sync.Mutex
	seen  map[string]int
	edits map[string]int
	reads map[string]int
}

func (f *Features) Update(p *HookPayload) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	blob, _ := json.Marshal(map[string]interface{}{"n": p.ToolName, "i": p.ToolInput})
	sum := sha256.Sum256(blob)
	sig := fmt.Sprintf("%x", sum[:16])
	f.seen[sig]++
	target := ""
	if v, ok := p.ToolInput["file_path"].(string); ok {
		target = v
	} else if v, ok := p.ToolInput["command"].(string); ok {
		target = v
	}
	switch p.ToolName {
	case "Edit", "Write":
		f.edits[target]++
	case "Read":
		f.reads[target]++
	}
	m := float64(f.seen[sig]) / 3.0
	for _, v := range []float64{float64(f.edits[target]) / 5.0, float64(f.reads[target]) / 4.0} {
		if v > m {
			m = v
		}
	}
	return m
}

var allow = []byte(`{}`)
var deny = []byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"amase: run predicted to fail"}}`)

func main() {
	f := &Features{seen: map[string]int{}, edits: map[string]int{}, reads: map[string]int{}}
	http.HandleFunc("/hooks/pre-tool-use", func(w http.ResponseWriter, r *http.Request) {
		var p HookPayload
		json.NewDecoder(r.Body).Decode(&p)
		b := allow
		if f.Update(&p) >= 1.0 {
			b = deny
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		w.Write(b)
	})
	http.ListenAndServe("127.0.0.1:"+os.Args[1], nil)
}
