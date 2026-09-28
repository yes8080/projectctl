package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/planner"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func digest(v any) string {
	b, _ := json.Marshal(displayNeutral(reflect.ValueOf(v)).Interface())
	d, _ := protocol.DigestJSON(b)
	return d
}

// Only transient identity projections omit display logins. Stored record bytes,
// source pins and policy digests are never rewritten or weakened by this helper.
func displayNeutral(v reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	if v.Type() == reflect.TypeOf(protocol.GitHubIdentity{}) {
		identity := v.Interface().(protocol.GitHubIdentity)
		identity.Login = ""
		return reflect.ValueOf(identity)
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return v
		}
		copy := reflect.New(v.Type()).Elem()
		if v.Kind() == reflect.Pointer {
			copy.Set(reflect.New(v.Type().Elem()))
			copy.Elem().Set(displayNeutral(v.Elem()))
		} else {
			copy.Set(displayNeutral(v.Elem()))
		}
		return copy
	case reflect.Struct:
		copy := reflect.New(v.Type()).Elem()
		copy.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				copy.Field(i).Set(displayNeutral(v.Field(i)))
			}
		}
		return copy
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		copy := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			copy.Index(i).Set(displayNeutral(v.Index(i)))
		}
		return copy
	}
	return v
}
func identity(a, b protocol.GitHubIdentity) bool {
	return a.ID == b.ID && a.NodeID == b.NodeID && a.Type == b.Type
}
func reference(a, b protocol.Reference) bool {
	return a.Kind == b.Kind && a.DatabaseID == b.DatabaseID && a.NodeID == b.NodeID && a.URL == b.URL && a.BodySHA256 == b.BodySHA256 && a.CanonicalSHA256 == b.CanonicalSHA256 && identity(a.Author, b.Author)
}
func operation(r Request, kind, key string) string {
	if key == "" {
		return r.OperationPrefix + "/" + kind
	}
	return r.OperationPrefix + "/" + kind + "/" + protocol.SHA256([]byte(key))
}
func policyReference(ref protocol.Reference, policy protocol.Policy) protocol.Reference {
	// Use the pinned policy's display spelling when reconstructing published
	// reference bytes. A native login rename must not rewrite an existing Plan.
	for _, grant := range policy.Grants {
		if identity(ref.Author, grant.Principal) {
			ref.Author.Login = grant.Principal.Login
			break
		}
	}
	return ref
}
func commentRef(c gh.Comment, policy protocol.Policy) protocol.Reference {
	return policyReference(protocol.Reference{Kind: "issue_comment", DatabaseID: c.DatabaseID, NodeID: c.NodeID, URL: c.URL, Author: c.Author, BodySHA256: c.Content.BodySHA256, CanonicalSHA256: c.Content.CanonicalSHA256}, policy)
}
func budgetWithin(run, policy protocol.Budget) bool {
	if run.MaxRunSeconds <= 0 || run.MaxRunSeconds > policy.MaxRunSeconds || run.MaxRunSeconds > 1800 {
		return false
	}
	run.MaxRunSeconds = policy.MaxRunSeconds
	return run == policy
}
func (e *Engine) source(ctx context.Context, ref protocol.SourceReference) ([]byte, error) {
	b, err := e.remote.ReadSource(ctx, ref)
	if err != nil || b.Reference != ref || protocol.SHA256(b.Bytes) != ref.SHA256 || len(b.Bytes) == 0 || len(b.Bytes) > 1<<20 {
		return nil, ErrBlocked
	}
	return b.Bytes, nil
}
func (e *Engine) record(ctx context.Context, ref protocol.Reference, policy protocol.Policy, role protocol.Role) (protocol.Record, error) {
	v, err := e.remote.FetchRecord(ctx, ref)
	if err != nil || !reference(v.Reference(), ref) || protocol.Authorize(v, policy, role) != nil {
		return nil, ErrBlocked
	}
	return v.Record()
}
func (e *Engine) prepare(ctx context.Context, r Request) (prepared, error) {
	var p prepared
	if ctx == nil || ctx.Err() != nil || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(r.OperationPrefix) || strings.TrimSpace(r.MilestoneTitle) == "" || len(r.MilestoneTitle) > 256 || strings.TrimSpace(r.MilestoneDescription) == "" {
		return p, ErrBlocked
	}
	input, err := e.inputs.Read(ctx, r.Input)
	if err != nil {
		return p, ErrBlocked
	}
	p.input = input
	data, err := e.source(ctx, r.Authorization)
	if err != nil {
		return p, err
	}
	p.policy, err = protocol.DecodePolicy(data)
	if err != nil || p.policy.Project != r.Input.Project {
		return p, ErrBlocked
	}
	data, err = e.source(ctx, r.Candidate)
	if err != nil {
		return p, err
	}
	p.candidate, err = planner.DecodeCandidate(data)
	if err != nil {
		return p, ErrBlocked
	}
	p.summary, err = planner.ValidateCandidate(p.candidate, input.Design.Requirements, input.Manifest.Policy.Environment)
	if err != nil {
		return p, ErrBlocked
	}
	value, err := e.record(ctx, r.PlannerRun, p.policy, protocol.RoleController)
	if err != nil {
		return p, err
	}
	run, ok := value.(*protocol.Run)
	if !ok || run.Role != protocol.RolePlanner || run.Subject != (protocol.Subject{Issue: r.Input.Issue.Number}) || run.InputSHA256 != input.SHA256 || len(run.Inputs) != 1 || !reference(run.Inputs[0], r.Input.Baseline) || !budgetWithin(run.Budget, input.Manifest.Policy.Budget) || run.Attempt > input.Manifest.Policy.Budget.MaxPlanningRounds {
		return p, ErrBlocked
	}
	p.plannerRun = *run
	value, err = e.record(ctx, r.Input.Baseline, p.policy, protocol.RoleController)
	if err != nil {
		return p, err
	}
	baseline, ok := value.(*protocol.Baseline)
	if !ok {
		return p, ErrBlocked
	}
	p.baseline = *baseline
	review, err := e.remote.FetchRecord(ctx, baseline.Review)
	if err != nil {
		return p, ErrBlocked
	}
	value, err = review.Record()
	if err != nil {
		return p, ErrBlocked
	}
	accept, ok := value.(*protocol.Acceptance)
	if !ok {
		return p, ErrBlocked
	}
	pr, err := e.remote.ReadPullRequest(ctx, accept.Binding.PullRequest)
	if err != nil || !pr.Merged || pr.MergeSHA != baseline.Design.Commit {
		return p, ErrBlocked
	}
	commit, err := e.remote.ReadCommit(ctx, pr.MergeSHA)
	if err != nil || commit.SHA != pr.MergeSHA {
		return p, ErrBlocked
	}
	p.designBaseline = protocol.DesignBaseline{Issue: p.plannerRun.Subject.Issue, PullRequest: pr.Number, MergeCommit: pr.MergeSHA, Tree: commit.TreeSHA, Approval: baseline.Approval.URL}
	// DesignBaseline.Issue is historical navigation; derive the real design Issue
	// from the reviewed designer Run rather than pretending the planner owns it.
	rv, err := e.remote.FetchRecord(ctx, accept.Run)
	if err != nil {
		return p, ErrBlocked
	}
	raw, err := rv.Record()
	if err != nil {
		return p, ErrBlocked
	}
	designRun, ok := raw.(*protocol.Run)
	if !ok || designRun.Subject.Issue <= 0 {
		return p, ErrBlocked
	}
	p.designBaseline.Issue = designRun.Subject.Issue
	repo, err := e.remote.ReadRepository(ctx)
	if err != nil || repo.DatabaseID != r.Input.Anchor.Repository.DatabaseID || repo.NodeID != r.Input.Anchor.Repository.NodeID {
		return p, ErrBlocked
	}
	p.repository = repo.Resource
	control, err := e.remote.ReadIssue(ctx, r.Input.Project.ControlIssue)
	if err != nil || control.State != "open" || control.Kind != "issue" {
		return p, ErrBlocked
	}
	p.control = control.Resource
	p.digest = digest(struct {
		Request   Request
		Input     string
		Candidate planner.Candidate
		Policy    protocol.Policy
	}{r, input.SHA256, p.candidate, p.policy})
	if p.digest == "" {
		return p, ErrBlocked
	}
	return p, nil
}

