package startup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

var capabilityNames = []string{"issues_read", "comments_read", "milestones_read", "dependencies_read", "issues_enabled", "delete_branch_on_merge", "auto_merge", "push_permission", "admin_permission", "issue_auto_close", "protected_merge_gate"}
var hostRequirements = []string{"runtime", "workspace_isolation", "publisher_isolation", "role_identities", "cancellation", "usage_budget", "cost_bound", "acceptance_environment"}

func knownCapability(name string) bool {
	for _, v := range capabilityNames {
		if v == name {
			return true
		}
	}
	return false
}
func capabilities(c gh.Capabilities) map[string]gh.Capability {
	return map[string]gh.Capability{"issues_read": c.IssuesRead, "comments_read": c.CommentsRead, "milestones_read": c.MilestonesRead, "dependencies_read": c.DependenciesRead, "issues_enabled": c.IssuesEnabled, "delete_branch_on_merge": c.DeleteBranchOnMergeEnabled, "auto_merge": c.AutoMergeEnabled, "push_permission": c.PushPermission, "admin_permission": c.AdminPermission, "issue_auto_close": c.IssueAutoCloseEnabled, "protected_merge_gate": c.ProtectedMergeGate}
}
func observed(c gh.Capability) gh.Capability {
	if c.State != gh.Supported && c.State != gh.Unsupported {
		c.State = gh.Unknown
	}
	// Adapter diagnostics may contain private API/provider text. Public reasons
	// are deterministic codes; credentials and unbounded errors are not copied.
	c.Reason = "trusted observation: " + string(c.State)
	return c
}
func initialState() State {
	return State{Status: "blocked", Phase: "startup", BootstrapAdapter: BootstrapIntegration, Reasons: []Reason{}, Inputs: []FixedInput{}, GitHubCapabilities: map[string]gh.Capability{}, HostCapabilities: map[string]gh.Capability{}}
}
func block(s State, code, subject string, err error) (State, error) {
	s.Status = "blocked"
	s.Reasons = append(s.Reasons, Reason{code, subject})
	return s, err
}

type loaded struct {
	manifest Manifest
	request  BootstrapRequest
}

func (e *Engine) load(ctx context.Context, anchor Anchor) (State, loaded, error) {
	s := initialState()
	var value loaded
	if !validRepo(anchor.Repository) || !validSource(anchor.Manifest) {
		s, err := block(s, "invalid_anchor", "manifest", ErrBlocked)
		return s, value, err
	}
	repo, err := e.remote.ReadRepository(ctx)
	if err != nil {
		s, err := block(s, "repository_unavailable", "repository", ErrBlocked)
		return s, value, err
	}
	want := gh.Resource{Kind: "repository", DatabaseID: anchor.Repository.DatabaseID, NodeID: anchor.Repository.NodeID}
	if repo.Resource != want || repo.URL != "https://github.com/"+anchor.Repository.Owner+"/"+anchor.Repository.Name || repo.DefaultBranch == "" {
		s, err := block(s, "repository_identity_drift", "repository", ErrConflict)
		return s, value, err
	}
	blob, err := e.remote.ReadSource(ctx, anchor.Manifest)
	if err != nil || !matchesBlob(blob, anchor.Manifest) {
		s, err := block(s, "manifest_unavailable_or_drifted", anchor.Manifest.Path, ErrBlocked)
		return s, value, err
	}
	m, err := DecodeManifest(blob.Bytes)
	if err != nil || m.Repository != anchor.Repository {
		s, err := block(s, "invalid_startup_policy", "manifest", ErrBlocked)
		return s, value, err
	}
	s.ManifestSHA256 = anchor.Manifest.SHA256
	policyData, _ := json.Marshal(m.Policy)
	s.PolicySHA256, _ = protocol.DigestJSON(policyData)
	actor, err := e.remote.CurrentUser(ctx)
	if err != nil || !sameIdentity(actor, m.Policy.Roles.Controller) {
		s, err := block(s, "controller_identity_unverified", "controller", ErrBlocked)
		return s, value, err
	}
	for _, input := range m.Inputs {
		data, err := e.remote.ReadSource(ctx, input.Source)
		if err != nil || !matchesBlob(data, input.Source) {
			s, err := block(s, "input_unavailable_or_drifted", input.Source.Path, ErrBlocked)
			return s, value, err
		}
		s.Inputs = append(s.Inputs, FixedInput{Kind: input.Kind, Repository: anchor.Repository, Source: input.Source, BlobSHA: data.BlobSHA})
	}
	control := ControlRecord{Schema: ControlSchema, Kind: "project_control", Repository: m.Repository, Cycle: m.Cycle, OperationID: m.OperationID, Manifest: anchor.Manifest}
	encoded, _ := json.Marshal(control)
	body, _ := protocol.CanonicalJSON(encoded)
	value = loaded{manifest: m, request: BootstrapRequest{Repository: m.Repository, Cycle: m.Cycle, OperationID: m.OperationID, Title: "Project control: " + m.Cycle, Body: ControlMarker + " " + string(body), Publisher: actor}}
	value.request.BodySHA256 = protocol.SHA256([]byte(value.request.Body))
	return s, value, nil
}

func matchesBlob(blob gh.SourceBlob, ref protocol.SourceReference) bool {
	return blob.Reference == ref && sha40.MatchString(blob.BlobSHA) && len(blob.Bytes) > 0 && protocol.SHA256(blob.Bytes) == ref.SHA256
}

