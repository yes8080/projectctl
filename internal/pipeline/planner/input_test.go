package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/design"
	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

type plannerInputFixture struct {
	repo                 gh.Repository
	branch               gh.Commit
	pr                   gh.PullRequest
	issues               map[int64]gh.Issue
	sources              map[protocol.SourceReference]gh.SourceBlob
	observations         map[int64]protocol.GitHubObservation
	comments             []gh.Comment
	commits              map[string]gh.Commit
	clock                time.Time
	manifest             startup.Manifest
	document             design.Document
	baseline             protocol.Baseline
	review               protocol.Acceptance
	evidence             protocol.Evidence
	approval             protocol.Approval
	authorRun, reviewRun protocol.Run
	nextID               int64
	sourceReads          []protocol.SourceReference
	readCount            int
}

func inputFixtureIdentity(id int64) protocol.GitHubIdentity {
	return protocol.GitHubIdentity{ID: id, NodeID: fmt.Sprint("U", id), Login: fmt.Sprint("user", id), Type: "User"}
}
func inputFixtureSHA(c string) string { return strings.Repeat(c, 40) }
func inputFixtureResource(kind string, n int64) gh.Resource {
	return gh.Resource{Kind: kind, Number: n, DatabaseID: 100 + n, NodeID: fmt.Sprint("N", n)}
}
func inputFixtureJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func inputFixtureSource(path string, b []byte) protocol.SourceReference {
	return protocol.SourceReference{Commit: inputFixtureSHA("a"), Path: path, SHA256: protocol.SHA256(b)}
}
func inputFixtureEnvelope(kind protocol.Kind, p protocol.Project, subject protocol.Subject, op string) protocol.Envelope {
	return protocol.Envelope{Schema: protocol.Schema, Kind: kind, Project: p, Subject: subject, OperationID: op, Version: 1}
}

func (f *plannerInputFixture) ReadRepository(context.Context) (gh.Repository, error) {
	f.readCount++
	return f.repo, nil
}
func (f *plannerInputFixture) ReadIssue(_ context.Context, n int64) (gh.Issue, error) {
	v, ok := f.issues[n]
	if !ok {
		return v, ErrBlocked
	}
	return v, nil
}
func (f *plannerInputFixture) ReadSource(_ context.Context, ref protocol.SourceReference) (gh.SourceBlob, error) {
	f.sourceReads = append(f.sourceReads, ref)
	v, ok := f.sources[ref]
	if !ok {
		return v, ErrBlocked
	}
	v.Bytes = append([]byte(nil), v.Bytes...)
	return v, nil
}
func (f *plannerInputFixture) ReadBranch(_ context.Context, name string) (gh.Commit, error) {
	if name != f.repo.DefaultBranch {
		return gh.Commit{}, ErrBlocked
	}
	return f.branch, nil
}
func (f *plannerInputFixture) ReadCommit(_ context.Context, sha string) (gh.Commit, error) {
	v, ok := f.commits[sha]
	if !ok {
		return v, ErrBlocked
	}
	return v, nil
}
func (f *plannerInputFixture) ReadPullRequest(_ context.Context, n int64) (gh.PullRequest, error) {
	if f.pr.Number != n {
		return gh.PullRequest{}, ErrBlocked
	}
	return f.pr, nil
}
func (f *plannerInputFixture) ListComments(context.Context, gh.Resource) ([]gh.Comment, error) {
	return append([]gh.Comment(nil), f.comments...), nil
}
func (f *plannerInputFixture) FetchRecord(_ context.Context, ref protocol.Reference) (protocol.VerifiedRecord, error) {
	o, ok := f.observations[ref.DatabaseID]
	if !ok {
		return protocol.VerifiedRecord{}, ErrBlocked
	}
	return protocol.VerifyGitHubRecord(o, ref)
}
func (f *plannerInputFixture) addSource(ref protocol.SourceReference, b []byte) {
	f.sources[ref] = gh.SourceBlob{Reference: ref, BlobSHA: inputFixtureSHA("b"), Bytes: append([]byte(nil), b...)}
}

