package github

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

// Native authors are deliberately shared here: this graph boundary verifies
// exact remote references, not the higher-level independent activation gate.
type planAcceptanceGraph struct {
	objects      map[int64]map[string]any
	member       map[string]any
	plan         *protocol.Plan
	planRef      protocol.Reference
	baselineRef  protocol.Reference
	authorRun    *protocol.Run
	authorRunRef protocol.Reference
	reviewerRun  *protocol.Run
	review       *protocol.PlanAcceptance
	decision     *protocol.Approval
	candidate    protocol.Reference
	root         protocol.Reference
}

func newPlanAcceptanceGraph(t *testing.T) *planAcceptanceGraph {
	t.Helper()
	previous := newAcceptanceGraph(t)
	f := &planAcceptanceGraph{objects: previous.objects, member: testServerFixtureIssue(4, nil)}
	header := func(kind protocol.Kind, op string) protocol.Envelope {
		return protocol.Envelope{Schema: protocol.Schema, Kind: kind, Project: testServerFixtureProject(), Subject: protocol.Subject{Milestone: 1}, OperationID: op, Version: 1}
	}
	source := protocol.SourceReference{Commit: strings.Repeat("b", 40), Path: "docs/design.md", SHA256: strings.Repeat("a", 64)}
	baseline := &protocol.Baseline{Envelope: header(protocol.KindBaseline, "baseline"), Inputs: []protocol.SourceReference{source}, Design: source, PolicySHA256: strings.Repeat("a", 64), Review: previous.candidate, Approval: previous.root}
	baseline.Subject = protocol.Subject{Issue: 1}
	f.objects[41], f.baselineRef = recordNative(t, baseline, 41)
	_, contractRef := recordNative(t, gatewayContract(), 31)
	budget := protocol.Budget{MaxAgentRuns: 10, MaxRunSeconds: 60, MaxWallSeconds: 600, MaxDesignRounds: 2, MaxPlanningRounds: 2, MaxImplementationRounds: 3, Currency: "USD"}
	f.plan = &protocol.Plan{Envelope: header(protocol.KindPlan, "plan"), Baseline: f.baselineRef, Members: []protocol.Member{{Issue: 4, IssueDatabaseID: 1004, IssueNodeID: "I_4", Contract: contractRef}}, NativeTopologySHA256: strings.Repeat("a", 64), Budget: budget, Completion: []string{"all required checks"}}
	f.objects[42], f.planRef = recordNative(t, f.plan, 42)
	f.authorRun = &protocol.Run{Envelope: header(protocol.KindRun, "planner-run"), Role: protocol.RolePlanner, AgentInstance: "planner-instance", Host: "test", Inputs: []protocol.Reference{f.baselineRef}, InputSHA256: strings.Repeat("a", 64), AssignmentGeneration: 1, Attempt: 1, Budget: budget}
	f.objects[43], f.authorRunRef = recordNative(t, f.authorRun, 43)
	f.reviewerRun = &protocol.Run{Envelope: header(protocol.KindRun, "plan-reviewer-run"), Role: protocol.RolePlanReviewer, AgentInstance: "plan-reviewer-instance", Host: "test", Inputs: []protocol.Reference{f.planRef, f.authorRunRef}, InputSHA256: strings.Repeat("a", 64), AssignmentGeneration: 1, Attempt: 1, Budget: budget}
	var runRef protocol.Reference
	f.objects[44], runRef = recordNative(t, f.reviewerRun, 44)
	f.review = &protocol.PlanAcceptance{Envelope: header(protocol.KindPlanAcceptance, "plan-review"), Candidate: f.planRef, Run: runRef, Assessments: []protocol.Assessment{}, UnresolvedFindings: []protocol.Reference{}, Decision: "PASS", Reason: "reviewed exact published Plan"}
	for _, criterion := range []string{"coverage", "dag", "acceptance", "environment", "budgets", "completion"} {
		f.review.Assessments = append(f.review.Assessments, protocol.Assessment{CriterionID: criterion, Result: "PASS", Reason: "observed exact Plan data", Evidence: []protocol.Reference{f.planRef}})
	}
	f.pinReview(t)
	return f
}

func (f *planAcceptanceGraph) pinReview(t *testing.T) {
	t.Helper()
	f.objects[45], f.candidate = recordNative(t, f.review, 45)
	f.decision = gatewayApproval(t, f.candidate, f.review.Subject, "plan-review-approval")
	f.pinDecision(t)
}

func (f *planAcceptanceGraph) pinDecision(t *testing.T) {
	t.Helper()
	f.objects[46], f.root = recordNative(t, f.decision, 46)
}

func (f *planAcceptanceGraph) gateway(t *testing.T, before func(int64)) *Gateway {
	t.Helper()
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("graph attempted an external write")
		}
		if r.URL.Path == "/repos/octo/pipeline/issues/4" {
			testServerFixtureJSON(t, w, f.member)
			return
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/octo/pipeline/issues/comments/"), 10, 64)
		if before != nil {
			before(id)
		}
		if item, ok := f.objects[id]; ok {
			testServerFixtureJSON(t, w, item)
		} else {
			http.NotFound(w, r)
		}
	})
	return g
}

