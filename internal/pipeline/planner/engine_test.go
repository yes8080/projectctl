package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/design/codexexec"
	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type latePlannerRemote struct {
	Remote
	lists int
	late  func()
}

func (r *latePlannerRemote) ListComments(ctx context.Context, issue gh.Resource) ([]gh.Comment, error) {
	r.lists++
	if r.lists == 4 {
		r.late()
	}
	return r.Remote.ListComments(ctx, issue)
}

func TestPlannerDeadlineAfterFinalRemoteRead(t *testing.T) {
	e, f, r, _ := engineFixture(t)
	e.remote = &latePlannerRemote{Remote: f, late: func() { f.clock = f.clock.Add(2 * time.Hour) }}
	a, err := e.Admit(context.Background(), r, f.fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Execute(context.Background(), a); err == nil {
		t.Fatal("returned receipt after final read passed wall deadline")
	}
}

func engineCandidate(f *plannerInputFixture) Candidate {
	c := Candidate{Schema: CandidateSchema, Contracts: []Slice{}, OpenDecisions: []string{}}
	previous := []string{}
	for i, req := range f.document.Requirements {
		kind, binding := "implementation", "none"
		if req.ID == "integration" {
			kind, binding = "integration", "exact_source_artifact_environment"
		}
		key := fmt.Sprintf("slice-%d", i)
		c.Contracts = append(c.Contracts, Slice{Key: key, Kind: kind, Goal: req.Statement, Scope: []string{req.Statement}, NonGoals: []string{"unapproved expansion"}, Requirements: []string{req.ID}, Acceptance: []Criterion{{ID: "AC-1", Statement: "verify specified behavior", Requirements: []string{req.ID}, Checks: []string{"check-1"}}}, Verification: []protocol.Check{{ID: "check-1", Argv: []string{"go", "test", "./..."}}}, AcceptanceEnvironment: protocol.Environment{OS: append([]string(nil), f.manifest.Policy.Environment.OperatingSystems...), Go: "1.22", Network: "forbidden", Credentials: "none", Fixtures: "deterministic unit fixtures"}, ImpactSurface: []string{"package tests"}, Risks: []protocol.Risk{{Risk: "regression", Mitigation: "deterministic checks"}}, Dependencies: append([]string{}, previous...), CandidateBinding: binding})
		previous = append(previous, key)
	}
	return c
}
func engineFixture(t *testing.T) (*Engine, *plannerInputFixture, Request, *plannerRuntime) {
	t.Helper()
	f, r := newPlannerInputFixture(t)
	runtime := &plannerRuntime{output: inputFixtureJSON(t, engineCandidate(f))}
	e, err := New(f, runtime)
	if err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return f.clock }
	return e, f, r, runtime
}

func TestPlannerExecuteCandidateOnlyAndFreshBudget(t *testing.T) {
	e, f, r, rt := engineFixture(t)
	for i := 0; i < int(f.manifest.Policy.Budget.MaxPlanningRounds); i++ {
		// Reconstructing an engine has no effect on remote consumed reservations.
		e, _ = New(f, rt)
		e.now = func() time.Time { return f.clock }
		a, err := e.Admit(context.Background(), r, f.fresh(t))
		if err != nil {
			t.Fatal(err)
		}
		got, err := e.Execute(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		if got.Run.DatabaseID == 0 || got.ThreadID == "" || len(got.Summary.Coverage) != len(f.document.Requirements) || got.Summary.Integration == "" || got.OutputSHA256 != protocol.SHA256(rt.output) {
			t.Fatal("incomplete candidate receipt")
		}
		if _, err = e.Execute(context.Background(), a); !errors.Is(err, ErrBlocked) {
			t.Fatal("reused permit", err)
		}
	}
	if _, err := e.Admit(context.Background(), r, f.fresh(t)); !errors.Is(err, ErrBudget) {
		t.Fatal("restart reset budget", err)
	}
	if rt.calls.Load() != f.manifest.Policy.Budget.MaxPlanningRounds {
		t.Fatal("unexpected dispatches")
	}
}

func TestPlannerAdmissionCopiesConcurrentExactlyOne(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		e, f, r, rt := engineFixture(t)
		a, err := e.Admit(context.Background(), r, f.fresh(t))
		if err != nil {
			t.Fatal(err)
		}
		var successes atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(copy Admission) {
				defer wg.Done()
				if _, err := e.Execute(context.Background(), copy); err == nil {
					successes.Add(1)
				} else if !errors.Is(err, ErrBlocked) {
					t.Error(err)
				}
			}(a)
		}
		wg.Wait()
		if successes.Load() != 1 || rt.calls.Load() != 1 {
			t.Fatalf("success=%d calls=%d", successes.Load(), rt.calls.Load())
		}
	}
}