func (f *plannerInputFixture) record(t *testing.T, r protocol.Record, author protocol.GitHubIdentity, listed bool) protocol.Reference {
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
		f.comments = append(f.comments, gh.Comment{DatabaseID: id, NodeID: ref.NodeID, URL: url, Author: author, Parent: inputFixtureResource("issue", h.Subject.Issue), Content: gh.Content{Body: &body, BodySHA256: ref.BodySHA256, CanonicalSHA256: ref.CanonicalSHA256, IsRecord: true, RecordValid: true}})
	}
	return ref
}

func (f *plannerInputFixture) fresh(t *testing.T) FreshRunSource {
	return func(_ context.Context, r Request, digest string) (protocol.Reference, error) {
		attempt := int64(len(f.comments) + 1)
		run := protocol.Run{Envelope: inputFixtureEnvelope(protocol.KindRun, r.Project, protocol.Subject{Issue: r.Issue.Number}, fmt.Sprintf("planner-%d", attempt)), Role: protocol.RolePlanner, AgentInstance: fmt.Sprintf("planner-instance-%d", attempt), Host: "test-host", Inputs: []protocol.Reference{r.Baseline}, InputSHA256: digest, AssignmentGeneration: attempt, Attempt: attempt, Budget: f.manifest.Policy.Budget}
		return f.record(t, &run, f.manifest.Policy.Roles.Controller, true), nil
	}
}

func (f *plannerInputFixture) pinManifest(t *testing.T, r *Request) {
	b := inputFixtureJSON(t, f.manifest)
	r.Anchor.Manifest = inputFixtureSource("docs/startup.json", b)
	f.addSource(r.Anchor.Manifest, b)
	control := startup.ControlRecord{Schema: startup.ControlSchema, Kind: "project_control", Repository: f.manifest.Repository, Cycle: f.manifest.Cycle, OperationID: f.manifest.OperationID, Manifest: r.Anchor.Manifest}
	canonical, err := protocol.CanonicalJSON(inputFixtureJSON(t, control))
	if err != nil {
		t.Fatal(err)
	}
	body := startup.ControlMarker + " " + string(canonical)
	v := f.issues[r.Project.ControlIssue]
	v.Content = gh.Content{Body: &body, BodySHA256: protocol.SHA256([]byte(body))}
	f.issues[r.Project.ControlIssue] = v
}

// repinGraph publishes only synthetic in-memory native observations. The model
// cannot use this helper: production Remote deliberately has no write methods.
func (f *plannerInputFixture) repinGraph(t *testing.T, r *Request) {
	policy := inputPolicy(r.Project, f.manifest)
	policySHA, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	f.evidence.Binding.PolicySHA256 = policySHA
	f.evidence.Run = f.record(t, &f.authorRun, f.manifest.Policy.Roles.Controller, false)
	evidence := f.record(t, &f.evidence, f.manifest.Policy.Roles.Designer, false)
	f.review.Run = f.record(t, &f.reviewRun, f.manifest.Policy.Roles.Controller, false)
	f.review.Binding = f.evidence.Binding
	for i := range f.review.Assessments {
		f.review.Assessments[i].Evidence = []protocol.Reference{evidence}
	}
	review := f.record(t, &f.review, f.manifest.Policy.Roles.DesignReviewer, false)
	f.approval.Candidate = review
	f.approval.PolicySHA256 = policySHA
	approval := f.record(t, &f.approval, f.manifest.Policy.Roles.Controller, false)
	f.baseline.Review, f.baseline.Approval, f.baseline.PolicySHA256 = review, approval, policySHA
	r.Baseline = f.record(t, &f.baseline, f.manifest.Policy.Roles.Controller, false)
}