func TestResolveApprovedPlanAcceptanceExactGraph(t *testing.T) {
	f := newPlanAcceptanceGraph(t)
	newer := *f.review
	newer.OperationID, newer.Version, newer.Reason = "unapproved-plan-review-v2", 2, "not approved"
	f.objects[47], _ = recordNative(t, &newer, 47)
	newerPlan := *f.plan
	newerPlan.OperationID, newerPlan.Version = "unapproved-plan-v2", 2
	f.objects[48], _ = recordNative(t, &newerPlan, 48)
	for i := 0; i < 2; i++ { // Reconstruct from the remote graph, without a cache.
		g := f.gateway(t, nil)
		chain, err := g.ResolveApproved(context.Background(), f.root, gatewayPolicy())
		if err != nil || chain.Candidate.Reference() != f.candidate || len(chain.Records) != 11 {
			t.Fatalf("exact complete graph not resolved: records=%d err=%v", len(chain.Records), err)
		}
		for _, record := range chain.Records {
			if record.Reference().DatabaseID >= 47 {
				t.Fatal("unapproved newer revision entered the graph")
			}
		}
	}
	delete(f.objects, 45)
	if _, err := f.gateway(t, nil).ResolveApproved(context.Background(), f.root, gatewayPolicy()); err == nil {
		t.Fatal("newer unapproved review substituted for deleted pinned review")
	}
}

func TestResolveApprovedPlanAcceptanceRejectsInvalidGraph(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *planAcceptanceGraph, *protocol.Policy)
		want   string
	}{
		{"candidate wrong kind", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.review.Candidate = f.baselineRef
			f.pinReview(t)
		}, "wrong record kind; want plan"},
		{"run wrong kind", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.review.Run = f.planRef
			f.pinReview(t)
		}, "wrong record kind; want run"},
		{"candidate milestone drift", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.review.Subject.Milestone = 2
			f.pinReview(t)
		}, "another milestone"},
		{"approval milestone drift", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.decision.Subject.Milestone = 2
			f.pinDecision(t)
		}, ""},
		{"FAIL", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.review.Decision = "FAIL"
			f.pinReview(t)
		}, "formal PASS"},
		{"FAIL with findings", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.review.Decision = "FAIL"
			f.review.UnresolvedFindings = []protocol.Reference{f.authorRunRef}
			f.pinReview(t)
		}, "formal PASS"},
		{"edited review", func(_ *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.objects[45]["body"] = f.objects[45]["body"].(string) + "\n"
		}, ""},
		{"edited Plan", func(_ *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.objects[42]["body"] = f.objects[42]["body"].(string) + "\n"
		}, ""},
		{"review native author drift", func(_ *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			author := testServerFixtureAuthor()
			author.ID++
			f.objects[45]["user"] = author
		}, ""},
		{"review native node drift", func(_ *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.objects[45]["node_id"] = "IC_replaced"
		}, ""},
		{"Plan native ID drift", func(_ *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.objects[42]["id"] = 999
		}, ""},
		{"member native identity drift", func(_ *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.member["node_id"] = "I_replaced"
		}, "native identity drift"},
		{"deleted author Run", func(_ *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			delete(f.objects, 43)
		}, ""},
		{"deleted transitive baseline review Run", func(_ *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			delete(f.objects, 33)
		}, ""},
		{"policy drift", func(_ *testing.T, _ *planAcceptanceGraph, p *protocol.Policy) {
			p.Version++
		}, ""},
		{"unauthorized native approver", func(_ *testing.T, _ *planAcceptanceGraph, p *protocol.Policy) {
			p.Grants[0].Roles = []protocol.Role{protocol.RolePlanReviewer}
		}, "not authorized"},
		{"operation conflict", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.reviewerRun.OperationID = f.plan.OperationID
			f.objects[44], f.review.Run = recordNative(t, f.reviewerRun, 44)
			f.pinReview(t)
		}, "operation_id"},
		{"two pins for Plan", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.review.Assessments[0].Evidence[0].BodySHA256 = strings.Repeat("f", 64)
			f.pinReview(t)
		}, "two pins"},
		{"cyclic Run input", func(t *testing.T, f *planAcceptanceGraph, _ *protocol.Policy) {
			f.reviewerRun.Inputs = append(f.reviewerRun.Inputs, f.candidate)
			f.objects[44], f.review.Run = recordNative(t, f.reviewerRun, 44)
			f.pinReview(t)
		}, "cyclic"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f, policy := newPlanAcceptanceGraph(t), gatewayPolicy()
			test.change(t, f, &policy)
			_, err := f.gateway(t, nil).ResolveApproved(context.Background(), f.root, policy)
			if err == nil || (test.want != "" && !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("invalid graph was not rejected at expected boundary %q: %v", test.want, err)
			}
		})
	}
}

func TestPlanAcceptanceRevalidatesAfterTraversal(t *testing.T) {
	f := newPlanAcceptanceGraph(t)
	reads := 0
	g := f.gateway(t, func(id int64) {
		if id == 45 {
			reads++
			if reads == 2 {
				f.objects[id]["body"] = f.objects[id]["body"].(string) + "\n"
			}
		}
	})
	if _, err := g.ResolveApproved(context.Background(), f.root, gatewayPolicy()); err == nil || reads != 2 {
		t.Fatalf("mid-traversal review edit hidden: reads=%d err=%v", reads, err)
	}
}
