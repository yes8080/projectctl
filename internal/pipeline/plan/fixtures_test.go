package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/design"
	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/planner"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

type publicationFixture struct {
	mu                                    sync.Mutex
	server                                *httptest.Server
	issues, milestones                    map[int64]map[string]any
	comments                              map[int64][]map[string]any
	dependencies                          map[int64][]int64
	sources                               map[protocol.SourceReference]gh.SourceBlob
	commits                               map[string]gh.Commit
	pr                                    gh.PullRequest
	inputs                                planner.Inputs
	candidate                             planner.Candidate
	policy                                protocol.Policy
	request                               Request
	posts                                 map[string]int
	events                                []string
	hidden                                map[string]bool
	allocations                           map[string]bool
	faultAction                           gh.Action
	faultRecordKind                       protocol.Kind
	faultIntent, faultUsed                bool
	duplicateAction                       gh.Action
	nextIssue, nextMilestone, nextComment int64
	inputReads                            int
}

type publicationRemote struct {
	*gh.Gateway
	f *publicationFixture
}

func (r *publicationRemote) ReadSource(_ context.Context, ref protocol.SourceReference) (gh.SourceBlob, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	v, ok := r.f.sources[ref]
	if !ok {
		return v, gh.ErrUnavailable
	}
	v.Bytes = append([]byte(nil), v.Bytes...)
	return v, nil
}
func (r *publicationRemote) ReadBranch(context.Context, string) (gh.Commit, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return r.f.commits[r.f.request.Input.ExpectedTargetSHA], nil
}
func (r *publicationRemote) ReadCommit(_ context.Context, sha string) (gh.Commit, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	v, ok := r.f.commits[sha]
	if !ok {
		return v, gh.ErrUnavailable
	}
	return v, nil
}
func (r *publicationRemote) ReadPullRequest(_ context.Context, n int64) (gh.PullRequest, error) {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	if n != r.f.pr.Number {
		return gh.PullRequest{}, gh.ErrUnavailable
	}
	return r.f.pr, nil
}