func requestFor(r Request, p prepared, action gh.Action, kind, key string, target gh.Resource) gh.Request {
	return gh.Request{Project: r.Input.Project, OperationID: operation(r, kind, key), Action: action, Control: p.control, Target: target}
}
func (e *Engine) step(ctx context.Context, q gh.Request) (gh.Outcome, error) {
	return e.remote.Reconcile(ctx, q)
}
func incomplete(state State, q gh.Request, err error) (State, error) {
	if errors.Is(err, gh.ErrUncertain) {
		state.Status = "partial"
		state.Next = &q
		return state, ErrPartial
	}
	return State{}, fmt.Errorf("%w: reconciliation rejected", ErrBlocked)
}

func contractFor(r Request, p prepared, s planner.Slice, issue gh.Resource) *protocol.Contract {
	criteria := make([]protocol.Criterion, 0, len(s.Acceptance))
	for _, ac := range s.Acceptance {
		trace, _ := protocol.CanonicalJSON(mustJSON(struct {
			Requirements     []string `json:"requirements"`
			Checks           []string `json:"checks"`
			CandidateBinding string   `json:"candidate_binding"`
		}{ac.Requirements, ac.Checks, s.CandidateBinding}))
		criteria = append(criteria, protocol.Criterion{ID: ac.ID, Statement: ac.Statement + "\n\nExact requirement/check references: " + string(trace)})
	}
	return &protocol.Contract{Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindContract, Project: r.Input.Project, Subject: protocol.Subject{Issue: issue.Number}, OperationID: operation(r, "contract", s.Key), Version: 1}, DesignBaseline: p.designBaseline, Key: s.Key, Goal: s.Goal, Scope: s.Scope, NonGoals: s.NonGoals, Requirements: s.Requirements, Acceptance: criteria, Verification: s.Verification, ImpactSurface: s.ImpactSurface, Risks: s.Risks, AcceptanceEnvironment: s.AcceptanceEnvironment}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// CompletionPolicy is the sole frozen completion/terminal policy representation
