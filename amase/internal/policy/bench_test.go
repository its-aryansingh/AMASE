package policy

import (
	"encoding/json"
	"testing"

	"github.com/its-aryansingh/amase/internal/runtime"
	"github.com/its-aryansingh/toolgate/guard"
)

// benchOne measures one request type in isolation; the mixed BenchmarkDecide
// hides that file changes cost a filesystem call and shell commands do not.
func benchOne(b *testing.B, req runtime.ApprovalRequest) {
	ws := Workspace{Root: b.TempDir(), Home: b.TempDir()}
	e := NewEngine()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Decide(req, ws)
	}
}

func BenchmarkDecideRead(b *testing.B) {
	benchOne(b, runtime.ApprovalRequest{Tool: "Read", Input: json.RawMessage(`{"file_path":"main.go"}`)})
}
func BenchmarkDecideBashBenign(b *testing.B)  { benchOne(b, bash("go test ./...")) }
func BenchmarkDecideBashEvasion(b *testing.B) { benchOne(b, bash(evasion[7])) }
func BenchmarkDecideEdit(b *testing.B)        { benchOne(b, edit("main.go")) }
func BenchmarkToolgateAnalyze(b *testing.B) {
	ctx := &guard.Context{Workspace: "/w", HomeDir: "/h"}
	for i := 0; i < b.N; i++ {
		guard.Analyze(evasion[7], ctx, guard.PolicyStandard)
	}
}