func TestPlannerFailureConsumesAdmissionAndRejectsDrift(t *testing.T) {
	for _, name := range []string{"cancel", "runtime_failure", "invalid_output", "branch_before", "branch_after", "edited_run", "deleted_run", "wall_time", "late_output", "malformed_comment", "duplicate_operation", "old_input", "budget_expansion"} {
		t.Run(name, func(t *testing.T) {
			e, f, r, rt := engineFixture(t)
			a, err := e.Admit(context.Background(), r, f.fresh(t))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "cancel":
				cancel()
			case "runtime_failure":
				rt.err = errors.New("failed")
			case "invalid_output":
				rt.output = []byte(`{}`)
			case "branch_before":
				f.branch.SHA = inputFixtureSHA("1")
			case "branch_after":
				rt.hook = func(context.Context, codexexec.Request) { f.branch.SHA = inputFixtureSHA("1") }
			case "edited_run":
				o := f.observations[a.p.run.DatabaseID]
				o.Body += " "
				f.observations[a.p.run.DatabaseID] = o
			case "deleted_run":
				delete(f.observations, a.p.run.DatabaseID)
			case "wall_time":
				f.clock = f.clock.Add(2 * time.Hour)
			case "late_output":
				rt.hook = func(context.Context, codexexec.Request) { f.clock = f.clock.Add(2 * time.Hour) }
			case "malformed_comment":
				bad := protocol.Marker + ` {"schema":"bad"}`
				f.comments[0].Content.Body = &bad
			case "duplicate_operation", "old_input", "budget_expansion":
				o := f.observations[a.p.run.DatabaseID]
				value, _ := protocol.DecodeComment(o.Body)
				run := value.(*protocol.Run)
				if name == "duplicate_operation" {
					run.AgentInstance = "other-instance"
					run.Attempt = 2
				} else if name == "old_input" {
					run.InputSHA256 = protocol.SHA256([]byte("old input"))
					f.comments = nil
				} else {
					run.Budget.MaxCostMinorUnits++
					f.comments = nil
				}
				ref := f.record(t, run, f.manifest.Policy.Roles.Controller, true)
				if name != "duplicate_operation" {
					a.p.run = ref
				}
			}
			if _, err = e.Execute(ctx, a); err == nil {
				t.Fatal("accepted rejected case")
			}
			if _, err = e.Execute(context.Background(), a); !errors.Is(err, ErrBlocked) {
				t.Fatal("refunded failed permit", err)
			}
		})
	}
}