func newPlannerInputFixture(t *testing.T) (*plannerInputFixture, Request) {
	t.Helper()
	project := protocol.Project{Owner: "octo", Repo: "pipeline", ControlIssue: 2}
	repository := startup.Repository{Owner: "octo", Name: "pipeline", DatabaseID: 1000, NodeID: "REPO"}
	f := &plannerInputFixture{clock: time.Now().UTC(), issues: map[int64]gh.Issue{}, sources: map[protocol.SourceReference]gh.SourceBlob{}, observations: map[int64]protocol.GitHubObservation{}, commits: map[string]gh.Commit{}, nextID: 1000}
	budget := protocol.Budget{MaxAgentRuns: 20, MaxRunSeconds: 60, MaxWallSeconds: 3600, MaxDesignRounds: 3, MaxPlanningRounds: 5, MaxImplementationRounds: 3, Currency: "USD"}
	original := []byte("UNAPPROVED_ORIGINAL_INPUT_MUST_NOT_REACH_PLANNER")
	f.manifest = startup.Manifest{Schema: startup.ManifestSchema, Version: 1, Repository: repository, Cycle: "cycle-1", OperationID: "startup-1", Inputs: []startup.Input{{Kind: "product", Source: inputFixtureSource("docs/input.md", original)}}, Policy: startup.Policy{Goal: "approved goal", NonGoals: []string{"unapproved expansion"}, StoreInputsAuthorized: true, RemoteWritesAuthorized: true, Roles: startup.Roles{Controller: inputFixtureIdentity(1), Designer: inputFixtureIdentity(2), DesignReviewer: inputFixtureIdentity(3), Developer: inputFixtureIdentity(4), Acceptor: inputFixtureIdentity(5)}, Budget: budget, MaxParallelDevelopment: 2, MaxParallelMerge: 1, Completion: startup.Completion{Endpoint: "integrated_code", Criteria: []string{"integrated and verified"}, RequireIntegration: true, RequireBugGate: true, RequireRemoteCleanup: true, RequireLocalCleanup: true}, TerminalConditions: []string{"completed", "blocked", "budget_exceeded", "failed", "paused", "cancelled"}, Environment: startup.Environment{OperatingSystems: []string{"macos"}, Runtime: "codex-exec", AcceptanceTarget: "fixture", WorkspaceIsolation: "isolated", CredentialIsolation: "controller_only", ExternalUsageLimit: "existing entitlement"}, RequiredGitHubCapabilities: []string{"issues_read", "issues_enabled", "push_permission"}, DesignFiles: []string{"docs/design.json.md"}}}
	f.document = design.Document{Schema: "projectctl.design-output/v1", Requirements: []design.Requirement{{ID: "REQ-1", Statement: "approved requirement"}, {ID: "integration", Statement: "integrate and verify all components"}}, OpenQuestions: []string{}, Sections: []design.Section{}}
	for _, id := range []string{"boundaries", "workflows", "architecture", "contracts", "data_model", "permissions", "external_dependencies", "technology", "environments", "testing", "integration", "migration", "risks", "completion", "rollback"} {
		f.document.Sections = append(f.document.Sections, design.Section{ID: id, Content: "Approved design for " + id})
	}
	issue := inputFixtureResource("issue", 20)
	r := Request{Project: project, Anchor: startup.Anchor{Repository: repository}, Issue: issue, TargetBranch: "main", ExpectedTargetSHA: inputFixtureSHA("d")}
	f.issues[2] = gh.Issue{Resource: inputFixtureResource("issue", 2), URL: "https://github.com/octo/pipeline/issues/2", State: "open", CreatedAt: f.clock.Add(-time.Second).Format(time.RFC3339), Author: inputFixtureIdentity(1)}
	f.issues[20] = gh.Issue{Resource: issue, URL: "https://github.com/octo/pipeline/issues/20", State: "open", Author: inputFixtureIdentity(1)}
	f.pinManifest(t, &r)
	f.addSource(f.manifest.Inputs[0].Source, original)
	repoID := gh.Resource{Kind: "repository", DatabaseID: repository.DatabaseID, NodeID: repository.NodeID}
	f.repo = gh.Repository{Resource: repoID, URL: "https://github.com/octo/pipeline", DefaultBranch: "main"}
	f.branch = gh.Commit{SHA: r.ExpectedTargetSHA, TreeSHA: inputFixtureSHA("b")}
	document := inputFixtureJSON(t, f.document)
	designRef := inputFixtureSource("docs/design.json.md", document)
	designRef.Commit = inputFixtureSHA("f")
	f.addSource(designRef, document)
	head := designRef
	head.Commit = inputFixtureSHA("e")
	f.addSource(head, document)
	f.pr = gh.PullRequest{Resource: inputFixtureResource("pull_request", 30), URL: "https://github.com/octo/pipeline/pull/30", Repository: repoID, BaseRepository: repoID, Author: inputFixtureIdentity(2), State: "closed", Merged: true, HeadSHA: head.Commit, BaseSHA: inputFixtureSHA("c"), BaseRef: "main", HeadRef: "codex/issue-7", MergeSHA: designRef.Commit}
	f.commits[head.Commit] = gh.Commit{SHA: head.Commit, TreeSHA: inputFixtureSHA("b")}
	f.commits[designRef.Commit] = gh.Commit{SHA: designRef.Commit, TreeSHA: inputFixtureSHA("b")}
	dummy := protocol.Reference{Kind: "issue_comment", DatabaseID: 91, NodeID: "OLD-CONTRACT", URL: "https://github.com/octo/pipeline/issues/7#issuecomment-91", Author: inputFixtureIdentity(1), BodySHA256: protocol.SHA256([]byte("old contract")), CanonicalSHA256: protocol.SHA256([]byte("old canonical contract"))}
	f.authorRun = protocol.Run{Envelope: inputFixtureEnvelope(protocol.KindRun, project, protocol.Subject{Issue: 7}, "designer-run"), Role: protocol.RoleDesigner, AgentInstance: "designer-instance", Host: "trusted-host", Inputs: []protocol.Reference{dummy}, InputSHA256: protocol.SHA256([]byte("frozen original design input")), AssignmentGeneration: 1, Attempt: 1, Budget: budget}
	f.reviewRun = f.authorRun
	f.reviewRun.OperationID = "reviewer-run"
	f.reviewRun.Role = protocol.RoleDesignReviewer
	f.reviewRun.AgentInstance = "reviewer-instance"
	binding := protocol.Binding{PullRequest: 30, HeadSHA: head.Commit, BaseSHA: f.pr.BaseSHA, DesignSHA256: designRef.SHA256, ContractSHA256: dummy.CanonicalSHA256, PolicySHA256: protocol.SHA256([]byte("replaced by repinGraph"))}
	f.evidence = protocol.Evidence{Envelope: inputFixtureEnvelope(protocol.KindEvidence, project, protocol.Subject{PullRequest: 30}, "designer-evidence"), Binding: binding, CheckID: "design-candidate", Argv: []string{"codex", "exec"}, EnvironmentSHA256: protocol.SHA256([]byte("isolated")), Result: "PASS", LogSHA256: designRef.SHA256}
	f.review = protocol.Acceptance{Envelope: inputFixtureEnvelope(protocol.KindAcceptance, project, protocol.Subject{PullRequest: 30}, "design-review"), Binding: binding, Assessments: []protocol.Assessment{}, UnresolvedFindings: []protocol.Reference{}, Decision: "PASS", Reason: "independent review"}
	for _, id := range append([]string{"requirements"}, func() []string {
		var ids []string
		for _, s := range f.document.Sections {
			ids = append(ids, s.ID)
		}
		return ids
	}()...) {
		f.review.Assessments = append(f.review.Assessments, protocol.Assessment{CriterionID: id, Result: "PASS", Reason: "checked"})
	}
	f.approval = protocol.Approval{Envelope: inputFixtureEnvelope(protocol.KindApproval, project, protocol.Subject{PullRequest: 30}, "design-approval"), Decision: "APPROVED", Reason: "approve exact review"}
	f.baseline = protocol.Baseline{Envelope: inputFixtureEnvelope(protocol.KindBaseline, project, protocol.Subject{Issue: 2}, "baseline-1"), Inputs: []protocol.SourceReference{f.manifest.Inputs[0].Source}, Design: designRef}
	f.repinGraph(t, &r)
	return f, r
}

