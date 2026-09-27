package design

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/design/codexexec"
	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

func native(id int64) protocol.GitHubIdentity {
	return protocol.GitHubIdentity{ID: id, NodeID: fmt.Sprint("U", id), Login: fmt.Sprint("user", id), Type: "User"}
}
func resource(kind string, n int64) gh.Resource {
	return gh.Resource{Kind: kind, Number: n, DatabaseID: 100 + n, NodeID: fmt.Sprint("N", n)}
}
func sha(c string) string { return strings.Repeat(c, 40) }
func sourceRef(path string, data []byte) protocol.SourceReference {
	return protocol.SourceReference{Commit: sha("a"), Path: path, SHA256: protocol.SHA256(data)}
}
func data(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func goodDocument() Document {
	d := Document{Schema: "projectctl.design-output/v1", Requirements: []Requirement{{"REQ-1", "specified requirement"}}, OpenQuestions: []string{}, Sections: []Section{}}
	for _, id := range sectionIDs {
		d.Sections = append(d.Sections, Section{id, "Explicit design for " + id})
	}
	return d
}
func goodReview(hash string) Review {
	r := Review{Schema: "projectctl.design-review/v1", CandidateSHA256: hash, Decision: "PASS", Reason: "independent complete review", Assessments: []Assessment{}}
	for _, id := range requiredSections() {
		r.Assessments = append(r.Assessments, Assessment{id, "PASS", "checked original inputs"})
	}
	return r
}

type fixture struct {
	mu           sync.Mutex
	startup      startup.State
	repo         gh.Repository
	issues       map[int64]gh.Issue
	sources      map[protocol.SourceReference]gh.SourceBlob
	observations map[int64]protocol.GitHubObservation
	comments     []gh.Comment
	branch       gh.Commit
	commits      map[string]gh.Commit
	pr           gh.PullRequest
	output       []byte
	calls        int
	requests     []codexexec.Request
	hook         func(context.Context)
	clock        time.Time
	manifest     startup.Manifest
	nextID       int64
}

func (f *fixture) Reconcile(context.Context, startup.Anchor) (startup.State, error) {
	return f.startup, nil
}
func (f *fixture) ReadRepository(context.Context) (gh.Repository, error) { return f.repo, nil }
func (f *fixture) ReadIssue(_ context.Context, n int64) (gh.Issue, error) {
	v, ok := f.issues[n]
	if !ok {
		return v, ErrBlocked
	}
	return v, nil
}
func (f *fixture) ReadSource(_ context.Context, r protocol.SourceReference) (gh.SourceBlob, error) {
	v, ok := f.sources[r]
	if !ok {
		return v, ErrBlocked
	}
	v.Bytes = append([]byte(nil), v.Bytes...)
	return v, nil
}
func (f *fixture) ReadBranch(context.Context, string) (gh.Commit, error) { return f.branch, nil }
func (f *fixture) ReadCommit(_ context.Context, s string) (gh.Commit, error) {
	v, ok := f.commits[s]
	if !ok {
		return v, ErrBlocked
	}
	return v, nil
}
func (f *fixture) ReadPullRequest(context.Context, int64) (gh.PullRequest, error) { return f.pr, nil }
func (f *fixture) ListComments(context.Context, gh.Resource) ([]gh.Comment, error) {
	return append([]gh.Comment(nil), f.comments...), nil
}
func (f *fixture) FetchRecord(_ context.Context, r protocol.Reference) (protocol.VerifiedRecord, error) {
	o, ok := f.observations[r.DatabaseID]
	if !ok {
		return protocol.VerifiedRecord{}, ErrBlocked
	}
	return protocol.VerifyGitHubRecord(o, r)
}
func (f *fixture) Run(ctx context.Context, r codexexec.Request) (codexexec.Result, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.requests = append(f.requests, r)
	f.mu.Unlock()
	if f.hook != nil {
		f.hook(ctx)
	}
	return codexexec.Result{ThreadID: fmt.Sprint("real-thread-", n), Output: append([]byte(nil), f.output...), InputTokens: 5, OutputTokens: 5}, nil
}
func (f *fixture) addSource(ref protocol.SourceReference, b []byte) {
	f.sources[ref] = gh.SourceBlob{Reference: ref, BlobSHA: sha("b"), Bytes: append([]byte(nil), b...)}
}
func (f *fixture) record(t *testing.T, r protocol.Record, author protocol.GitHubIdentity, listed bool) protocol.Reference {
	t.Helper()
	body, err := protocol.EncodeComment(r)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := protocol.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	f.nextID++
	id := f.nextID
	h := r.Header()
	url := fmt.Sprintf("https://github.com/octo/pipeline/issues/%d#issuecomment-%d", h.Project.ControlIssue, id)
	if h.Kind == protocol.KindRun || h.Kind == protocol.KindContract {
		url = fmt.Sprintf("https://github.com/octo/pipeline/issues/%d#issuecomment-%d", h.Subject.Issue, id)
	}
	if h.Kind == protocol.KindEvidence || h.Kind == protocol.KindAcceptance {
		url = fmt.Sprintf("https://github.com/octo/pipeline/pull/%d#issuecomment-%d", h.Subject.PullRequest, id)
	}
	ref := protocol.Reference{Kind: "issue_comment", DatabaseID: id, NodeID: fmt.Sprint("C", id), URL: url, Author: author, BodySHA256: protocol.SHA256([]byte(body)), CanonicalSHA256: protocol.SHA256(canonical)}
	f.observations[id] = protocol.GitHubObservation{Project: h.Project, Kind: ref.Kind, DatabaseID: id, NodeID: ref.NodeID, URL: url, Author: author, Body: body}
	if listed {
		f.comments = append(f.comments, gh.Comment{DatabaseID: id, NodeID: ref.NodeID, URL: url, Author: author, Parent: resource("issue", h.Subject.Issue), Content: gh.Content{Body: &body, BodySHA256: ref.BodySHA256, CanonicalSHA256: ref.CanonicalSHA256, IsRecord: true, RecordValid: true}})
	}
	return ref
}
func envelope(kind protocol.Kind, p protocol.Project, subject protocol.Subject, op string) protocol.Envelope {
	return protocol.Envelope{Schema: protocol.Schema, Kind: kind, Project: p, Subject: subject, OperationID: op, Version: 1}
}

func newFixture(t *testing.T) (*fixture, *Engine, Request) {
	t.Helper()
	now := time.Now().UTC()
	project := protocol.Project{Owner: "octo", Repo: "pipeline", ControlIssue: 2}
	repo := startup.Repository{Owner: "octo", Name: "pipeline", DatabaseID: 1000, NodeID: "R"}
	f := &fixture{clock: now, sources: map[protocol.SourceReference]gh.SourceBlob{}, observations: map[int64]protocol.GitHubObservation{}, issues: map[int64]gh.Issue{}, commits: map[string]gh.Commit{}, nextID: 1000}
	input := []byte("Original requirement REQ-1. No author conversation.")
	b := protocol.Budget{MaxAgentRuns: 6, MaxRunSeconds: 60, MaxWallSeconds: 600, MaxDesignRounds: 3, MaxPlanningRounds: 3, MaxImplementationRounds: 3, Currency: "USD"}
	f.manifest = startup.Manifest{Schema: startup.ManifestSchema, Version: 1, Repository: repo, Cycle: "cycle-1", OperationID: "startup-cycle-1", Inputs: []startup.Input{{Kind: "product", Source: sourceRef("docs/input.md", input)}}, Policy: startup.Policy{Goal: "bounded goal", NonGoals: []string{"product implementation"}, StoreInputsAuthorized: true, RemoteWritesAuthorized: true, Roles: startup.Roles{Controller: native(1), Designer: native(2), DesignReviewer: native(3), Developer: native(4), Acceptor: native(5)}, Budget: b, MaxParallelDevelopment: 1, MaxParallelMerge: 1, Completion: startup.Completion{Endpoint: "integrated_code", Criteria: []string{"done"}, RequireIntegration: true, RequireBugGate: true, RequireRemoteCleanup: true, RequireLocalCleanup: true}, TerminalConditions: []string{"completed", "blocked", "budget_exceeded", "failed", "paused", "cancelled"}, Environment: startup.Environment{OperatingSystems: []string{"macos"}, Runtime: "codex-exec", AcceptanceTarget: "fixture", WorkspaceIsolation: "isolated", CredentialIsolation: "controller_only", ExternalUsageLimit: "existing entitlement"}, RequiredGitHubCapabilities: []string{"issues_read", "issues_enabled", "push_permission"}, DesignFiles: []string{"docs/design.json.md"}}}
	manifest := data(t, f.manifest)
	anchor := startup.Anchor{Repository: repo, Manifest: sourceRef("docs/startup.json", manifest)}
	f.addSource(anchor.Manifest, manifest)
	f.addSource(f.manifest.Inputs[0].Source, input)
	control := resource("issue", 2)
	issue := resource("issue", 20)
	f.startup = startup.State{Status: "ready_for_design", Control: &control}
	f.issues[2] = gh.Issue{Resource: control, State: "open", CreatedAt: now.Add(-time.Second).Format(time.RFC3339)}
	f.issues[20] = gh.Issue{Resource: issue, State: "open"}
	f.repo = gh.Repository{Resource: gh.Resource{Kind: "repository", DatabaseID: repo.DatabaseID, NodeID: repo.NodeID}, URL: "https://github.com/octo/pipeline", DefaultBranch: "main"}
	f.branch = gh.Commit{SHA: sha("c"), TreeSHA: sha("d")}
	contract := &protocol.Contract{Envelope: envelope(protocol.KindContract, project, protocol.Subject{Issue: 20}, "design-contract"), DesignBaseline: protocol.DesignBaseline{Issue: 1, PullRequest: 1, MergeCommit: sha("a"), Tree: sha("b"), Approval: "https://github.com/octo/pipeline/pull/1#issuecomment-1"}, Key: "design", Goal: "design only", Scope: []string{"design"}, NonGoals: []string{"product"}, Requirements: []string{"REQ-1"}, Acceptance: []protocol.Criterion{{ID: "AC-1", Statement: "complete design"}}, Verification: []protocol.Check{{Argv: []string{"go", "test"}}}, ImpactSurface: []string{"docs"}, Risks: []protocol.Risk{{Risk: "incomplete", Mitigation: "review"}}, AcceptanceEnvironment: protocol.Environment{OS: []string{"macos"}, Go: "1.22", Network: "agent", Credentials: "isolated", Fixtures: "fake"}}
	ref := f.record(t, contract, native(1), false)
	r := Request{Project: project, Anchor: anchor, Issue: issue, Contract: ref, TargetBranch: "main", ExpectedTargetSHA: f.branch.SHA, Role: protocol.RoleDesigner}
	f.output = data(t, goodDocument())
	e, err := New(f, f, f)
	if err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return f.clock }
	return f, e, r
}
func (f *fixture) fresh(t *testing.T) FreshRunSource {
	t.Helper()
	return func(_ context.Context, r Request, inputHash string) (protocol.Reference, error) {
		var round int64 = 1
		for _, c := range f.comments {
			rec, err := protocol.DecodeComment(*c.Content.Body)
			if err == nil {
				if prior, ok := rec.(*protocol.Run); ok && prior.Role == r.Role {
					round++
				}
			}
		}
		run := &protocol.Run{Envelope: envelope(protocol.KindRun, r.Project, protocol.Subject{Issue: r.Issue.Number}, fmt.Sprintf("%s-%d", r.Role, round)), Role: r.Role, AgentInstance: fmt.Sprintf("host-instance-%s-%d", r.Role, round), Host: "isolated-host", Inputs: []protocol.Reference{r.Contract}, InputSHA256: inputHash, AssignmentGeneration: round, Attempt: round, Budget: f.manifest.Policy.Budget}
		if r.Role == protocol.RoleDesignReviewer && r.AuthorRun != nil {
			run.Inputs = append(run.Inputs, *r.AuthorRun)
		}
		return f.record(t, run, native(1), true), nil
	}
}
