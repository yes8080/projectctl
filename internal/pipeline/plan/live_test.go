package plan

// This is a test-only Controller host. It is intentionally not a production
// allocation source, bootstrap writer, scheduler, or upstream startup proof.
import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/planner"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

//go:embed testdata/live/*.json
var liveFiles embed.FS

type liveBundle struct {
	Candidate         planner.Candidate        `json:"candidate"`
	Inputs            planner.Inputs           `json:"inputs"`
	Authorization     protocol.Policy          `json:"authorization"`
	DesignSource      protocol.SourceReference `json:"design_source"`
	DesignPullRequest int64                    `json:"design_pull_request"`
}

func liveDecode(b []byte, v any) error {
	canonical, err := protocol.CanonicalJSON(b)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(canonical))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

func liveCommentRef(c gh.Comment) protocol.Reference {
	return protocol.Reference{Kind: "issue_comment", DatabaseID: c.DatabaseID, NodeID: c.NodeID, URL: c.URL, Author: c.Author, BodySHA256: c.Content.BodySHA256, CanonicalSHA256: c.Content.CanonicalSHA256}
}

// Only Controller code constructs this reader. Every call fetches the exact
// committed fixture using production GitHub Git-object reads and digest checks.
// It does NOT pretend the fixture has passed startup or planner.InspectInputs.
type liveInputs struct {
	gateway *gh.Gateway
	source  protocol.SourceReference
}

func (l liveInputs) Read(ctx context.Context, _ planner.Request) (planner.Inputs, error) {
	blob, err := l.gateway.ReadSource(ctx, l.source)
	if err != nil {
		return planner.Inputs{}, err
	}
	var v planner.Inputs
	if liveDecode(blob.Bytes, &v) != nil {
		return v, ErrBlocked
	}
	v.SHA256 = digest(struct {
		Design   any
		Manifest any
	}{v.Design, v.Manifest})
	return v, nil
}
func (l liveInputs) Replay(ctx context.Context, r planner.Request) (planner.Inputs, error) {
	return l.Read(ctx, r)
}

type liveHost struct {
	mu                           sync.Mutex
	prefix, phase, token         string
	expectedIssueBody            string
	actor                        protocol.GitHubIdentity
	g                            *gh.Gateway
	client                       *http.Client
	transport                    http.RoundTripper // test recorder only; nil uses production network
	control                      gh.Resource
	created                      map[int64]gh.Issue
	comments, issues, prComments int
}