func TestPlannerInputApprovedOnlyColdStart(t *testing.T) {
	f, r := newPlannerInputFixture(t)
	e := &Engine{remote: f, now: func() time.Time { return f.clock }}
	p, err := e.prepare(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.prompt, "UNAPPROVED_ORIGINAL") || !strings.Contains(p.prompt, "approved requirement") || !strings.Contains(p.prompt, `"policy"`) {
		t.Fatal("planner prompt input boundary violated")
	}
	for _, ref := range f.sourceReads {
		if ref == f.manifest.Inputs[0].Source {
			t.Fatal("pre-approval original source was read")
		}
	}
	restarted := &Engine{remote: f, now: e.now}
	again, err := restarted.prepare(context.Background(), r)
	if err != nil || p.digest != again.digest || !reflect.DeepEqual(p.document, again.document) || f.readCount != 2 {
		t.Fatal("restart used local facts or changed fixed input", err)
	}
}

func (f *plannerInputFixture) repinDocument(t *testing.T, r *Request) {
	b := inputFixtureJSON(t, f.document)
	f.baseline.Design.SHA256 = protocol.SHA256(b)
	f.addSource(f.baseline.Design, b)
	head := f.baseline.Design
	head.Commit = f.pr.HeadSHA
	f.addSource(head, b)
	f.evidence.Binding.DesignSHA256 = f.baseline.Design.SHA256
	f.evidence.LogSHA256 = f.baseline.Design.SHA256
	f.repinGraph(t, r)
}

