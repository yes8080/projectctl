package plan

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/design"
	"github.com/yes8080/projectctl/internal/pipeline/planner"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

// This test validates committed plan-boundary DTOs, not startup readiness,
// native credential separation, a live mutation, or an acceptance decision.
func TestPlanLiveFixtureBundle(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile("testdata/live/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := protocol.CanonicalJSON(data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return data
	}
	var bundle struct {
		Candidate         json.RawMessage          `json:"candidate"`
		Inputs            json.RawMessage          `json:"inputs"`
		Authorization     json.RawMessage          `json:"authorization"`
		DesignSource      protocol.SourceReference `json:"design_source"`
		DesignPullRequest int64                    `json:"design_pull_request"`
	}
	decode := func(data []byte, out any) {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	decode(read("bundle.json"), &bundle)
	for name, value := range map[string]json.RawMessage{"candidate.json": bundle.Candidate, "inputs.json": bundle.Inputs, "authorization.json": bundle.Authorization} {
		file, _ := protocol.CanonicalJSON(read(name))
		aggregate, err := protocol.CanonicalJSON(value)
		if err != nil || !bytes.Equal(file, aggregate) {
			t.Fatalf("bundle and %s differ: %v", name, err)
		}
	}
	candidate, err := planner.DecodeCandidate(bundle.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	var input planner.Inputs
	decode(bundle.Inputs, &input)
	documentBytes, _ := json.Marshal(input.Design)
	document, err := design.DecodeDocument(documentBytes)
	if err != nil || len(document.OpenQuestions) != 0 {
		t.Fatalf("invalid fixed plan-stage design DTO: %v", err)
	}
	summary, err := planner.ValidateCandidate(candidate, document.Requirements, input.Manifest.Policy.Environment)
	if err != nil || len(candidate.Contracts) != 1 || len(summary.Edges) != 0 || summary.Integration != "integration" || len(document.Requirements) != 1 || document.Requirements[0].ID != "integration" {
		t.Fatalf("fixture is not the minimal complete one-slice plan: %#v %v", summary, err)
	}
	policy, err := protocol.DecodePolicy(bundle.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	wantPrincipal := protocol.GitHubIdentity{ID: 515784, NodeID: "MDQ6VXNlcjUxNTc4NA==", Login: "yes8080", Type: "User"}
	wantProject := protocol.Project{Owner: "yes8080", Repo: "projectctl", ControlIssue: 2}
	if policy.Project != wantProject || len(policy.Grants) != 1 || policy.Grants[0].Principal != wantPrincipal || !reflect.DeepEqual(policy.Grants[0].Roles, []protocol.Role{protocol.RoleController, protocol.RolePublisher, protocol.RolePlanReviewer, protocol.RolePlanner}) {
		t.Fatal("fixture grants authority beyond the single real Controller principal")
	}
	roles := input.Manifest.Policy.Roles
	for _, principal := range []protocol.GitHubIdentity{roles.Controller, roles.Designer, roles.DesignReviewer, roles.Developer, roles.Acceptor} {
		if principal != wantPrincipal {
			t.Fatal("fixture invented a native role identity")
		}
	}
	manifestBytes, _ := json.Marshal(input.Manifest)
	if _, err := startup.DecodeManifest(manifestBytes); err == nil {
		t.Fatal("single-publisher fixture must not silently become a startup-ready manifest")
	}
	budget := input.Manifest.Policy.Budget
	if budget.MaxAgentRuns != 12 || budget.MaxRunSeconds != 120 || budget.MaxWallSeconds != 1800 || budget.MaxDesignRounds != 1 || budget.MaxPlanningRounds != 1 || budget.MaxImplementationRounds != 1 || budget.MaxDefectFixIssues != 0 || budget.MaxChangeRequests != 0 || budget.MaxCostMinorUnits != 0 || budget.Currency != "USD" {
		t.Fatal("fixed finite fixture budgets changed")
	}
	if !reflect.DeepEqual(input.Manifest.Policy.TerminalConditions, []string{"completed", "blocked", "budget_exceeded", "failed", "paused", "cancelled"}) {
		t.Fatal("fixture terminal conditions incomplete")
	}
	if input.SHA256 != "" {
		t.Fatal("input identity must be computed after decoding, not supplied as authority")
	}
	identityBytes, _ := json.Marshal(struct {
		Design   design.Document
		Manifest startup.Manifest
	}{input.Design, input.Manifest})
	identityDigest, err := protocol.DigestJSON(identityBytes)
	if err != nil || len(identityDigest) != 64 {
		t.Fatal("fixed DTO identity cannot be computed", err)
	}
	wantSource := protocol.SourceReference{Commit: "1da6dfa438d4deaa13d42475e33fe84492c3d40b", Path: "docs/pipeline-design.md", SHA256: "3c5d9585a9fec44455af7a2a4c2a4090a6d78b297cd6f07a196a09bd2a5184c8"}
	if bundle.DesignPullRequest != 3 || bundle.DesignSource != wantSource || len(input.Manifest.Inputs) != 1 || input.Manifest.Inputs[0].Source != wantSource {
		t.Fatal("historical Markdown source pin changed")
	}
	var blueprint struct {
		Project protocol.Project `json:"project"`
		Limits  struct {
			Comments   int `json:"new_comments"`
			Issues     int `json:"new_issues"`
			Edges      int `json:"new_dependency_edges"`
			Milestones int `json:"new_milestones"`
		} `json:"limits"`
	}
	// Blueprint also contains descriptive scope and read-only existing-resource
	// pins; it is intentionally not decoded as an executable Request.
	if err := json.Unmarshal(read("request-blueprint.json"), &blueprint); err != nil {
		t.Fatal(err)
	}
	if blueprint.Project != wantProject || blueprint.Limits.Comments != 24 || blueprint.Limits.Issues != 3 || blueprint.Limits.Edges != 2 || blueprint.Limits.Milestones != 0 {
		t.Fatal("live mutation caps or target project changed")
	}
}