// Guard every real request, including requests emitted inside Gateway. Only
// exact new fixture Issues enter the PATCH/comment allowlist after readback.
func (h *liveHost) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
		return nil, ErrBlocked
	}
	transport := h.transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if r.Method == http.MethodGet {
		return transport.RoundTrip(r)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/repos/yes8080/projectctl")
	data, err := io.ReadAll(io.LimitReader(r.Body, 65001))
	if err != nil || len(data) > 65000 {
		return nil, ErrBlocked
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	var body struct {
		Body      string `json:"body"`
		State     string `json:"state"`
		Milestone int64  `json:"milestone"`
	}
	if json.Unmarshal(data, &body) != nil {
		return nil, ErrBlocked
	}
	allowed := false
	if r.Method == http.MethodPost && p == "/issues" && h.phase == "publish" && body.Milestone == 1 && strings.Contains(body.Body, h.prefix+"/") && h.issues < 3 {
		h.issues++
		allowed = true
	}
	if h.phase != "cleanup" && r.Method == http.MethodPost && strings.HasSuffix(p, "/comments") && strings.Contains(body.Body, h.prefix+"/") && h.comments < 24 {
		var n int64
		if _, err := fmt.Sscanf(p, "/issues/%d/comments", &n); err == nil && p == fmt.Sprintf("/issues/%d/comments", n) {
			_, created := h.created[n]
			if n == 2 || created || (n == 3 && h.prComments < 1) {
				allowed = true
				h.comments++
				if n == 3 {
					h.prComments++
				}
			}
		}
	}
	if r.Method == http.MethodPatch && body.State == "closed" {
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || len(fields) != 1 {
			return nil, ErrBlocked
		}
		for n := range h.created {
			if p == fmt.Sprintf("/issues/%d", n) {
				allowed = true
			}
		}
	}
	if !allowed {
		return nil, ErrBlocked
	}
	return transport.RoundTrip(r)
}

func (h *liveHost) append(ctx context.Context, record protocol.Record, parent gh.Resource, create bool) (protocol.Reference, error) {
	body, err := protocol.EncodeComment(record)
	if err != nil {
		return protocol.Reference{}, err
	}
	if !strings.HasPrefix(record.Header().OperationID, h.prefix+"/") {
		return protocol.Reference{}, ErrBlocked
	}
	comments, err := h.g.ListComments(ctx, parent)
	if err != nil {
		return protocol.Reference{}, err
	}
	var found *protocol.Reference
	for _, c := range comments {
		if c.Content.Body == nil {
			continue
		}
		raw, e := protocol.DecodeComment(*c.Content.Body)
		if e != nil || raw.Header().OperationID != record.Header().OperationID {
			continue
		}
		if found != nil || *c.Content.Body != body || !identity(c.Author, h.actor) {
			return protocol.Reference{}, gh.ErrConflict
		}
		r := liveCommentRef(c)
		found = &r
	}
	if found != nil {
		return *found, nil
	}
	if !create {
		return protocol.Reference{}, gh.ErrUncertain
	}
	// One fresh host allocation, one POST, no retry (including transport loss).
	data, _ := json.Marshal(struct {
		Body string `json:"body"`
	}{body})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("https://api.github.com/repos/yes8080/projectctl/issues/%d/comments", parent.Number), bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return protocol.Reference{}, gh.ErrUncertain
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return protocol.Reference{}, gh.ErrUncertain
	}
	// Use complete native readback, never trust a guessed comment ID or body.
	return h.append(ctx, record, parent, false)
}