// in protocol.Plan. It introduces no second TerminalConditions field.
func CompletionPolicy(p prepared) []string {
	b, _ := protocol.CanonicalJSON(mustJSON(struct {
		Completion any      `json:"completion"`
		Terminal   []string `json:"terminal_conditions"`
	}{p.input.Manifest.Policy.Completion, p.input.Manifest.Policy.TerminalConditions}))
	return []string{string(b)}
}

// Inspect replays the complete deterministic operation prefix from GitHub. A
// missing/hidden effect returns the exact next request but grants no write right.
func (e *Engine) Inspect(ctx context.Context, r Request) (State, error) {
	p, err := e.prepare(ctx, r)
	if err != nil {
		return State{}, err
	}
	state, err := e.inspect(ctx, r, p)
	if err != nil {
		return state, err
	}
	again, err := e.prepare(ctx, r)
	if err != nil || again.digest != p.digest {
		return State{}, ErrBlocked
	}
	check, err := e.inspect(ctx, r, again)
	if err != nil || digest(state) != digest(check) {
		return State{}, ErrBlocked
	}
	return check, nil
}
func (e *Engine) inspect(ctx context.Context, r Request, p prepared) (State, error) {
	state := State{Status: "partial", InputSHA256: p.digest}
	var milestone gh.Milestone
	if r.ExistingMilestone != nil {
		v, err := e.remote.ReadMilestone(ctx, r.ExistingMilestone.Resource.Number)
		if err != nil || v.Resource != r.ExistingMilestone.Resource || v.Title != r.ExistingMilestone.Title || v.Content.Body == nil || v.Content.BodySHA256 != r.ExistingMilestone.BodySHA256 {
			return State{}, ErrBlocked
		}
		milestone = v
	} else {
		q := requestFor(r, p, gh.CreateMilestone, "milestone", "", p.repository)
		q.Title = r.MilestoneTitle
		q.Body = r.MilestoneDescription
		out, err := e.step(ctx, q)
		if err != nil {
			return incomplete(state, q, err)
		}
		if out.Milestone == nil {
			return State{}, ErrBlocked
		}
		milestone = *out.Milestone
	}
	if milestone.State != "open" && !(e.historical && milestone.State == "closed") {
		return State{}, ErrBlocked
	}
	issues := map[string]gh.Issue{}
	contracts := map[string]protocol.Reference{}
	members := []protocol.Member{}
	slices := append([]planner.Slice(nil), p.candidate.Contracts...)
	sort.Slice(slices, func(i, j int) bool { return slices[i].Key < slices[j].Key })
	for _, s := range slices {
		q := requestFor(r, p, gh.CreateIssue, "issue", s.Key, p.repository)
		q.Related = &milestone.Resource
		q.Title = s.Goal
		q.Body = "Plan slice " + s.Key + ". The versioned contract comment is authoritative. Candidate source: " + r.Candidate.Commit + ":" + r.Candidate.Path + " sha256=" + r.Candidate.SHA256
		out, err := e.step(ctx, q)
		if err != nil {
			return incomplete(state, q, err)
		}
		if out.Issue == nil || (out.Issue.State != "open" && !(e.historical && out.Issue.State == "closed")) || out.Issue.Milestone == nil || *out.Issue.Milestone != milestone.Resource {
			return State{}, ErrBlocked
		}
		issues[s.Key] = *out.Issue
		contract := contractFor(r, p, s, out.Issue.Resource)
		if _, err = protocol.Encode(contract); err != nil {
			return State{}, ErrBlocked
		}
		q = requestFor(r, p, gh.PublishRecord, "contract", s.Key, out.Issue.Resource)
		q.Record = contract
		out, err = e.step(ctx, q)
		if err != nil {
			return incomplete(state, q, err)
		}
		if out.Comment == nil {
			return State{}, ErrBlocked
		}
		ref := commentRef(*out.Comment, p.policy)
		actual, err := e.record(ctx, ref, p.policy, protocol.RoleController)
		if err != nil || !reflect.DeepEqual(actual, contract) {
			return State{}, ErrBlocked
		}
		contracts[s.Key] = ref
		issue := issues[s.Key].Resource
		members = append(members, protocol.Member{Issue: issue.Number, IssueDatabaseID: issue.DatabaseID, IssueNodeID: issue.NodeID, Contract: ref})
	}
	for _, edge := range p.summary.Edges {
		target, related := issues[edge.To].Resource, issues[edge.From].Resource
		q := requestFor(r, p, gh.AddDependency, "dependency", edge.To+"\x00"+edge.From, target)
		q.Related = &related
		if _, err := e.step(ctx, q); err != nil {
			return incomplete(state, q, err)
		}
	}
	topology, err := e.topology(ctx, r, p, milestone, issues)
	if err != nil {
		return State{}, err
	}
	plan := &protocol.Plan{Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindPlan, Project: r.Input.Project, Subject: protocol.Subject{Milestone: milestone.Number}, OperationID: operation(r, "candidate", ""), Version: 1}, Baseline: policyReference(r.Input.Baseline, p.policy), Members: members, NativeTopologySHA256: topology, Budget: p.input.Manifest.Policy.Budget, Completion: CompletionPolicy(p)}
	if _, err := protocol.Encode(plan); err != nil {
		return State{}, ErrBlocked
	}
	state.Plan = plan
	q := requestFor(r, p, gh.PublishRecord, "candidate", "", p.control)
	q.Record = plan
	out, err := e.step(ctx, q)
	if err != nil {
		return incomplete(state, q, err)
	}
	if out.Comment == nil {
		return State{}, ErrBlocked
	}
	ref := commentRef(*out.Comment, p.policy)
	actual, err := e.record(ctx, ref, p.policy, protocol.RoleController)
	if err != nil || !reflect.DeepEqual(actual, plan) {
		return State{}, ErrBlocked
	}
	state.Status = "candidate"
	state.PlanReference = &ref
	return state, nil
}