func (e *Engine) reconcile(ctx context.Context, anchor Anchor) (State, loaded, error) {
	s, value, err := e.load(ctx, anchor)
	if err != nil {
		return s, value, err
	}
	issues, err := e.remote.ListIssues(ctx)
	if err != nil {
		s, err := block(s, "control_discovery_unavailable", "repository", ErrBlocked)
		return s, value, err
	}
	active := []gh.Issue{}
	closedSame := false
	seenIDs := map[int64]bool{}
	seenNodes := map[string]bool{}
	for _, issue := range issues {
		if issue.Content.Body == nil || !strings.Contains(*issue.Content.Body, ControlMarker) {
			continue
		}
		if issue.Kind != "issue" || issue.Number <= 0 || issue.DatabaseID <= 0 || issue.NodeID == "" || seenIDs[issue.DatabaseID] || seenNodes[issue.NodeID] {
			s, err := block(s, "control_native_identity_conflict", "control_issue", ErrConflict)
			return s, value, err
		}
		seenIDs[issue.DatabaseID] = true
		seenNodes[issue.NodeID] = true
		if issue.State == "open" {
			active = append(active, issue)
		} else if issue.State == "closed" {
			var record ControlRecord
			if decodeControl(*issue.Content.Body, &record) == nil && (record.Cycle == value.manifest.Cycle || record.OperationID == value.manifest.OperationID) {
				closedSame = true
			}
		} else {
			s, err := block(s, "control_state_unknown", "control_issue", ErrBlocked)
			return s, value, err
		}
	}
	if len(active) > 1 {
		s, err := block(s, "duplicate_active_control_issues", "repository", ErrConflict)
		return s, value, err
	}
	if closedSame {
		s, err := block(s, "cycle_already_closed", "control_issue", ErrBlocked)
		return s, value, err
	}
	probeIssue := int64(0)
	if len(active) == 1 {
		issue := active[0]
		var record ControlRecord
		if err := decodeControl(*issue.Content.Body, &record); err != nil || record.Schema != ControlSchema || record.Kind != "project_control" || record.Repository != anchor.Repository || record.Cycle != value.manifest.Cycle || record.OperationID != value.manifest.OperationID || record.Manifest != anchor.Manifest || *issue.Content.Body != value.request.Body || issue.Content.BodySHA256 != value.request.BodySHA256 || !sameIdentity(issue.Author, value.manifest.Policy.Roles.Controller) || issue.URL != fmt.Sprintf("https://github.com/%s/%s/issues/%d", anchor.Repository.Owner, anchor.Repository.Name, issue.Number) {
			s, err := block(s, "control_record_drift", "control_issue", ErrConflict)
			return s, value, err
		}
		resource := issue.Resource
		s.Control = &resource
		probeIssue = issue.Number
	}
	for name, cap := range capabilities(e.remote.ProbeCapabilities(ctx, probeIssue)) {
		s.GitHubCapabilities[name] = observed(cap)
	}
	for _, name := range value.manifest.Policy.RequiredGitHubCapabilities {
		if !s.GitHubCapabilities[name].IsSupported() {
			s.Reasons = append(s.Reasons, Reason{"github_capability_" + string(s.GitHubCapabilities[name].State), name})
		}
	}
	hostCaps := map[string]gh.Capability{}
	if e.host != nil {
		environment := value.manifest.Policy.Environment
		environment.OperatingSystems = append([]string(nil), environment.OperatingSystems...)
		hostCaps = e.host.Probe(ctx, HostRequest{Environment: environment, Roles: value.manifest.Policy.Roles, Budget: value.manifest.Policy.Budget})
	}
	for _, name := range hostRequirements {
		cap := observed(hostCaps[name])
		s.HostCapabilities[name] = cap
		if !cap.IsSupported() {
			s.Reasons = append(s.Reasons, Reason{"host_capability_" + string(cap.State), name})
		}
	}
	sort.Slice(s.Reasons, func(i, j int) bool {
		if s.Reasons[i].Code != s.Reasons[j].Code {
			return s.Reasons[i].Code < s.Reasons[j].Code
		}
		return s.Reasons[i].Subject < s.Reasons[j].Subject
	})
	if len(s.Reasons) > 0 {
		return s, value, ErrBlocked
	}
	if s.Control == nil {
		s.Status = "bootstrap_required"
		s.Reasons = append(s.Reasons, Reason{"control_issue_missing", "repository"})
		return s, value, nil
	}
	s.Status = "ready_for_design"
	s.Phase = "design"
	return s, value, nil
}

func decodeControl(body string, out *ControlRecord) error {
	if !strings.HasPrefix(body, ControlMarker) || strings.Count(body, ControlMarker) != 1 {
		return fmt.Errorf("invalid control marker")
	}
	return strictDecode([]byte(strings.TrimSpace(strings.TrimPrefix(body, ControlMarker))), out)
}

// Reconcile reconstructs from GitHub/Git objects and current host observations.
// No cached State, previous successful read, or stored admission is accepted.
func (e *Engine) Reconcile(ctx context.Context, anchor Anchor) (State, error) {
	s, _, err := e.reconcile(ctx, anchor)
	return s, err
}

func (e *Engine) CheckProposal(ctx context.Context, anchor Anchor, artifact Artifact) (State, error) {
	s, value, err := e.reconcile(ctx, anchor)
	if err != nil {
		return s, err
	}
	if s.Status != "ready_for_design" || (artifact.Role != protocol.RoleDesigner && artifact.Role != protocol.RoleDesignReviewer) || (artifact.Kind != "input_document" && artifact.Kind != "design_document" && artifact.Kind != "startup_document") {
		return block(s, "product_execution_not_authorized", "proposal", ErrBlocked)
	}
	for _, file := range value.manifest.Policy.DesignFiles {
		if artifact.Path == file {
			return s, nil
		}
	}
	return block(s, "design_artifact_out_of_scope", artifact.Path, ErrBlocked)
}
