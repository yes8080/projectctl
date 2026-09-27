// Package planner produces validated, ephemeral plan candidates from an exact
// approved GitHub baseline. It has no publication or scheduling API.
package planner

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/design/codexexec"
	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

var ErrBlocked = errors.New("planner admission blocked")
var ErrBudget = errors.New("planner budget exhausted")

// Remote is a trusted authenticated repository-bound read-only Gateway. It is
// held by the Controller side; it is never exposed to the Planner process.
type Remote interface {
	ReadRepository(context.Context) (gh.Repository, error)
	ReadIssue(context.Context, int64) (gh.Issue, error)
	ReadSource(context.Context, protocol.SourceReference) (gh.SourceBlob, error)
	ReadBranch(context.Context, string) (gh.Commit, error)
	ReadCommit(context.Context, string) (gh.Commit, error)
	ReadPullRequest(context.Context, int64) (gh.PullRequest, error)
	ListComments(context.Context, gh.Resource) ([]gh.Comment, error)
	FetchRecord(context.Context, protocol.Reference) (protocol.VerifiedRecord, error)
}
type Runtime interface {
	Run(context.Context, codexexec.Request) (codexexec.Result, error)
}
type Request struct {
	Project           protocol.Project
	Anchor            startup.Anchor
	Baseline          protocol.Reference
	Issue             gh.Resource
	TargetBranch      string
	ExpectedTargetSHA string
}
type Engine struct {
	remote  Remote
	runtime Runtime
	now     func() time.Time
}

func New(remote Remote, runtime Runtime) (*Engine, error) {
	if remote == nil || runtime == nil {
		return nil, ErrBlocked
	}
	return &Engine{remote: remote, runtime: runtime, now: time.Now}, nil
}

// FreshRunSource is a trusted Controller channel, not a Planner capability.
// It must reserve a new GitHub Run before returning, with global budget/fencing
// enforced by the Controller. Empty listings or a restart never imply freshness.
// This package cannot provide cross-process exactly-once registration.
type FreshRunSource func(context.Context, Request, string) (protocol.Reference, error)
type Admission struct{ p *permit }
type permit struct {
	used    atomic.Bool
	request Request
	digest  string
	run     protocol.Reference
}

func (Admission) MarshalJSON() ([]byte, error) { return nil, ErrBlocked }
func (*Admission) UnmarshalJSON([]byte) error  { return ErrBlocked }

// Receipt is a transient proposal, not an Approval, Plan, or dispatch authority.
// Its summaries are recomputed from Candidate, never maintained independently.
type Receipt struct {
	Run                                                protocol.Reference
	AgentInstance, ThreadID, InputSHA256, OutputSHA256 string
	Candidate                                          Candidate
	Summary                                            Summary
	InputTokens, OutputTokens                          int64
}

func runMatches(run *protocol.Run, r Request, input string) bool {
	return run.Project == r.Project && run.Subject == (protocol.Subject{Issue: r.Issue.Number}) && run.Role == protocol.RolePlanner && run.InputSHA256 == input && len(run.Inputs) == 1 && inputReference(run.Inputs[0], r.Baseline)
}

func budgetWithin(run, policy protocol.Budget) bool {
	// A Run may lower its execution timeout, but must retain the exact project
	// ceilings. Otherwise a smaller declared wall/round limit could be silently
	// ignored while the engine enforces the larger fixed-policy limit.
	if run.MaxRunSeconds <= 0 || run.MaxRunSeconds > policy.MaxRunSeconds || run.MaxRunSeconds > 1800 {
		return false
	}
	run.MaxRunSeconds = policy.MaxRunSeconds
	return run == policy
}

