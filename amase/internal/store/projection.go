package store

import (
	"time"

	"github.com/its-aryansingh/amase/internal/event"
)

// State is the projection of the event log: what the UI and the manager need
// to know right now, rebuilt from scratch by replay at every start.
type State struct {
	LastSeq   uint64               `json:"last_seq"`
	Daemon    Daemon               `json:"daemon"`
	Repos     map[string]string    `json:"repos"`
	Tasks     map[string]*Task     `json:"tasks"`
	Approvals map[string]*Approval `json:"approvals"`
	Usage     Usage                `json:"usage"`
}

type Daemon struct {
	StartedSeq   uint64 `json:"started_seq"`
	Version      string `json:"version"`
	PrevShutdown string `json:"prev_shutdown"`
	Running      bool   `json:"running"`
}

type Task struct {
	ID         string  `json:"id"`
	RepoID     string  `json:"repo_id"`
	Harness    string  `json:"harness"`
	Agent      string  `json:"agent,omitempty"`
	BaseRef    string  `json:"base_ref"`
	Prompt     string  `json:"prompt"`
	Worktree   string  `json:"worktree,omitempty"`
	Branch     string  `json:"branch,omitempty"`
	Session    string  `json:"session,omitempty"`
	ProviderID string  `json:"provider_id,omitempty"`
	Model      string  `json:"model,omitempty"`
	Removed    bool    `json:"removed,omitempty"`
	Turns      []*Turn `json:"turns"`
	Usage      Usage   `json:"usage"`
	Signals    int     `json:"signals"`
}

type Turn struct {
	N           int               `json:"n"`
	Session     string            `json:"session"`
	Status      string            `json:"status"`
	Prompt      string            `json:"prompt"`
	StopReason  string            `json:"stop_reason,omitempty"`
	CostUSD     float64           `json:"cost_usd,omitempty"`
	Checkpoints map[string]string `json:"checkpoints,omitempty"` // phase → commit
}