func (h *liveHost) cleanup(ctx context.Context) error {
	// Exactly one native Issue belongs to the committed single-slice fixture.
	// Missing/hidden/deleted/duplicated effects are not successful empty cleanup.
	if len(h.created) != 1 {
		return gh.ErrUncertain
	}
	for n, want := range h.created {
		got, err := h.g.ReadIssue(ctx, n)
		if err != nil || got.Resource != want.Resource || got.Content.BodySHA256 != want.Content.BodySHA256 || got.Milestone == nil || got.Milestone.Number != 1 {
			return ErrBlocked
		}
		if got.State != "closed" {
			req, _ := http.NewRequestWithContext(ctx, http.MethodPatch, fmt.Sprintf("https://api.github.com/repos/yes8080/projectctl/issues/%d", n), strings.NewReader(`{"state":"closed"}`))
			req.Header.Set("Authorization", "Bearer "+h.token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := h.client.Do(req)
			if err != nil {
				return gh.ErrUncertain
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				return gh.ErrUncertain
			}
		}
		got, err = h.g.ReadIssue(ctx, n)
		if err != nil || got.Resource != want.Resource || got.State != "closed" {
			return gh.ErrUncertain
		}
	}
	return nil
}

func liveExpectedIssueBody(project protocol.Project, control, repository, milestone gh.Resource, source protocol.SourceReference, prefix string, slice planner.Slice) string {
	op := prefix + "/issue/" + protocol.SHA256([]byte(slice.Key))
	body := "Plan slice " + slice.Key + ". The versioned contract comment is authoritative. Candidate source: " + source.Commit + ":" + source.Path + " sha256=" + source.SHA256
	fingerprint := struct {
		Project     protocol.Project `json:"project"`
		OperationID string           `json:"operation_id"`
		Action      gh.Action        `json:"action"`
		Control     gh.Resource      `json:"control"`
		Target      gh.Resource      `json:"target"`
		Related     *gh.Resource     `json:"related,omitempty"`
		Title       string           `json:"title"`
		Body        string           `json:"body"`
	}{project, op, gh.CreateIssue, control, repository, &milestone, slice.Goal, body}
	b, _ := json.Marshal(fingerprint)
	sha, _ := protocol.DigestJSON(b)
	intent := gh.Intent{Schema: "projectctl.operation/v1", Kind: "operation", OperationID: op, Phase: "object", Project: project, Action: gh.CreateIssue, Control: control, Target: repository, Related: &milestone, RequestSHA256: sha}
	b, _ = json.Marshal(intent)
	b, _ = protocol.CanonicalJSON(b)
	return body + "\n\n<!-- projectctl:operation:v1 --> " + string(b)
}

// Controller runs publish, has a different Agent inspect ReviewContext and
// publish the exact PlanAcceptance, then runs finish. This host never invents
// the independent review or publishes its own PASS.
func TestControllerLivePlan(t *testing.T) {
	if os.Getenv("PROJECTCTL_PLAN_LIVE") != "CONTROLLER_EXPLICIT_OPT_IN" {
		t.Skip("disabled; explicit Controller authorization required")
	}
	phase := os.Getenv("PROJECTCTL_PLAN_LIVE_PHASE")
	if phase != "publish" && phase != "finish" && phase != "cleanup" {
		t.Fatal("BLOCKED: exact phase required")
	}
	prefix, commit := os.Getenv("PROJECTCTL_PLAN_LIVE_PREFIX"), os.Getenv("PROJECTCTL_PLAN_LIVE_COMMIT")
	reviewerInstance := os.Getenv("PROJECTCTL_PLAN_REVIEWER_INSTANCE")
	if reviewerInstance == "" || reviewerInstance == prefix+"/planner" {
		t.Fatal("BLOCKED: explicit distinct independent reviewer Agent instance required")
	}
	if !regexp.MustCompile(`^issue9-[a-f0-9]{12}-[a-f0-9]{12}$`).MatchString(prefix) || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(commit) || !strings.HasPrefix(prefix, "issue9-"+commit[:12]+"-") {
		t.Fatal("BLOCKED: exact candidate commit and fresh prefix required")
	}
	token := os.Getenv("PROJECTCTL_GITHUB_TOKEN")
	if token == "" {
		t.Fatal("BLOCKED: Controller secret provider required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	h := &liveHost{prefix: prefix, phase: phase, token: token, created: map[int64]gh.Issue{}}
	h.client = &http.Client{Transport: h, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	project := protocol.Project{Owner: "yes8080", Repo: "projectctl", ControlIssue: 2}
	config := gh.Config{Project: project, HTTPClient: h.client, Token: func(context.Context) (string, error) { return token, nil }}
	g, err := gh.New(config)
	if err != nil {
		t.Fatal("BLOCKED gateway")
	}
	h.g = g
	actor, err := g.CurrentUser(ctx)
	if err != nil || actor.ID != 515784 || actor.NodeID != "MDQ6VXNlcjUxNTc4NA==" || actor.Type != "User" {
		t.Fatal("BLOCKED authenticated Controller identity")
	}
	h.actor = actor
	config.Publisher = actor
	config.Authorize = func(_ context.Context, i gh.Intent, a protocol.GitHubIdentity) error {
		if i.Project != project || i.Control != h.control || !identity(a, actor) || !strings.HasPrefix(i.OperationID, prefix+"/") || i.Action == gh.CreateMilestone || i.Action == gh.AddDependency {
			return gh.ErrDenied
		}
		return nil
	}
	g, err = gh.New(config)
	if err != nil {
		t.Fatal("BLOCKED gateway")
	}
	h.g = g
	pin := func(name string) protocol.SourceReference {
		b, err := liveFiles.ReadFile("testdata/live/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.SourceReference{Commit: commit, Path: "internal/pipeline/plan/testdata/live/" + name, SHA256: protocol.SHA256(b)}
	}
	blob, err := g.ReadSource(ctx, pin("bundle.json"))
	if err != nil {
		t.Fatal("BLOCKED immutable bundle")
	}
	var bundle liveBundle
	if liveDecode(blob.Bytes, &bundle) != nil {
		t.Fatal("BLOCKED strict bundle")
	}
	for _, name := range []string{"candidate.json", "authorization.json", "inputs.json", "request-blueprint.json"} {
		if _, err := g.ReadSource(ctx, pin(name)); err != nil {
			t.Fatal("BLOCKED immutable source", name)
		}
	}
	control, err := g.ReadIssue(ctx, 2)
	if err != nil || control.State != "open" {
		t.Fatal("BLOCKED control")
	}
	h.control = control.Resource
	milestone, err := g.ReadMilestone(ctx, 1)
	if err != nil || milestone.State != "open" {
		t.Fatal("BLOCKED existing milestone")
	}
	if control.Resource != (gh.Resource{Kind: "issue", Number: 2, DatabaseID: 5604683166, NodeID: "I_kwDOUuTVxc8AAAABThCtng"}) || milestone.Resource != (gh.Resource{Kind: "milestone", Number: 1, DatabaseID: 18118698, NodeID: "MI_kwDOUuTVxc4BFHgq"}) {
		t.Fatal("BLOCKED preexisting native identity drift")
	}
	repository, err := g.ReadRepository(ctx)
	if err != nil || repository.DatabaseID != 1390728645 || repository.NodeID != "R_kgDOUuTVxQ" {
		t.Fatal("BLOCKED repository identity")
	}
	h.expectedIssueBody = liveExpectedIssueBody(project, control.Resource, repository.Resource, milestone.Resource, pin("candidate.json"), prefix, bundle.Candidate.Contracts[0])
	pr, err := g.ReadPullRequest(ctx, 3)
	if err != nil || !pr.Merged || pr.MergeSHA != bundle.DesignSource.Commit {
		t.Fatal("BLOCKED real merged design navigation")
	}
	if _, err := g.ReadSource(ctx, bundle.DesignSource); err != nil {
		t.Fatal("BLOCKED real historical design source")
	}
	merge, err := g.ReadCommit(ctx, pr.MergeSHA)
	if err != nil {
		t.Fatal("BLOCKED merge commit")
	}
	// Discover exact own objects for recovery/cleanup; never adopt by title.
	issues, err := g.ListIssues(ctx)
	if err != nil {
		t.Fatal("BLOCKED complete issue listing")
	}
	for _, v := range issues {
		if v.Kind == "issue" && v.Content.Body != nil && strings.Contains(*v.Content.Body, `"operation_id":"`+prefix+`/issue/`) && identity(v.Author, actor) && v.Milestone != nil && v.Milestone.Number == 1 {
			if *v.Content.Body != h.expectedIssueBody {
				t.Fatal("BLOCKED edited fixture Issue body")
			}
			h.created[v.Number] = v
		}
	}
	// Rebuild the whole fixture's counters on every process start, including
	// Controller-published independent review comments. No local budget ledger.
	parents := []gh.Resource{control.Resource, pr.Resource}
	for _, v := range h.created {
		parents = append(parents, v.Resource)
	}
	h.issues = len(h.created)
	for _, parent := range parents {
		cs, e2 := g.ListComments(ctx, parent)
		if e2 != nil {
			t.Fatal("BLOCKED complete cumulative comment count")
		}
		for _, c := range cs {
			if c.Content.Body != nil && strings.Contains(*c.Content.Body, prefix+"/") {
				h.comments++
				if parent.Number == 3 {
					h.prComments++
				}
			}
		}
	}
	if h.comments > 24 || h.issues > 3 || h.prComments > 1 {
		t.Fatal("BLOCKED cumulative fixture budget")
	}
	if phase == "cleanup" {
		if err := h.cleanup(ctx); err != nil {
			t.Fatal("CLEANUP INCOMPLETE", err)
		}
		t.Log("CLEANUP VERIFIED; comments retained; no activation claim")
		return
	}
	if phase == "publish" {
		for _, parent := range []gh.Resource{control.Resource, pr.Resource} {
			cs, e := g.ListComments(ctx, parent)
			if e != nil {
				t.Fatal("BLOCKED complete prefix scan")
			}
			for _, c := range cs {
				if c.Content.Body != nil && strings.Contains(*c.Content.Body, prefix+"/") {
					t.Fatal("UNCERTAIN: prefix already used; reconcile only, never restart publish")
				}
			}
		}
		if len(h.created) != 0 {
			t.Fatal("UNCERTAIN: existing prefix effect")
		}
	}
	create := phase == "publish"
	appendRecord := func(r protocol.Record, parent gh.Resource) protocol.Reference {
		ref, e := h.append(ctx, r, parent, create)
		if e != nil {
			t.Fatal("UNCERTAIN/BLOCKED record; do not retry publish", r.Header().OperationID, e)
		}
		t.Logf("native record id=%d node=%s url=%s", ref.DatabaseID, ref.NodeID, ref.URL)
		return ref
	}
	env := func(k protocol.Kind, s protocol.Subject, key string) protocol.Envelope {
		return protocol.Envelope{Schema: protocol.Schema, Kind: k, Project: project, Subject: s, OperationID: prefix + "/" + key, Version: 1}
	}
	policySHA, err := bundle.Authorization.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// A Contract is the required strong-reference leaf for Run.Inputs, not a
	// second task table or historical design approval. Its AC is fixture-only.
	seed := &protocol.Contract{Envelope: env(protocol.KindContract, protocol.Subject{Issue: 2}, "fixture-seed"), DesignBaseline: protocol.DesignBaseline{Issue: 1, PullRequest: 3, MergeCommit: pr.MergeSHA, Tree: merge.TreeSHA, Approval: "https://github.com/yes8080/projectctl/issues/2#issuecomment-5861156252"}, Key: "fixture-only", Goal: "Plan-boundary fixture; not upstream design acceptance", Scope: []string{"fixed Controller input"}, NonGoals: []string{"startup or design E2E"}, Requirements: []string{"integration"}, Acceptance: []protocol.Criterion{{ID: "fixture", Statement: "Controller supplied fixed plan input"}}, Verification: []protocol.Check{{ID: "fixture", Argv: []string{"go", "test", "./internal/pipeline/plan/..."}}}, ImpactSurface: []string{"test fixture"}, Risks: []protocol.Risk{{Risk: "mistaken upstream proof", Mitigation: "explicit plan boundary only"}}, AcceptanceEnvironment: bundle.Candidate.Contracts[0].AcceptanceEnvironment}
	seedRef := appendRecord(seed, control.Resource)
	inputReader := liveInputs{g, pin("inputs.json")}
	inputs, err := inputReader.Read(ctx, planner.Request{})
	if err != nil {
		t.Fatal("BLOCKED fixed inputs")
	}
	run := func(role protocol.Role, subject protocol.Subject, key, instance, sha string, refs []protocol.Reference) *protocol.Run {
		return &protocol.Run{Envelope: env(protocol.KindRun, subject, key), Role: role, AgentInstance: prefix + "/" + instance, Host: "Controller-test-host-shared-native-principal", Inputs: refs, InputSHA256: sha, AssignmentGeneration: 1, Attempt: 1, Budget: inputs.Manifest.Policy.Budget}
	}
	designRun := appendRecord(run(protocol.RoleDesigner, protocol.Subject{Issue: 2}, "fixture-designer-run", "fixture-designer", inputs.SHA256, []protocol.Reference{seedRef}), control.Resource)
	binding := protocol.Binding{PullRequest: 3, HeadSHA: pr.HeadSHA, BaseSHA: pr.BaseSHA, DesignSHA256: bundle.DesignSource.SHA256, ContractSHA256: seedRef.CanonicalSHA256, PolicySHA256: policySHA}
	legacyReview := appendRecord(&protocol.Acceptance{Envelope: env(protocol.KindAcceptance, protocol.Subject{PullRequest: 3}, "fixture-acceptance"), Binding: binding, Run: designRun, Assessments: []protocol.Assessment{{CriterionID: "fixture", Result: "PASS", Reason: "Controller plan-boundary fixture only; does not replace historical design approval", Evidence: []protocol.Reference{seedRef}}}, UnresolvedFindings: []protocol.Reference{}, Decision: "PASS", Reason: "Plan-only trusted InputReader fixture, not upstream proof"}, pr.Resource)
	legacyApproval := appendRecord(&protocol.Approval{Envelope: env(protocol.KindApproval, protocol.Subject{PullRequest: 3}, "fixture-approval"), Candidate: legacyReview, Decision: "APPROVED", Reason: "Approve only fixed plan-boundary fixture", PolicySHA256: policySHA}, control.Resource)
	baseline := appendRecord(&protocol.Baseline{Envelope: env(protocol.KindBaseline, protocol.Subject{Issue: 2}, "fixture-baseline"), Inputs: []protocol.SourceReference{bundle.DesignSource}, Design: bundle.DesignSource, PolicySHA256: policySHA, Review: legacyReview, Approval: legacyApproval}, control.Resource)
	plannerRun := appendRecord(run(protocol.RolePlanner, protocol.Subject{Issue: 2}, "planner-run", "planner", inputs.SHA256, []protocol.Reference{baseline}), control.Resource)
	r := Request{Input: planner.Request{Project: project, Anchor: startup.Anchor{Repository: inputs.Manifest.Repository, Manifest: pin("inputs.json")}, Issue: control.Resource, Baseline: baseline, TargetBranch: repository.DefaultBranch, ExpectedTargetSHA: pr.MergeSHA}, Candidate: pin("candidate.json"), Authorization: pin("authorization.json"), PlannerRun: plannerRun, OperationPrefix: prefix, MilestoneTitle: milestone.Title, MilestoneDescription: "existing Milestone #1 unchanged", ExistingMilestone: &MilestonePin{Resource: milestone.Resource, Title: milestone.Title, BodySHA256: milestone.Content.BodySHA256}}
	e, _ := New(g, inputReader)
	var state State
	for step := 0; step < 4; step++ {
		state, err = e.Inspect(ctx, r)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrPartial) || state.Next == nil || !create {
			t.Fatal("BLOCKED publication; no blind retry", err)
		}
		admission, e2 := e.Admit(ctx, r, func(c context.Context, i gh.Intent, a protocol.GitHubIdentity) (protocol.VerifiedRecord, error) {
			ref := appendRecord(run(protocol.RolePublisher, protocol.Subject{Issue: 2}, "publisher-"+protocol.SHA256([]byte(i.OperationID)), "publisher-"+protocol.SHA256([]byte(i.OperationID)), i.RequestSHA256, []protocol.Reference{baseline}), control.Resource)
			return g.FetchRecord(c, ref)
		})
		if e2 != nil {
			t.Fatal("UNCERTAIN admission", e2)
		}
		_, e2 = e.Apply(ctx, admission)
		if e2 != nil && !errors.Is(e2, ErrPartial) {
			t.Fatal("UNCERTAIN apply; reconcile only", e2)
		}
		all, e2 := g.ListIssues(ctx)
		if e2 != nil {
			t.Fatal("UNCERTAIN readback")
		}
		for _, v := range all {
			if v.Kind == "issue" && v.Content.Body != nil && strings.Contains(*v.Content.Body, `"operation_id":"`+prefix+`/issue/`) && identity(v.Author, actor) && v.Milestone != nil && v.Milestone.Number == 1 {
				if *v.Content.Body != h.expectedIssueBody {
					t.Fatal("BLOCKED edited fixture Issue body")
				}
				h.created[v.Number] = v
			}
		}
	}
	if err != nil || state.PlanReference == nil {
		t.Fatal("BLOCKED incomplete publication")
	}
	view, err := e.ReviewInput(ctx, r)
	if err != nil {
		t.Fatal("BLOCKED review context", err)
	}
	reviewerAssignment := run(protocol.RolePlanReviewer, state.Plan.Subject, "plan-reviewer-run", "independent-reviewer", view.InputSHA256, view.Inputs)
	reviewerAssignment.AgentInstance = reviewerInstance
	reviewerRun := appendRecord(reviewerAssignment, control.Resource)
	if phase == "publish" {
		allowlist := []gh.Resource{}
		for _, v := range h.created {
			allowlist = append(allowlist, v.Resource)
			t.Logf("cleanup allowlist id=%d node=%s url=%s body_sha256=%s", v.DatabaseID, v.NodeID, v.URL, v.Content.BodySHA256)
		}
		b, _ := json.Marshal(struct {
			Request          Request
			Plan             protocol.Reference
			ReviewerRun      protocol.Reference
			ReviewContext    ReviewContext
			CleanupAllowlist []gh.Resource
			Boundary         string
		}{r, *state.PlanReference, reviewerRun, view, allowlist, "Plan-only fixture. Same native credential; independent Agent must review. No upstream proof. No PASS."})
		t.Log(string(b))
		t.Log("AWAITING_INDEPENDENT_AGENT_REVIEW; retain fixtures then run finish or cleanup")
		return
	}
	// The independent Agent's Controller-published review is selected by exact
	// operation, never generated here. Duplicate, changed or wrong bindings fail.
	comments, err := g.ListComments(ctx, control.Resource)
	if err != nil {
		t.Fatal(err)
	}
	var reviewRef *protocol.Reference
	for _, c := range comments {
		if c.Content.Body == nil {
			continue
		}
		rr, e2 := protocol.DecodeComment(*c.Content.Body)
		if e2 != nil || rr.Header().OperationID != prefix+"/independent-plan-review" {
			continue
		}
		if reviewRef != nil {
			t.Fatal("BLOCKED duplicate review")
		}
		v := liveCommentRef(c)
		reviewRef = &v
	}
	if reviewRef == nil {
		t.Fatal("BLOCKED independent review missing")
	}
	verified, err := g.FetchRecord(ctx, *reviewRef)
	if err != nil || protocol.Authorize(verified, bundle.Authorization, protocol.RolePlanReviewer) != nil {
		t.Fatal("BLOCKED review author")
	}
	record, err := verified.Record()
	rv, ok := record.(*protocol.PlanAcceptance)
	if err != nil || !ok || rv.Decision != "PASS" || len(rv.UnresolvedFindings) != 0 || !reference(rv.Candidate, *state.PlanReference) || !reference(rv.Run, reviewerRun) || rv.Subject != state.Plan.Subject || len(rv.Assessments) != len(reviewChecks) {
		t.Fatal("BLOCKED independent review binding")
	}
	checks := map[string]bool{}
	for _, id := range reviewChecks {
		checks[id] = true
	}
	for _, ac := range rv.Assessments {
		if !checks[ac.CriterionID] || ac.Result != "PASS" || len(ac.Evidence) != 1 || !reference(ac.Evidence[0], *state.PlanReference) {
			t.Fatal("BLOCKED incomplete review")
		}
		delete(checks, ac.CriterionID)
	}
	approval := &protocol.Approval{Envelope: env(protocol.KindApproval, state.Plan.Subject, "plan-approval"), Candidate: *reviewRef, Decision: "APPROVED", Reason: "Controller explicit finish; exact independent Plan review", PolicySHA256: policySHA}
	// Only this new append is permitted in finish; same fixed prefix and bound.
	approvalRef, err := h.append(ctx, approval, control.Resource, true)
	if err != nil {
		t.Fatal("UNCERTAIN approval", err)
	}
	rr := ReviewRequest{Publication: r, Plan: *state.PlanReference, Review: *reviewRef, Approval: approvalRef}
	if _, err = e.Activate(ctx, rr); err != nil {
		t.Fatal("BLOCKED activation", err)
	}
	fresh, _ := New(g, inputReader)
	if _, err = fresh.CheckFrozen(ctx, rr); err != nil {
		t.Fatal("BLOCKED cold recheck", err)
	}
	if err = h.cleanup(ctx); err != nil {
		t.Fatal("CLEANUP INCOMPLETE", err)
	}
	if _, err = fresh.CheckFrozen(ctx, rr); err != nil {
		t.Fatal("BLOCKED post-cleanup freeze", err)
	}
	t.Logf("PLAN_BOUNDARY_ACTIVATED_AND_CLEANUP_VERIFIED plan=%s approval=%s new_issues=%d; no credential isolation or upstream proof", state.PlanReference.URL, approvalRef.URL, len(h.created))
}