func (e *Engine) runs(ctx context.Context, r Request, p prepared, selected *protocol.Reference) (*protocol.Run, error) {
	comments, err := e.remote.ListComments(ctx, r.Issue)
	if err != nil {
		return nil, ErrBlocked
	}
	operations, instances, attempts := map[string]bool{}, map[string]bool{}, map[int64]bool{}
	var chosen *protocol.Run
	var count int64
	for _, c := range comments {
		if c.Content.Body == nil || !strings.Contains(*c.Content.Body, protocol.Marker) {
			continue
		}
		value, err := protocol.DecodeComment(*c.Content.Body)
		if err != nil {
			return nil, ErrBlocked
		}
		run, ok := value.(*protocol.Run)
		if !ok {
			continue
		}
		ref := protocol.Reference{Kind: "issue_comment", DatabaseID: c.DatabaseID, NodeID: c.NodeID, URL: c.URL, Author: c.Author, BodySHA256: c.Content.BodySHA256, CanonicalSHA256: c.Content.CanonicalSHA256}
		verified, err := e.remote.FetchRecord(ctx, ref)
		if err != nil || protocol.Authorize(verified, p.policy, protocol.RoleController) != nil {
			return nil, ErrBlocked
		}
		actual, err := verified.Record()
		if err != nil || !reflect.DeepEqual(actual, value) {
			return nil, ErrBlocked
		}
		if run.Project != r.Project || run.Subject != (protocol.Subject{Issue: r.Issue.Number}) || run.Role != protocol.RolePlanner || operations[run.OperationID] || instances[run.AgentInstance] || attempts[run.Attempt] {
			return nil, ErrBlocked
		}
		operations[run.OperationID], instances[run.AgentInstance], attempts[run.Attempt] = true, true, true
		count++
		b := p.manifest.Policy.Budget
		if run.Attempt > b.MaxPlanningRounds || !budgetWithin(run.Budget, b) {
			return nil, ErrBudget
		}
		if selected != nil && inputReference(ref, *selected) {
			copy := *run
			chosen = &copy
		}
	}
	for attempt := int64(1); attempt <= count; attempt++ {
		if !attempts[attempt] {
			return nil, ErrBlocked
		}
	}
	b := p.manifest.Policy.Budget
	if count > b.MaxAgentRuns || count > b.MaxPlanningRounds {
		return nil, ErrBudget
	}
	if selected == nil {
		if count >= b.MaxAgentRuns || count >= b.MaxPlanningRounds {
			return nil, ErrBudget
		}
		return nil, nil
	}
	if chosen == nil || chosen.Attempt != count || !runMatches(chosen, r, p.digest) {
		return nil, ErrBlocked
	}
	return chosen, nil
}

func (e *Engine) Admit(ctx context.Context, r Request, fresh FreshRunSource) (Admission, error) {
	if ctx == nil || fresh == nil {
		return Admission{}, ErrBlocked
	}
	p, err := e.prepare(ctx, r)
	if err != nil {
		return Admission{}, err
	}
	if _, err = e.runs(ctx, r, p, nil); err != nil {
		return Admission{}, err
	}
	ref, err := fresh(ctx, r, p.digest)
	if err != nil {
		return Admission{}, ErrBlocked
	}
	if _, err = e.runs(ctx, r, p, &ref); err != nil {
		return Admission{}, err
	}
	return Admission{p: &permit{request: r, digest: p.digest, run: ref}}, nil
}

func (e *Engine) Execute(ctx context.Context, a Admission) (Receipt, error) {
	var zero Receipt
	if ctx == nil || a.p == nil || !a.p.used.CompareAndSwap(false, true) {
		return zero, ErrBlocked
	}
	r := a.p.request
	p, err := e.prepare(ctx, r)
	if err != nil {
		return zero, err
	}
	if p.digest != a.p.digest {
		return zero, ErrBlocked
	}
	run, err := e.runs(ctx, r, p, &a.p.run)
	if err != nil {
		return zero, err
	}
	limit := time.Duration(run.Budget.MaxRunSeconds) * time.Second
	if remaining := p.deadline.Sub(e.now()); remaining < limit {
		limit = remaining
	}
	if limit <= 0 {
		return zero, ErrBudget
	}
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	result, err := e.runtime.Run(runCtx, codexexec.Request{Prompt: p.prompt, Schema: p.schema, Timeout: limit})
	if err != nil || runCtx.Err() != nil || !e.now().Before(p.deadline) {
		return zero, ErrBlocked
	}
	if result.ThreadID == "" || result.InputTokens < 0 || result.OutputTokens < 0 {
		return zero, ErrBlocked
	}
	candidate, err := DecodeCandidate(result.Output)
	if err != nil {
		return zero, ErrBlocked
	}
	summary, err := ValidateCandidate(candidate, p.document.Requirements, p.manifest.Policy.Environment)
	if err != nil {
		return zero, ErrBlocked
	}
	after, err := e.prepare(ctx, r)
	if err != nil || after.digest != p.digest {
		return zero, ErrBlocked
	}
	afterRun, err := e.runs(ctx, r, after, &a.p.run)
	if err != nil || !reflect.DeepEqual(afterRun, run) || runCtx.Err() != nil || !e.now().Before(after.deadline) {
		return zero, ErrBlocked
	}
	return Receipt{Run: a.p.run, AgentInstance: run.AgentInstance, ThreadID: result.ThreadID, InputSHA256: p.digest, OutputSHA256: protocol.SHA256(result.Output), Candidate: candidate, Summary: summary, InputTokens: result.InputTokens, OutputTokens: result.OutputTokens}, nil
}
func (r Receipt) String() string {
	return fmt.Sprintf("planner candidate %s %s", r.AgentInstance, r.OutputSHA256)
}