func (e *Engine) topology(ctx context.Context, r Request, p prepared, m gh.Milestone, issues map[string]gh.Issue) (string, error) {
	current, err := e.remote.ReadMilestone(ctx, m.Number)
	if err != nil || !reflect.DeepEqual(current, m) {
		return "", ErrBlocked
	}
	known := map[int64]bool{}
	for _, i := range issues {
		known[i.Number] = true
	}
	all, err := e.remote.ListIssues(ctx)
	if err != nil {
		return "", ErrBlocked
	}
	for _, i := range all {
		if i.Milestone != nil && *i.Milestone == m.Resource && !known[i.Number] && r.ExistingMilestone == nil {
			return "", ErrBlocked
		}
	}
	type item struct {
		Issue               gh.Issue
		BlockedBy, Blocking []gh.Resource
	}
	rows := []item{}
	for key, i := range issues {
		live, err := e.remote.ReadIssue(ctx, i.Number)
		if err != nil || !reflect.DeepEqual(live, i) {
			return "", ErrBlocked
		}
		row := item{Issue: live, BlockedBy: []gh.Resource{}, Blocking: []gh.Resource{}}
		for _, direction := range []string{"blocked_by", "blocking"} {
			observed, err := e.remote.ListDependencies(ctx, i.Number, direction)
			if err != nil {
				return "", ErrBlocked
			}
			expected := map[gh.Resource]bool{}
			for _, edge := range p.summary.Edges {
				if direction == "blocked_by" && edge.To == key {
					expected[issues[edge.From].Resource] = true
				}
				if direction == "blocking" && edge.From == key {
					expected[issues[edge.To].Resource] = true
				}
			}
			if len(observed) != len(expected) {
				return "", ErrBlocked
			}
			seen := map[gh.Resource]bool{}
			for _, related := range observed {
				if !expected[related.Resource] || seen[related.Resource] {
					return "", ErrBlocked
				}
				seen[related.Resource] = true
				if direction == "blocked_by" {
					row.BlockedBy = append(row.BlockedBy, related.Resource)
				} else {
					row.Blocking = append(row.Blocking, related.Resource)
				}
			}
		}
		sort.Slice(row.BlockedBy, func(i, j int) bool { return row.BlockedBy[i].DatabaseID < row.BlockedBy[j].DatabaseID })
		sort.Slice(row.Blocking, func(i, j int) bool { return row.Blocking[i].DatabaseID < row.Blocking[j].DatabaseID })
		// Native open/closed progress and display login changes are not topology.
		row.Issue.State = ""
		row.Issue.CreatedAt = ""
		row.Issue.Author.Login = ""
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Issue.DatabaseID < rows[j].Issue.DatabaseID })
	m.State = ""
	m.Author.Login = ""
	return digest(struct {
		Milestone gh.Milestone
		Members   []item
	}{m, rows}), nil
}