type Approval struct {
	ID       string    `json:"id"`
	CallID   string    `json:"call_id"`
	Task     string    `json:"task"`
	Session  string    `json:"session"`
	Turn     int       `json:"turn"`
	Tool     string    `json:"tool"`
	Command  string    `json:"command,omitempty"`
	Rules    []string  `json:"rules,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Deadline time.Time `json:"deadline"`
	State    string    `json:"state"`
	By       string    `json:"by,omitempty"`
}

type Usage struct {
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	CostUSD    float64 `json:"cost_usd"`
}

func newState() *State {
	return &State{
		Repos:     map[string]string{},
		Tasks:     map[string]*Task{},
		Approvals: map[string]*Approval{},
	}
}

// apply folds one committed event into the projection. Unknown or malformed
// payloads are ignored: the projection is a cache of the log, and refusing to
// start over one odd record would turn a display problem into an outage.
func (s *State) apply(c Committed) {
	s.LastSeq = c.Seq
	e := c.Env
	task := s.Tasks[e.Task]

	switch e.Type {
	case event.DaemonStarted:
		var d event.DaemonStartedData
		if e.Decode(&d) == nil {
			s.Daemon = Daemon{StartedSeq: c.Seq, Version: d.Version, PrevShutdown: d.PrevShutdown, Running: true}
		}
	case event.DaemonStopped:
		s.Daemon.Running = false
	case event.RepoRegistered:
		var d event.RepoRegisteredData
		if e.Decode(&d) == nil {
			s.Repos[d.RepoID] = d.Path
		}
	case event.TaskCreated:
		var d event.TaskCreatedData
		if e.Decode(&d) == nil {
			s.Tasks[d.TaskID] = &Task{ID: d.TaskID, RepoID: d.RepoID, Harness: d.Harness, Agent: d.Agent, BaseRef: d.BaseRef, Prompt: d.Prompt}
		}
	case event.WorktreeCreated:
		var d event.WorktreeData
		if task != nil && e.Decode(&d) == nil {
			task.Worktree, task.Branch = d.Path, d.Branch
		}
	case event.WorktreeRemoved:
		if task != nil {
			task.Removed = true
		}
	case event.SessionStarted:
		var d event.SessionStartedData
		if task != nil && e.Decode(&d) == nil {
			task.Session, task.ProviderID, task.Model = e.Session, d.ProviderID, d.Model
		}
	case event.TurnStarted:
		var d event.TurnStartedData
		if task != nil && e.Decode(&d) == nil {
			task.Turns = append(task.Turns, &Turn{N: e.Turn, Session: e.Session, Status: event.StatusRunning, Prompt: d.Prompt})
		}
	case event.TurnCompleted:
		var d event.TurnCompletedData
		if t := task.turn(e.Turn); t != nil && e.Decode(&d) == nil {
			t.Status, t.StopReason, t.CostUSD = d.Status, d.StopReason, d.CostUSD
			task.Usage.CostUSD += d.CostUSD
			s.Usage.CostUSD += d.CostUSD
		}
	case event.TurnInterrupted:
		if t := task.turn(e.Turn); t != nil {
			t.Status = event.StatusInterrupted
		}
	case event.CheckpointSaved:
		var d event.CheckpointSavedData
		if t := task.turn(e.Turn); t != nil && e.Decode(&d) == nil {
			if t.Checkpoints == nil {
				t.Checkpoints = map[string]string{}
			}
			t.Checkpoints[d.Phase] = d.Commit
		}
	case event.ApprovalRequested:
		var d event.ApprovalRequestedData
		if e.Decode(&d) == nil {
			s.Approvals[d.ApprovalID] = &Approval{ID: d.ApprovalID, CallID: d.CallID, Task: e.Task, Session: e.Session, Turn: e.Turn,
				Tool: d.Tool, Command: d.Command, Rules: d.Rules, Reason: d.Reason, Deadline: d.Deadline, State: event.StatePending}
		}
	case event.ApprovalResolved:
		var d event.ApprovalResolvedData
		if e.Decode(&d) == nil {
			if a := s.Approvals[d.ApprovalID]; a != nil {
				a.By = d.By
				switch {
				case d.By == "timeout":
					a.State = event.StateTimedOut
				case d.Decision == "allow":
					a.State = event.StateApproved
				default:
					a.State = event.StateDenied
				}
			}
		}
	case event.ApprovalExpired:
		var d event.ApprovalExpiredData
		if e.Decode(&d) == nil {
			if a := s.Approvals[d.ApprovalID]; a != nil {
				a.State = event.StateExpired
			}
		}
	case event.UsageReported:
		var d event.UsageReportedData
		if e.Decode(&d) == nil {
			u := Usage{Input: d.Input, Output: d.Output, CacheRead: d.CacheRead, CacheWrite: d.CacheWrite}
			s.Usage.add(u)
			if task != nil {
				task.Usage.add(u)
			}
		}
	case event.GovernorSignal:
		if task != nil {
			task.Signals++
		}
	}
}

func (u *Usage) add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
}

func (t *Task) turn(n int) *Turn {
	if t == nil {
		return nil
	}
	for i := len(t.Turns) - 1; i >= 0; i-- {
		if t.Turns[i].N == n {
			return t.Turns[i]
		}
	}
	return nil
}

// TurnRef identifies one turn.
type TurnRef struct {
	Repo, Task, Session string
	Turn                int
}

// runningTurns lists turns still marked running — after a restart, every one of
// them died with the previous daemon.
func (s *State) runningTurns() []TurnRef {
	var out []TurnRef
	for _, t := range s.Tasks {
		for _, tr := range t.Turns {
			if tr.Status == event.StatusRunning {
				out = append(out, TurnRef{Repo: t.RepoID, Task: t.ID, Session: tr.Session, Turn: tr.N})
			}
		}
	}
	return out
}

func (s *State) pendingApprovals() []*Approval {
	var out []*Approval
	for _, a := range s.Approvals {
		if a.State == event.StatePending {
			out = append(out, a)
		}
	}
	return out
}

// clone returns a deep copy, so callers can read it without holding the lock
// and without seeing it change underneath them.
func (s *State) clone() State {
	out := *s
	out.Repos = make(map[string]string, len(s.Repos))
	for k, v := range s.Repos {
		out.Repos[k] = v
	}
	out.Tasks = make(map[string]*Task, len(s.Tasks))
	for k, t := range s.Tasks {
		ct := *t
		ct.Turns = make([]*Turn, len(t.Turns))
		for i, tr := range t.Turns {
			c := *tr
			if tr.Checkpoints != nil {
				c.Checkpoints = make(map[string]string, len(tr.Checkpoints))
				for p, v := range tr.Checkpoints {
					c.Checkpoints[p] = v
				}
			}
			ct.Turns[i] = &c
		}
		out.Tasks[k] = &ct
	}
	out.Approvals = make(map[string]*Approval, len(s.Approvals))
	for k, a := range s.Approvals {
		ca := *a
		ca.Rules = append([]string(nil), a.Rules...)
		out.Approvals[k] = &ca
	}
	return out
}