func TestPlannerInputRejectsRemoteCounterexamples(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, *plannerInputFixture, *Request)
	}{
		{"deleted baseline", func(_ *testing.T, f *plannerInputFixture, r *Request) { delete(f.observations, r.Baseline.DatabaseID) }},
		{"edited baseline", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			o := f.observations[r.Baseline.DatabaseID]
			o.Body += " "
			f.observations[r.Baseline.DatabaseID] = o
		}},
		{"recreated baseline", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			o := f.observations[r.Baseline.DatabaseID]
			o.NodeID = "REPLACEMENT"
			f.observations[r.Baseline.DatabaseID] = o
		}},
		{"spoofed native Controller", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			o := f.observations[r.Baseline.DatabaseID]
			o.Author = inputFixtureIdentity(9)
			f.observations[r.Baseline.DatabaseID] = o
		}},
		{"unauthorized baseline publisher", func(t *testing.T, f *plannerInputFixture, r *Request) {
			r.Baseline = f.record(t, &f.baseline, inputFixtureIdentity(2), false)
		}},
		{"wrong baseline subject", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.baseline.Subject = protocol.Subject{Issue: 20}
			r.Baseline = f.record(t, &f.baseline, inputFixtureIdentity(1), false)
		}},
		{"policy digest mismatch", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.baseline.PolicySHA256 = protocol.SHA256([]byte("other"))
			r.Baseline = f.record(t, &f.baseline, inputFixtureIdentity(1), false)
		}},
		{"baseline original input differs", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.baseline.Inputs[0].SHA256 = protocol.SHA256([]byte("different"))
			r.Baseline = f.record(t, &f.baseline, inputFixtureIdentity(1), false)
		}},
		{"mutable manifest", func(_ *testing.T, _ *plannerInputFixture, r *Request) { r.Anchor.Manifest.Commit = "main" }},
		{"encoded source slash", func(_ *testing.T, _ *plannerInputFixture, r *Request) { r.Anchor.Manifest.Path = "docs%2fstartup.json" }},
		{"parent path", func(_ *testing.T, _ *plannerInputFixture, r *Request) { r.Anchor.Manifest.Path = "../startup.json" }},
		{"manifest bytes drift", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			b := f.sources[r.Anchor.Manifest]
			b.Bytes = append(b.Bytes, ' ')
			f.sources[r.Anchor.Manifest] = b
		}},
		{"manifest missing policy", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			b := []byte(`{"schema":"projectctl.startup/v1"}`)
			r.Anchor.Manifest = inputFixtureSource("docs/startup.json", b)
			f.addSource(r.Anchor.Manifest, b)
		}},
		{"manifest unknown field", func(t *testing.T, f *plannerInputFixture, r *Request) {
			b := inputFixtureJSON(t, f.manifest)
			b = append([]byte(`{"extra":true,`), b[1:]...)
			r.Anchor.Manifest = inputFixtureSource("docs/startup.json", b)
			f.addSource(r.Anchor.Manifest, b)
		}},
		{"manifest duplicate field", func(t *testing.T, f *plannerInputFixture, r *Request) {
			b := inputFixtureJSON(t, f.manifest)
			b = append([]byte(`{"schema":"projectctl.startup/v1",`), b[1:]...)
			r.Anchor.Manifest = inputFixtureSource("docs/startup.json", b)
			f.addSource(r.Anchor.Manifest, b)
		}},
		{"repository ID replaced", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.repo.DatabaseID++ }},
		{"default branch changed", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.repo.DefaultBranch = "other" }},
		{"branch advanced after pin", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.branch.SHA = inputFixtureSHA("9") }},
		{"issue replaced", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			v := f.issues[r.Issue.Number]
			v.DatabaseID++
			f.issues[r.Issue.Number] = v
		}},
		{"issue closed", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			v := f.issues[r.Issue.Number]
			v.State = "closed"
			f.issues[r.Issue.Number] = v
		}},
		{"control closed", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			v := f.issues[r.Project.ControlIssue]
			v.State = "closed"
			f.issues[r.Project.ControlIssue] = v
		}},
		{"control author changed", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			v := f.issues[r.Project.ControlIssue]
			v.Author = inputFixtureIdentity(8)
			f.issues[r.Project.ControlIssue] = v
		}},
		{"control body edited", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			v := f.issues[r.Project.ControlIssue]
			body := *v.Content.Body + " "
			v.Content.Body = &body
			v.Content.BodySHA256 = protocol.SHA256([]byte(body))
			f.issues[r.Project.ControlIssue] = v
		}},
		{"future wall origin", func(_ *testing.T, f *plannerInputFixture, r *Request) {
			v := f.issues[r.Project.ControlIssue]
			v.CreatedAt = f.clock.Add(time.Hour).Format(time.RFC3339)
			f.issues[r.Project.ControlIssue] = v
		}},
		{"wall budget expired", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.clock = f.clock.Add(2 * time.Hour) }},
		{"missing design", func(_ *testing.T, f *plannerInputFixture, _ *Request) { delete(f.sources, f.baseline.Design) }},
		{"design bytes drift", func(_ *testing.T, f *plannerInputFixture, _ *Request) {
			b := f.sources[f.baseline.Design]
			b.Bytes = append(b.Bytes, ' ')
			f.sources[f.baseline.Design] = b
		}},
		{"unapproved design revision", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.document.Sections[0].Content = "new unapproved revision"
			b := inputFixtureJSON(t, f.document)
			f.baseline.Design.SHA256 = protocol.SHA256(b)
			f.addSource(f.baseline.Design, b)
			r.Baseline = f.record(t, &f.baseline, inputFixtureIdentity(1), false)
		}},
		{"open design question", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.document.OpenQuestions = []string{"undecided permission"}
			f.repinDocument(t, r)
		}},
		{"missing integration requirement", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.document.Requirements = f.document.Requirements[:1]
			f.repinDocument(t, r)
		}},
		{"deleted review", func(_ *testing.T, f *plannerInputFixture, _ *Request) {
			delete(f.observations, f.baseline.Review.DatabaseID)
		}},
		{"deleted approval", func(_ *testing.T, f *plannerInputFixture, _ *Request) {
			delete(f.observations, f.baseline.Approval.DatabaseID)
		}},
		{"deleted evidence", func(_ *testing.T, f *plannerInputFixture, _ *Request) {
			delete(f.observations, f.review.Assessments[0].Evidence[0].DatabaseID)
		}},
		{"deleted author Run", func(_ *testing.T, f *plannerInputFixture, _ *Request) {
			delete(f.observations, f.evidence.Run.DatabaseID)
		}},
		{"deleted reviewer Run", func(_ *testing.T, f *plannerInputFixture, _ *Request) {
			delete(f.observations, f.review.Run.DatabaseID)
		}},
		{"same agent instance", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.reviewRun.AgentInstance = f.authorRun.AgentInstance
			f.repinGraph(t, r)
		}},
		{"operation reused by different object", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.reviewRun.OperationID = f.authorRun.OperationID
			f.repinGraph(t, r)
		}},
		{"incomplete assessments", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.review.Assessments = f.review.Assessments[:len(f.review.Assessments)-1]
			f.repinGraph(t, r)
		}},
		{"evidence wrong result", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.evidence.Result = "FAIL"
			f.evidence.ExitCode = 1
			f.repinGraph(t, r)
		}},
		{"evidence wrong design log", func(t *testing.T, f *plannerInputFixture, r *Request) {
			f.evidence.LogSHA256 = protocol.SHA256([]byte("other"))
			f.repinGraph(t, r)
		}},
		{"PR unmerged", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.pr.Merged = false }},
		{"PR draft", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.pr.Draft = true }},
		{"PR different repository", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.pr.Repository.DatabaseID++ }},
		{"PR other base", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.pr.BaseSHA = inputFixtureSHA("9") }},
		{"PR other head", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.pr.HeadSHA = inputFixtureSHA("9") }},
		{"PR other merge", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.pr.MergeSHA = inputFixtureSHA("9") }},
		{"PR wrong author", func(_ *testing.T, f *plannerInputFixture, _ *Request) { f.pr.Author = inputFixtureIdentity(3) }},
		{"merge tree mismatch", func(_ *testing.T, f *plannerInputFixture, _ *Request) {
			v := f.commits[f.pr.MergeSHA]
			v.TreeSHA = inputFixtureSHA("9")
			f.commits[f.pr.MergeSHA] = v
		}},
		{"head source removed", func(_ *testing.T, f *plannerInputFixture, _ *Request) {
			ref := f.baseline.Design
			ref.Commit = f.pr.HeadSHA
			delete(f.sources, ref)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, r := newPlannerInputFixture(t)
			e := &Engine{remote: f, now: func() time.Time { return f.clock }}
			if _, err := e.prepare(context.Background(), r); err != nil {
				t.Fatalf("baseline fixture failed: %v", err)
			}
			test.change(t, f, &r)
			if _, err := e.prepare(context.Background(), r); err == nil {
				t.Fatal("counterexample accepted after a successful prior read")
			}
			restarted := &Engine{remote: f, now: e.now}
			if _, err := restarted.prepare(context.Background(), r); err == nil {
				t.Fatal("restart resurrected invalid remote graph")
			}
		})
	}
}