func (e *Engine) Admit(ctx context.Context, r Request, fresh gh.FreshAllocationSource) (Admission, error) {
	if fresh == nil {
		return Admission{}, ErrBlocked
	}
	state, err := e.Inspect(ctx, r)
	if !errors.Is(err, ErrPartial) || state.Next == nil {
		return Admission{}, ErrBlocked
	}
	admission, err := e.remote.AdmitFirstCreate(ctx, *state.Next, fresh)
	if err != nil {
		return Admission{}, err
	}
	if r.ExistingMilestone != nil {
		copy := *r.ExistingMilestone
		r.ExistingMilestone = &copy
	}
	return Admission{p: &permit{request: r, step: *state.Next, digest: state.InputSHA256, gateway: admission}}, nil
}
func (e *Engine) Apply(ctx context.Context, a Admission) (State, error) {
	if a.p == nil || !a.p.used.CompareAndSwap(false, true) {
		return State{}, ErrBlocked
	}
	state, err := e.Inspect(ctx, a.p.request)
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, ErrPartial) || state.Next == nil || state.InputSHA256 != a.p.digest || digest(state.Next) != digest(a.p.step) {
		return State{}, ErrBlocked
	}
	if _, err = e.remote.ApplyFirst(ctx, a.p.step, a.p.gateway); err != nil {
		return State{}, err
	}
	return e.Inspect(ctx, a.p.request)
}
