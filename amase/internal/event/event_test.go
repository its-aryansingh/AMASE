package event

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func samplePayload(t Type) any {
	exit := 1
	switch t {
	case DaemonStarted:
		return DaemonStartedData{Version: "0.1.0", PID: 42, PrevShutdown: ShutdownUnclean, TornTailBytes: 17, InterruptedTurns: 1, ExpiredApprovals: 2, PossibleReceiptGap: true}
	case DaemonStopped:
		return DaemonStoppedData{Reason: "signal"}
	case RepoRegistered:
		return RepoRegisteredData{RepoID: "abc123def456", Path: "/work/demo"}
	case TaskCreated:
		return TaskCreatedData{TaskID: "task_1", RepoID: "abc", Harness: "claude", BaseRef: "HEAD", Prompt: "fix the test"}
	case WorktreeCreated, WorktreeRemoved:
		return WorktreeData{Path: "/state/wt/abc/task_1", Branch: "amase/task_1"}
	case SessionStarted:
		return SessionStartedData{ProviderID: "96ea6bd6", HarnessVersion: "2.1.331", Model: "claude-haiku-5-5"}
	case SessionEnded:
		return SessionEndedData{Reason: "closed"}
	case TurnStarted:
		return TurnStartedData{Prompt: "fix it"}
	case AssistantText:
		return AssistantTextData{Text: "done"}
	case ToolRequested:
		return ToolRequestedData{CallID: "toolu_1", Tool: "Bash", Input: json.RawMessage(`{"command":"ls"}`), Action: json.RawMessage(`{"category":"read"}`), Fingerprint: "ff"}
	case PolicyDecided:
		return PolicyDecidedData{CallID: "toolu_1", Verdict: "deny", Rules: []string{"TG001"}, Reason: "curl piped to sh", Source: "can_use_tool", DecidedInNs: 14300}
	case ApprovalRequested:
		return ApprovalRequestedData{ApprovalID: "apr_1", CallID: "toolu_1", Tool: "Bash", Command: "rm -rf docs", Rules: []string{"TG007"}, Deadline: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)}
	case ApprovalResolved:
		return ApprovalResolvedData{ApprovalID: "apr_1", CallID: "toolu_1", Decision: "allow", By: "human"}
	case ApprovalExpired:
		return ApprovalExpiredData{ApprovalID: "apr_1", CallID: "toolu_1", Reason: "daemon_crash"}
	case ToolCompleted:
		return ToolCompletedData{CallID: "toolu_1", IsError: true, ExitCode: &exit, OutputBytes: 10, OutputSHA256: "aa", OutputPrefix: "boom"}
	case UsageReported:
		return UsageReportedData{MessageID: "msg_1", Model: "claude-haiku-5-5", Input: 2, Output: 6, CacheRead: 16580, CacheWrite: 9370}
	case CheckpointSaved:
		return CheckpointSavedData{Phase: "pre", Ref: "refs/amase/turns/s/1/pre", Commit: "c0ffee", Tree: "7ree"}
	case RewindPerformed:
		return RewindPerformedData{ToRef: "refs/amase/turns/s/1/pre", UndoRef: "refs/amase/undo/t/1", Restored: []string{"main.go"}, Removed: []string{"new.txt"}}
	case GovernorSignal:
		return GovernorSignalData{Kind: "termination_lag", Steps: 6, Tokens: 1200, Mode: "shadow"}
	case PinInjected:
		return PinInjectedData{BundleSHA256: "abc", TokensEst: 47}
	case AttritionDetected:
		return AttritionDetectedData{CallID: "toolu_1", Kind: "truncated_json"}
	case CriticReport:
		return CriticReportData{Findings: json.RawMessage(`[{"severity":"low"}]`)}
	case TurnCompleted:
		return TurnCompletedData{Status: StatusOK, StopReason: "end_turn", DurationMs: 4677, CostUSD: 0.0039}
	case TurnInterrupted:
		return TurnInterruptedData{Reason: "daemon_crash"}
	case QueueDropped:
		return QueueDroppedData{Queue: "low", Count: 3}
	}
	return nil
}

func TestEveryTypeRoundTrips(t *testing.T) {
	for _, typ := range All {
		t.Run(string(typ), func(t *testing.T) {
			want := samplePayload(typ)
			if want == nil {
				t.Fatalf("no sample payload for %s", typ)
			}
			env, err := New(typ, want)
			if err != nil {
				t.Fatal(err)
			}
			env = env.With("repo", "task", "sess", 3)

			wire, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			var back Envelope
			if err := json.Unmarshal(wire, &back); err != nil {
				t.Fatal(err)
			}
			if back.Type != typ || back.Repo != "repo" || back.Task != "task" || back.Session != "sess" || back.Turn != 3 {
				t.Fatalf("envelope fields lost: %+v", back)
			}

			got := reflect.New(reflect.TypeOf(want))
			if err := back.Decode(got.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Elem().Interface(), want) {
				t.Fatalf("payload changed in transit:\n got %+v\nwant %+v", got.Elem().Interface(), want)
			}
		})
	}
}

func TestAllHasNoDuplicates(t *testing.T) {
	seen := map[Type]bool{}
	for _, typ := range All {
		if seen[typ] {
			t.Fatalf("duplicate type %s", typ)
		}
		seen[typ] = true
	}
}

func TestOversizedPayloadIsRejected(t *testing.T) {
	_, err := New(AssistantText, AssistantTextData{Text: strings.Repeat("x", MaxDataBytes)})
	if err == nil {
		t.Fatal("a payload over MaxDataBytes was accepted")
	}
}

func TestPriority(t *testing.T) {
	for _, typ := range All {
		low := typ == AssistantText || typ == UsageReported || typ == AttritionDetected
		if got := typ.Priority() == PriorityLow; got != low {
			t.Errorf("%s: low priority = %v, want %v", typ, got, low)
		}
	}
}

func TestNewIDIsPrefixedUniqueAndSortable(t *testing.T) {
	ids := make([]string, 0, 50)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := NewID("task")
		if !strings.HasPrefix(id, "task_") || len(id) != len("task_")+12+10 {
			t.Fatalf("bad id shape %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
		ids = append(ids, id)
		if i%10 == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	first, last := ids[0], ids[len(ids)-1]
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if sorted[0][:17] != first[:17] || sorted[len(sorted)-1][:17] != last[:17] {
		t.Fatal("ids do not sort by creation time")
	}
}

func TestClipKeepsUTF8Intact(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := Clip(s, 5)
	if !strings.HasPrefix(got, "éé") || strings.Contains(got, "�") {
		t.Fatalf("clip split a rune: %q", got)
	}
	if Clip("short", 10) != "short" {
		t.Fatal("clip changed a string under the limit")
	}
}