func TestPlannerRuntimeHasFiniteDeadlineAndNoRecords(t *testing.T) {
	e, f, r, rt := engineFixture(t)
	rt.hook = func(ctx context.Context, request codexexec.Request) {
		if _, ok := ctx.Deadline(); !ok || request.Timeout <= 0 || request.Timeout > 60*time.Second {
			t.Fatal("unbounded runtime")
		}
		if len(request.Schema) == 0 || request.Prompt == "" {
			t.Fatal("missing strict input")
		}
	}
	a, err := e.Admit(context.Background(), r, f.fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Execute(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

type plannerRuntime struct {
	calls  atomic.Int64
	output []byte
	hook   func(context.Context, codexexec.Request)
	err    error
}

func (r *plannerRuntime) Run(ctx context.Context, request codexexec.Request) (codexexec.Result, error) {
	r.calls.Add(1)
	if r.hook != nil {
		r.hook(ctx, request)
	}
	return codexexec.Result{ThreadID: "native-fresh-thread", Output: append([]byte(nil), r.output...), InputTokens: 1, OutputTokens: 2}, r.err
}

func TestUnusableAdmissionsAndPorts(t *testing.T) {
	if _, err := New(nil, &plannerRuntime{}); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	var a Admission
	if _, err := json.Marshal(a); err == nil {
		t.Fatal("serialized admission")
	}
	if err := json.Unmarshal([]byte(`{}`), &a); err == nil {
		t.Fatal("deserialized admission")
	}
	e := &Engine{}
	if _, err := e.Execute(context.Background(), a); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	if _, err := e.Admit(context.Background(), Request{}, nil); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	if _, err := e.Admit(nil, Request{}, func(context.Context, Request, string) (protocol.Reference, error) {
		t.Fatal("allocated")
		return protocol.Reference{}, nil
	}); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
}

func TestRunInputBindingRequiresExactBaseline(t *testing.T) {
	r := Request{Project: protocol.Project{Owner: "octo", Repo: "pipeline", ControlIssue: 1}}
	r.Issue.Number = 2
	r.Baseline = protocol.Reference{Kind: "issue_comment", DatabaseID: 10, NodeID: "N10", URL: "url", BodySHA256: "body", CanonicalSHA256: "canonical", Author: protocol.GitHubIdentity{ID: 1, NodeID: "U1", Type: "User"}}
	run := protocol.Run{Envelope: protocol.Envelope{Project: r.Project, Subject: protocol.Subject{Issue: 2}}, Role: protocol.RolePlanner, InputSHA256: "input", Inputs: []protocol.Reference{r.Baseline}}
	if !runMatches(&run, r, "input") {
		t.Fatal("valid input")
	}
	for _, change := range []func(*protocol.Run){
		func(x *protocol.Run) { x.InputSHA256 = "other" },
		func(x *protocol.Run) { x.Inputs = nil },
		func(x *protocol.Run) { x.Inputs = append(x.Inputs, r.Baseline) },
		func(x *protocol.Run) { x.Inputs[0].BodySHA256 = "edited" },
		func(x *protocol.Run) { x.Inputs[0].Author.ID = 9 },
		func(x *protocol.Run) { x.Role = protocol.RoleDesigner },
		func(x *protocol.Run) { x.Subject.Issue = 3 },
		func(x *protocol.Run) { x.Project.Repo = "other" },
	} {
		copy := run
		copy.Inputs = append([]protocol.Reference(nil), run.Inputs...)
		change(&copy)
		if runMatches(&copy, r, "input") {
			t.Fatal("accepted drifted input")
		}
	}
}

func TestRunBudgetCannotRedefineProjectCeilings(t *testing.T) {
	policy := protocol.Budget{MaxAgentRuns: 20, MaxRunSeconds: 60, MaxWallSeconds: 3600, MaxDesignRounds: 3, MaxPlanningRounds: 5, MaxImplementationRounds: 3, Currency: "USD"}
	if !budgetWithin(policy, policy) {
		t.Fatal("exact policy rejected")
	}
	short := policy
	short.MaxRunSeconds = 1
	if !budgetWithin(short, policy) {
		t.Fatal("shorter per-run timeout rejected")
	}
	for _, change := range []func(*protocol.Budget){func(b *protocol.Budget) { b.MaxWallSeconds = 1 }, func(b *protocol.Budget) { b.MaxPlanningRounds = 1 }, func(b *protocol.Budget) { b.MaxAgentRuns = 1 }, func(b *protocol.Budget) { b.MaxCostMinorUnits = 1 }, func(b *protocol.Budget) { b.Currency = "EUR" }, func(b *protocol.Budget) { b.MaxRunSeconds = 0 }, func(b *protocol.Budget) { b.MaxRunSeconds = 61 }} {
		b := policy
		change(&b)
		if budgetWithin(b, policy) {
			t.Fatal("run silently redefined fixed project ceilings")
		}
	}
}