func publicationIdentity(id int64) protocol.GitHubIdentity {
	return protocol.GitHubIdentity{ID: id, NodeID: fmt.Sprint("U_", id), Login: fmt.Sprint("user", id), Type: "User"}
}
func publicationProject() protocol.Project {
	return protocol.Project{Owner: "octo", Repo: "pipeline", ControlIssue: 2}
}
func publicationSHA(c string) string { return strings.Repeat(c, 40) }
func publicationResource(kind string, n int64) gh.Resource {
	if kind == "repository" {
		return gh.Resource{Kind: kind, DatabaseID: 99, NodeID: "R_99"}
	}
	id, node := int64(1000)+n, fmt.Sprint("I_", n)
	if kind == "milestone" {
		id, node = 2000+n, fmt.Sprint("M_", n)
	}
	return gh.Resource{Kind: kind, Number: n, DatabaseID: id, NodeID: node}
}
func publicationJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func publicationEnvelope(kind protocol.Kind, subject protocol.Subject, op string) protocol.Envelope {
	return protocol.Envelope{Schema: protocol.Schema, Kind: kind, Project: publicationProject(), Subject: subject, OperationID: op, Version: 1}
}
func publicationIssue(n int64, body string) map[string]any {
	return map[string]any{"id": 1000 + n, "node_id": fmt.Sprint("I_", n), "number": n, "url": fmt.Sprintf("https://api.github.com/repos/octo/pipeline/issues/%d", n), "html_url": fmt.Sprintf("https://github.com/octo/pipeline/issues/%d", n), "repository_url": "https://api.github.com/repos/octo/pipeline", "title": "fixture issue", "body": body, "state": "open", "created_at": time.Now().UTC().Format(time.RFC3339), "user": publicationIdentity(1), "milestone": nil}
}
func publicationMilestone(n int64, body string) map[string]any {
	return map[string]any{"id": 2000 + n, "node_id": fmt.Sprint("M_", n), "number": n, "url": fmt.Sprintf("https://api.github.com/repos/octo/pipeline/milestones/%d", n), "html_url": fmt.Sprintf("https://github.com/octo/pipeline/milestone/%d", n), "title": "fixture milestone", "description": body, "state": "open", "creator": publicationIdentity(1)}
}
func publicationComment(n, id int64, body string, author protocol.GitHubIdentity) map[string]any {
	return map[string]any{"id": id, "node_id": fmt.Sprint("IC_", id), "url": fmt.Sprintf("https://api.github.com/repos/octo/pipeline/issues/comments/%d", id), "html_url": fmt.Sprintf("https://github.com/octo/pipeline/issues/%d#issuecomment-%d", n, id), "issue_url": fmt.Sprintf("https://api.github.com/repos/octo/pipeline/issues/%d", n), "body": body, "user": author}
}
func publicationSorted(m map[int64]map[string]any) []int64 {
	out := []int64{}
	for n := range m {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (f *publicationFixture) gateway(t *testing.T) *publicationRemote {
	t.Helper()
	g, err := gh.New(gh.Config{Project: publicationProject(), BaseURL: f.server.URL, AllowLoopbackTestServer: true, HTTPClient: f.server.Client(), Publisher: publicationIdentity(1), Token: func(context.Context) (string, error) { return "synthetic-publication-token", nil }, Authorize: func(_ context.Context, intent gh.Intent, author protocol.GitHubIdentity) error {
		if intent.Project != publicationProject() || intent.Control != publicationResource("issue", 2) || author.ID != 1 {
			return gh.ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &publicationRemote{Gateway: g, f: f}
}
func (f *publicationFixture) engine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(f.gateway(t), f)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func (f *publicationFixture) Read(_ context.Context, _ planner.Request) (planner.Inputs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputReads++
	b, err := json.Marshal(f.inputs)
	if err != nil {
		return planner.Inputs{}, err
	}
	var value planner.Inputs
	err = json.Unmarshal(b, &value)
	return value, err
}
func (f *publicationFixture) source(t *testing.T, path string, value any) protocol.SourceReference {
	b := publicationJSON(t, value)
	ref := protocol.SourceReference{Commit: publicationSHA("a"), Path: path, SHA256: protocol.SHA256(b)}
	f.sources[ref] = gh.SourceBlob{Reference: ref, BlobSHA: publicationSHA("b"), Bytes: b}
	return ref
}

func (f *publicationFixture) record(t *testing.T, r protocol.Record, author protocol.GitHubIdentity, parent int64) protocol.Reference {
	t.Helper()
	body, err := protocol.EncodeComment(r)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := protocol.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextComment++
	id := f.nextComment
	native := publicationComment(parent, id, body, author)
	if r.Header().Kind == protocol.KindEvidence || r.Header().Kind == protocol.KindAcceptance {
		native["html_url"] = fmt.Sprintf("https://github.com/octo/pipeline/pull/%d#issuecomment-%d", parent, id)
	}
	f.comments[parent] = append(f.comments[parent], native)
	return protocol.Reference{Kind: "issue_comment", DatabaseID: id, NodeID: fmt.Sprint("IC_", id), URL: native["html_url"].(string), Author: author, BodySHA256: protocol.SHA256([]byte(body)), CanonicalSHA256: protocol.SHA256(canonical)}
}

func (f *publicationFixture) freshAllocation(t *testing.T) gh.FreshAllocationSource {
	t.Helper()
	var used atomic.Bool
	return func(ctx context.Context, intent gh.Intent, author protocol.GitHubIdentity) (protocol.VerifiedRecord, error) {
		if !used.CompareAndSwap(false, true) {
			return protocol.VerifiedRecord{}, gh.ErrDenied
		}
		f.mu.Lock()
		if f.allocations[intent.OperationID] {
			f.mu.Unlock()
			return protocol.VerifiedRecord{}, gh.ErrDenied
		}
		f.allocations[intent.OperationID] = true
		f.mu.Unlock()
		run := &protocol.Run{Envelope: publicationEnvelope(protocol.KindRun, protocol.Subject{Issue: 2}, "allocation-"+intent.OperationID), Role: protocol.RolePublisher, AgentInstance: "publisher-" + intent.OperationID, Host: "fixture-controller", Inputs: []protocol.Reference{f.request.Input.Baseline}, InputSHA256: intent.RequestSHA256, AssignmentGeneration: 1, Attempt: 1, Budget: f.inputs.Manifest.Policy.Budget}
		ref := f.record(t, run, author, 2)
		return f.gateway(t).FetchRecord(ctx, ref)
	}
}

func (f *publicationFixture) handle(t *testing.T, w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer synthetic-publication-token" {
		t.Error("missing synthetic fixture credential")
	}
	p := strings.TrimPrefix(r.URL.Path, "/repos/octo/pipeline")
	f.events = append(f.events, r.Method+" "+p)
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Error(err)
		}
	}
	if r.Method == http.MethodGet {
		switch {
		case r.URL.Path == "/user":
			write(publicationIdentity(1))
		case p == "":
			write(map[string]any{"id": int64(99), "node_id": "R_99", "full_name": "octo/pipeline", "html_url": "https://github.com/octo/pipeline", "default_branch": "main"})
		case p == "/issues":
			out := []any{}
			for _, n := range publicationSorted(f.issues) {
				if !f.hidden[fmt.Sprint("issue:", n)] {
					out = append(out, f.issues[n])
				}
			}
			write(out)
		case p == "/milestones":
			out := []any{}
			for _, n := range publicationSorted(f.milestones) {
				if !f.hidden[fmt.Sprint("milestone:", n)] {
					out = append(out, f.milestones[n])
				}
			}
			write(out)
		case strings.Contains(p, "/dependencies/"):
			parts := strings.Split(p, "/")
			n, _ := strconv.ParseInt(parts[2], 10, 64)
			direction := parts[len(parts)-1]
			out := []any{}
			if direction == "blocked_by" {
				for _, from := range f.dependencies[n] {
					if !f.hidden[fmt.Sprintf("edge:%d:%d", n, from)] {
						out = append(out, f.issues[from])
					}
				}
			} else {
				for target, deps := range f.dependencies {
					for _, from := range deps {
						if from == n && !f.hidden[fmt.Sprintf("edge:%d:%d", target, from)] {
							out = append(out, f.issues[target])
						}
					}
				}
			}
			write(out)
		case strings.HasPrefix(p, "/issues/comments/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/issues/comments/"), 10, 64)
			for _, list := range f.comments {
				for _, c := range list {
					if c["id"] == id && !f.hidden[fmt.Sprint("comment:", id)] {
						write(c)
						return
					}
				}
			}
			http.NotFound(w, r)
		case strings.HasSuffix(p, "/comments"):
			n, _ := strconv.ParseInt(strings.Split(p, "/")[2], 10, 64)
			out := []any{}
			for _, c := range f.comments[n] {
				if !f.hidden[fmt.Sprint("comment:", c["id"])] {
					out = append(out, c)
				}
			}
			write(out)
		case strings.HasPrefix(p, "/issues/"):
			n, _ := strconv.ParseInt(strings.TrimPrefix(p, "/issues/"), 10, 64)
			if item, ok := f.issues[n]; ok && !f.hidden[fmt.Sprint("issue:", n)] {
				write(item)
			} else {
				http.NotFound(w, r)
			}
		case strings.HasPrefix(p, "/milestones/"):
			n, _ := strconv.ParseInt(strings.TrimPrefix(p, "/milestones/"), 10, 64)
			if item, ok := f.milestones[n]; ok && !f.hidden[fmt.Sprint("milestone:", n)] {
				write(item)
			} else {
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "fixture cleanup denied", http.StatusForbidden)
		return
	}
	f.posts[p]++
	var payload struct {
		Title       string `json:"title"`
		Body        string `json:"body"`
		Description string `json:"description"`
		Milestone   *int64 `json:"milestone"`
		IssueID     int64  `json:"issue_id"`
	}
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		http.Error(w, "bad fixture request", 400)
		return
	}
	var action gh.Action
	intent := strings.HasPrefix(payload.Body, "<!-- projectctl:operation:v1 -->")
	switch {
	case p == "/issues":
		action = gh.CreateIssue
	case p == "/milestones":
		action = gh.CreateMilestone
	case strings.Contains(p, "/dependencies/"):
		action = gh.AddDependency
	case strings.HasSuffix(p, "/comments"):
		action = gh.PublishRecord
	default:
		http.NotFound(w, r)
		return
	}
	count := 1
	if action == f.duplicateAction && !intent {
		count = 2
	}
	var value any
	var key string
	for i := 0; i < count; i++ {
		switch action {
		case gh.CreateIssue:
			f.nextIssue++
			n := f.nextIssue
			v := publicationIssue(n, payload.Body)
			v["title"] = payload.Title
			if payload.Milestone != nil {
				v["milestone"] = f.milestones[*payload.Milestone]
			}
			f.issues[n] = v
			value, key = v, fmt.Sprint("issue:", n)
		case gh.CreateMilestone:
			f.nextMilestone++
			n := f.nextMilestone
			v := publicationMilestone(n, payload.Description)
			v["title"] = payload.Title
			f.milestones[n] = v
			value, key = v, fmt.Sprint("milestone:", n)
		case gh.PublishRecord:
			n, _ := strconv.ParseInt(strings.Split(p, "/")[2], 10, 64)
			f.nextComment++
			v := publicationComment(n, f.nextComment, payload.Body, publicationIdentity(1))
			f.comments[n] = append(f.comments[n], v)
			value, key = v, fmt.Sprint("comment:", f.nextComment)
		case gh.AddDependency:
			n, _ := strconv.ParseInt(strings.Split(p, "/")[2], 10, 64)
			var related int64
			for number, item := range f.issues {
				if item["id"] == payload.IssueID {
					related = number
				}
			}
			if related == 0 {
				http.Error(w, "unknown native dependency ID", 400)
				return
			}
			f.dependencies[n] = append(f.dependencies[n], related)
			value, key = f.issues[related], fmt.Sprintf("edge:%d:%d", n, related)
		}
	}
	var recordKind protocol.Kind
	if action == gh.PublishRecord && !intent {
		if record, err := protocol.DecodeComment(payload.Body); err == nil {
			recordKind = record.Header().Kind
		}
	}
	if !f.faultUsed && ((f.faultIntent && intent) || (!intent && action == f.faultAction && (f.faultRecordKind == "" || f.faultRecordKind == recordKind))) {
		f.faultUsed = true
		f.hidden[key] = true
		http.Error(w, "accepted but response lost", http.StatusGatewayTimeout)
		return
	}
	w.WriteHeader(http.StatusCreated)
	write(value)
}

func newPublicationFixture(t *testing.T) (*publicationFixture, Request) {
	t.Helper()
	f := &publicationFixture{issues: map[int64]map[string]any{}, milestones: map[int64]map[string]any{}, comments: map[int64][]map[string]any{}, dependencies: map[int64][]int64{}, sources: map[protocol.SourceReference]gh.SourceBlob{}, commits: map[string]gh.Commit{}, posts: map[string]int{}, hidden: map[string]bool{}, allocations: map[string]bool{}, nextIssue: 20, nextMilestone: 10, nextComment: 100}
	f.issues[2] = publicationIssue(2, "temporary fixture control")
	f.issues[7] = publicationIssue(7, "historical design issue")
	f.issues[8] = publicationIssue(8, "historical planning issue")
	f.milestones[1] = publicationMilestone(1, "pre-existing fixture milestone")
	budget := protocol.Budget{MaxAgentRuns: 20, MaxRunSeconds: 60, MaxWallSeconds: 3600, MaxDesignRounds: 3, MaxPlanningRounds: 5, MaxImplementationRounds: 3, Currency: "USD"}
	env := startup.Environment{OperatingSystems: []string{"macos"}, Runtime: "codex-exec", AcceptanceTarget: "publication-fixture", WorkspaceIsolation: "isolated", CredentialIsolation: "controller_only", ExternalUsageLimit: "bounded"}
	original := protocol.SourceReference{Commit: publicationSHA("a"), Path: "docs/product.md", SHA256: protocol.SHA256([]byte("original"))}
	manifest := startup.Manifest{Schema: startup.ManifestSchema, Version: 1, Repository: startup.Repository{Owner: "octo", Name: "pipeline", DatabaseID: 99, NodeID: "R_99"}, Cycle: "cycle-1", OperationID: "startup-1", Inputs: []startup.Input{{Kind: "product", Source: original}}, Policy: startup.Policy{
		Goal: "publish a bounded approved plan", NonGoals: []string{"unapproved scope"}, StoreInputsAuthorized: true, RemoteWritesAuthorized: true,
		Roles:  startup.Roles{Controller: publicationIdentity(1), Designer: publicationIdentity(4), DesignReviewer: publicationIdentity(5), Developer: publicationIdentity(6), Acceptor: publicationIdentity(7)},
		Budget: budget, MaxParallelDevelopment: 2, MaxParallelMerge: 1, Environment: env,
		Completion:                 startup.Completion{Endpoint: "integrated_code", Criteria: []string{"all integrated"}, RequireIntegration: true, RequireBugGate: true, RequireRemoteCleanup: true, RequireLocalCleanup: true},
		TerminalConditions:         []string{"completed", "blocked", "budget_exceeded", "failed", "paused", "cancelled"},
		RequiredGitHubCapabilities: []string{"issues_read", "issues_enabled", "push_permission"}, DesignFiles: []string{"docs/design.md"},
	}}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("invalid trusted manifest fixture: %v", err)
	}
	document := design.Document{Schema: "projectctl.design-output/v1", Requirements: []design.Requirement{{ID: "REQ-A", Statement: "Implement A"}, {ID: "integration", Statement: "Integrate A"}}, Sections: []design.Section{}, OpenQuestions: []string{}}
	for _, id := range []string{"boundaries", "workflows", "architecture", "contracts", "data_model", "permissions", "external_dependencies", "technology", "environments", "testing", "integration", "migration", "risks", "completion", "rollback"} {
		document.Sections = append(document.Sections, design.Section{ID: id, Content: "approved " + id})
	}
	if _, err := design.DecodeDocument(publicationJSON(t, document)); err != nil {
		t.Fatalf("invalid trusted design fixture: %v", err)
	}
	f.inputs = planner.Inputs{Design: document, Manifest: manifest, SHA256: protocol.SHA256([]byte("approved planner input"))}
	f.candidate = planner.Candidate{Schema: planner.CandidateSchema, Contracts: []planner.Slice{}, OpenDecisions: []string{}}
	for i, req := range f.inputs.Design.Requirements {
		kind, binding := "implementation", "none"
		deps := []string{}
		if req.ID == "integration" {
			kind, binding = "integration", "exact_source_artifact_environment"
			deps = []string{"alpha"}
		}
		key := "alpha"
		if i == 1 {
			key = "integration"
		}
		f.candidate.Contracts = append(f.candidate.Contracts, planner.Slice{Key: key, Kind: kind, Goal: req.Statement, Scope: []string{req.Statement}, NonGoals: []string{"unapproved scope"}, Requirements: []string{req.ID}, Acceptance: []planner.Criterion{{ID: "AC-1", Statement: "verify requirement", Requirements: []string{req.ID}, Checks: []string{"test"}}}, Verification: []protocol.Check{{ID: "test", Argv: []string{"go", "test", "./..."}}}, AcceptanceEnvironment: protocol.Environment{OS: []string{"macos"}, Go: "1.22", Network: "mocked", Credentials: "synthetic", Fixtures: "publication-fixture"}, ImpactSurface: []string{"internal/product"}, Risks: []protocol.Risk{{Risk: "regression", Mitigation: "tests"}}, Dependencies: deps, CandidateBinding: binding})
	}
	f.policy = protocol.Policy{Schema: protocol.PolicySchema, Version: 1, Project: publicationProject(), Grants: []protocol.Grant{{Principal: publicationIdentity(1), Roles: []protocol.Role{protocol.RoleController}}, {Principal: publicationIdentity(2), Roles: []protocol.Role{protocol.RolePlanner}}, {Principal: publicationIdentity(3), Roles: []protocol.Role{protocol.RolePlanReviewer}}}}
	designSource := protocol.SourceReference{Commit: publicationSHA("f"), Path: "docs/design.md", SHA256: protocol.SHA256(publicationJSON(t, document))}
	// The bootstrap navigation link is a real native comment, not a forged
	// protocol reference. All strong protocol references below form a complete DAG.
	bootstrap := publicationComment(30, 90, "historical bootstrap design approved", publicationIdentity(1))
	bootstrap["html_url"] = "https://github.com/octo/pipeline/pull/30#issuecomment-90"
	f.comments[30] = append(f.comments[30], bootstrap)
	designContract := &protocol.Contract{Envelope: publicationEnvelope(protocol.KindContract, protocol.Subject{Issue: 7}, "historical-design-contract"), DesignBaseline: protocol.DesignBaseline{Issue: 7, PullRequest: 30, MergeCommit: publicationSHA("f"), Tree: publicationSHA("b"), Approval: bootstrap["html_url"].(string)}, Key: "design", Goal: "produce approved design", Scope: []string{"fixed design"}, NonGoals: []string{"implementation"}, Requirements: []string{"REQ-A", "integration"}, Acceptance: []protocol.Criterion{{ID: "design", Statement: "complete design review"}}, Verification: []protocol.Check{{ID: "design", Argv: []string{"design-review"}}}, ImpactSurface: []string{"docs/design.md"}, Risks: []protocol.Risk{{Risk: "incomplete design", Mitigation: "independent review"}}, AcceptanceEnvironment: protocol.Environment{OS: []string{"macos"}, Go: "1.22", Network: "mocked", Credentials: "synthetic", Fixtures: "publication-fixture"}}
	contractRef := f.record(t, designContract, publicationIdentity(1), 7)
	baseline := &protocol.Baseline{Envelope: publicationEnvelope(protocol.KindBaseline, protocol.Subject{Issue: 2}, "baseline"), Inputs: []protocol.SourceReference{original}, Design: designSource, PolicySHA256: protocol.SHA256([]byte("design policy"))}
	binding := protocol.Binding{PullRequest: 30, HeadSHA: publicationSHA("e"), BaseSHA: publicationSHA("c"), DesignSHA256: designSource.SHA256, ContractSHA256: contractRef.CanonicalSHA256, PolicySHA256: baseline.PolicySHA256}
	designerRun := &protocol.Run{Envelope: publicationEnvelope(protocol.KindRun, protocol.Subject{Issue: 7}, "designer-run"), Role: protocol.RoleDesigner, AgentInstance: "designer", Host: "fixture-host", Inputs: []protocol.Reference{contractRef}, InputSHA256: protocol.SHA256([]byte("original design input")), AssignmentGeneration: 1, Attempt: 1, Budget: budget}
	designerRunRef := f.record(t, designerRun, publicationIdentity(1), 7)
	evidence := &protocol.Evidence{Envelope: publicationEnvelope(protocol.KindEvidence, protocol.Subject{PullRequest: 30}, "design-evidence"), Binding: binding, Run: designerRunRef, CheckID: "design", Argv: []string{"design-review"}, EnvironmentSHA256: protocol.SHA256([]byte("design environment")), Result: "PASS", ExitCode: 0, LogSHA256: protocol.SHA256([]byte("design passed"))}
	evidenceRef := f.record(t, evidence, publicationIdentity(4), 30)
	designRun := &protocol.Run{Envelope: publicationEnvelope(protocol.KindRun, protocol.Subject{Issue: 7}, "design-review-run"), Role: protocol.RoleDesignReviewer, AgentInstance: "design-reviewer", Host: "fixture-host", Inputs: []protocol.Reference{contractRef, evidenceRef}, InputSHA256: protocol.SHA256([]byte("original design input")), AssignmentGeneration: 1, Attempt: 1, Budget: budget}
	designRunRef := f.record(t, designRun, publicationIdentity(1), 7)
	designReview := &protocol.Acceptance{Envelope: publicationEnvelope(protocol.KindAcceptance, protocol.Subject{PullRequest: 30}, "design-review"), Binding: binding, Run: designRunRef, Assessments: []protocol.Assessment{{CriterionID: "requirements", Result: "PASS", Reason: "historical verified design", Evidence: []protocol.Reference{evidenceRef}}}, UnresolvedFindings: []protocol.Reference{}, Decision: "PASS", Reason: "verified by injected input reader"}
	for _, section := range document.Sections {
		designReview.Assessments = append(designReview.Assessments, protocol.Assessment{CriterionID: section.ID, Result: "PASS", Reason: "historical verified design", Evidence: []protocol.Reference{evidenceRef}})
	}
	baseline.Review = f.record(t, designReview, publicationIdentity(5), 30)
	designApproval := &protocol.Approval{Envelope: publicationEnvelope(protocol.KindApproval, protocol.Subject{PullRequest: 30}, "design-approval"), Candidate: baseline.Review, PolicySHA256: baseline.PolicySHA256, Decision: "APPROVED", Reason: "historical exact design approval"}
	baseline.Approval = f.record(t, designApproval, publicationIdentity(1), 2)
	baselineRef := f.record(t, baseline, publicationIdentity(1), 2)
	r := Request{Input: planner.Request{Project: publicationProject(), Anchor: startup.Anchor{Repository: manifest.Repository}, Baseline: baselineRef, Issue: publicationResource("issue", 8), TargetBranch: "main", ExpectedTargetSHA: publicationSHA("f")}, OperationPrefix: "issue9-test", MilestoneTitle: "issue9-test-stage", MilestoneDescription: "temporary publication fixture"}
	r.Candidate = f.source(t, "docs/plan-candidate.json", f.candidate)
	r.Authorization = f.source(t, "docs/plan-policy.json", f.policy)
	run := &protocol.Run{Envelope: publicationEnvelope(protocol.KindRun, protocol.Subject{Issue: 8}, "planner-run"), Role: protocol.RolePlanner, AgentInstance: "planner-instance", Host: "fixture-host", Inputs: []protocol.Reference{baselineRef}, InputSHA256: f.inputs.SHA256, AssignmentGeneration: 1, Attempt: 1, Budget: budget}
	r.PlannerRun = f.record(t, run, publicationIdentity(1), 8)
	f.commits[publicationSHA("f")] = gh.Commit{SHA: publicationSHA("f"), TreeSHA: publicationSHA("b")}
	f.commits[publicationSHA("e")] = gh.Commit{SHA: publicationSHA("e"), TreeSHA: publicationSHA("b")}
	f.pr = gh.PullRequest{Resource: publicationResource("pull_request", 30), Repository: publicationResource("repository", 0), BaseRepository: publicationResource("repository", 0), URL: "https://github.com/octo/pipeline/pull/30", Author: publicationIdentity(4), State: "closed", Merged: true, HeadSHA: publicationSHA("e"), BaseSHA: publicationSHA("c"), HeadRef: "codex/issue-7", BaseRef: "main", MergeSHA: publicationSHA("f")}
	f.request = r
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.handle(t, w, r) }))
	t.Cleanup(f.server.Close)
	return f, r
}