func (f *plannerInputFixture) pinReviewSelection(t *testing.T, r *Request) {
	review := f.record(t, &f.review, f.manifest.Policy.Roles.DesignReviewer, false)
	f.approval.Candidate = review
	approval := f.record(t, &f.approval, f.manifest.Policy.Roles.Controller, false)
	f.baseline.Review, f.baseline.Approval = review, approval
	r.Baseline = f.record(t, &f.baseline, f.manifest.Policy.Roles.Controller, false)
}

func TestPlannerInputFetchesEveryEvidenceEdge(t *testing.T) {
	f, r := newPlannerInputFixture(t)
	other := f.evidence
	other.OperationID = "second-evidence"
	other.Binding.ContractSHA256 = protocol.SHA256([]byte("wrong contract"))
	ref := f.record(t, &other, f.manifest.Policy.Roles.Designer, false)
	i := len(f.review.Assessments) - 1
	f.review.Assessments[i].Evidence = append(f.review.Assessments[i].Evidence, ref)
	f.pinReviewSelection(t, &r)
	e := &Engine{remote: f, now: func() time.Time { return f.clock }}
	if _, err := e.prepare(context.Background(), r); err == nil {
		t.Fatal("only first evidence edge was checked")
	}
}

func TestPlannerInputNeverSelectsUnapprovedRevision(t *testing.T) {
	f, r := newPlannerInputFixture(t)
	e := &Engine{remote: f, now: func() time.Time { return f.clock }}
	first, err := e.prepare(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	newReview := f.review
	newReview.OperationID = "newer-unapproved-review"
	newReview.Reason = "newer but not approved"
	newRef := f.record(t, &newReview, f.manifest.Policy.Roles.DesignReviewer, false)
	again, err := e.prepare(context.Background(), r)
	if err != nil || again.digest != first.digest {
		t.Fatal("unreferenced newer candidate displaced exact approval", err)
	}
	f.baseline.Review = newRef
	r.Baseline = f.record(t, &f.baseline, f.manifest.Policy.Roles.Controller, false)
	if _, err := e.prepare(context.Background(), r); err == nil {
		t.Fatal("new baseline self-assertion bypassed exact approval")
	}
}

func TestPlannerInputDigestPinsFullRequestManifestAndWallOrigin(t *testing.T) {
	for _, mode := range []string{"target pin", "raw manifest", "wall origin", "request reference display metadata"} {
		t.Run(mode, func(t *testing.T) {
			f, r := newPlannerInputFixture(t)
			e := &Engine{remote: f, now: func() time.Time { return f.clock }}
			first, err := e.prepare(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "target pin":
				f.branch.SHA = inputFixtureSHA("9")
				r.ExpectedTargetSHA = f.branch.SHA
			case "raw manifest":
				old := r.Anchor.Manifest
				b := append([]byte("\n"), f.sources[old].Bytes...)
				r.Anchor.Manifest.SHA256 = protocol.SHA256(b)
				f.addSource(r.Anchor.Manifest, b)
				v := f.issues[r.Project.ControlIssue]
				body := strings.ReplaceAll(*v.Content.Body, old.SHA256, r.Anchor.Manifest.SHA256)
				v.Content.Body = &body
				v.Content.BodySHA256 = protocol.SHA256([]byte(body))
				f.issues[r.Project.ControlIssue] = v
			case "wall origin":
				v := f.issues[r.Project.ControlIssue]
				v.CreatedAt = f.clock.Add(-10 * time.Second).Format(time.RFC3339)
				f.issues[r.Project.ControlIssue] = v
			case "request reference display metadata":
				r.Baseline.Author.Login = "renamed-display-login"
			}
			after, err := e.prepare(context.Background(), r)
			if err != nil {
				t.Fatal("legitimate new pin rejected", err)
			}
			if first.digest == after.digest {
				t.Fatal("exact input identity omitted changed pin")
			}
		})
	}
}
