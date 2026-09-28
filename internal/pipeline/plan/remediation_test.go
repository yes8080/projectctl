package plan

import (
	"context"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func remediationRecord(t *testing.T, f *publicationFixture, ref protocol.Reference) protocol.Record {
	t.Helper()
	v, err := f.gateway(t).FetchRecord(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Record()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHistoricalPlannerRunBudgetPolicy(t *testing.T) {
	for _, mode := range []string{"timeout expanded", "timeout narrowed", "total runs expanded", "total runs narrowed", "wall", "planning rounds", "implementation rounds", "design rounds", "defects", "changes", "cost", "currency", "attempt"} {
		t.Run(mode, func(t *testing.T) {
			f, r, run, review, approval := reviewFixture(t)
			plannerRun := remediationRecord(t, f, r.Publication.PlannerRun).(*protocol.Run)
			plannerRun.OperationID += "-new"
			switch mode {
			case "timeout expanded":
				plannerRun.Budget.MaxRunSeconds++
			case "timeout narrowed":
				plannerRun.Budget.MaxRunSeconds--
			case "total runs expanded":
				plannerRun.Budget.MaxAgentRuns++
			case "total runs narrowed":
				plannerRun.Budget.MaxAgentRuns--
			case "wall":
				plannerRun.Budget.MaxWallSeconds--
			case "planning rounds":
				plannerRun.Budget.MaxPlanningRounds--
			case "implementation rounds":
				plannerRun.Budget.MaxImplementationRounds--
			case "design rounds":
				plannerRun.Budget.MaxDesignRounds--
			case "defects":
				plannerRun.Budget.MaxDefectFixIssues++
			case "changes":
				plannerRun.Budget.MaxChangeRequests++
			case "cost":
				plannerRun.Budget.MaxCostMinorUnits++
			case "currency":
				plannerRun.Budget.Currency = "EUR"
			case "attempt":
				plannerRun.Attempt = plannerRun.Budget.MaxPlanningRounds + 1
			}
			r.Publication.PlannerRun = f.record(t, plannerRun, publicationIdentity(1), 8)
			before := f.postCount()
			view, err := f.engine(t).ReviewInput(context.Background(), r.Publication)
			if mode != "timeout narrowed" && err != nil {
				return
			}
			if err != nil {
				t.Fatal("valid timeout narrowing rejected", err)
			}
			run.Inputs, run.InputSHA256 = view.Inputs, view.InputSHA256
			review.Run = f.record(t, &run, publicationIdentity(1), 2)
			r.Review = f.record(t, &review, publicationIdentity(3), 2)
			approval.Candidate = r.Review
			r.Approval = f.record(t, &approval, publicationIdentity(1), 2)
			_, err = f.engine(t).Activate(context.Background(), r)
			if (err == nil) != (mode == "timeout narrowed") {
				t.Fatalf("policy-inconsistent historical Run accepted or valid narrowing blocked: %v", err)
			}
			if f.postCount() != before {
				t.Fatal("read-only budget check wrote")
			}
		})
	}
}

func TestDisplayLoginDoesNotChangeReviewIdentity(t *testing.T) {
	for _, mode := range []string{"Plan", "all native comments", "caller references"} {
		t.Run(mode, func(t *testing.T) {
			f, r, _, _, _ := reviewFixture(t)
			before, err := f.engine(t).ReviewInput(context.Background(), r.Publication)
			if err != nil {
				t.Fatal(err)
			}
			posts := f.postCount()
			if mode == "caller references" {
				r.Publication.Input.Baseline.Author.Login = "renamed-controller"
				r.Publication.PlannerRun.Author.Login = "renamed-controller"
				r.Plan.Author.Login = "renamed-controller"
				r.Review.Author.Login = "renamed-reviewer"
				r.Approval.Author.Login = "renamed-controller"
			} else {
				f.mu.Lock()
				for _, list := range f.comments {
					for _, c := range list {
						if mode == "all native comments" || c["id"] == r.Plan.DatabaseID {
							a := c["user"].(protocol.GitHubIdentity)
							a.Login = "renamed-display"
							c["user"] = a
						}
					}
				}
				f.mu.Unlock()
			}
			after, err := f.engine(t).ReviewInput(context.Background(), r.Publication)
			if err != nil || before.InputSHA256 != after.InputSHA256 {
				t.Fatalf("display name changed review identity: %v", err)
			}
			if _, err := f.engine(t).CheckFrozen(context.Background(), r); err != nil {
				t.Fatal("display name blocked historical activation", err)
			}
			if f.postCount() != posts {
				t.Fatal("display-only replay wrote")
			}
		})
	}
}

func TestBootstrapPrincipalCanReviewWithSeparateAgentInstance(t *testing.T) {
	f, r, run, review, approval := reviewFixture(t)
	f.policy.Grants[0].Roles = []protocol.Role{protocol.RoleController, protocol.RolePublisher, protocol.RolePlanReviewer}
	r.Publication.Authorization = f.source(t, "docs/plan-policy-bootstrap.json", f.policy)
	view, err := f.engine(t).ReviewInput(context.Background(), r.Publication)
	if err != nil {
		t.Fatal(err)
	}
	run.Inputs, run.InputSHA256 = view.Inputs, view.InputSHA256
	review.Run = f.record(t, &run, publicationIdentity(1), 2)
	r.Review = f.record(t, &review, publicationIdentity(1), 2)
	approval.Candidate = r.Review
	approval.PolicySHA256, _ = f.policy.Digest()
	r.Approval = f.record(t, &approval, publicationIdentity(1), 2)
	if _, err := f.engine(t).Activate(context.Background(), r); err != nil {
		t.Fatal("explicit bootstrap role grant rejected", err)
	}
	run.AgentInstance = "planner-instance"
	review.Run = f.record(t, &run, publicationIdentity(1), 2)
	r.Review = f.record(t, &review, publicationIdentity(1), 2)
	approval.Candidate = r.Review
	r.Approval = f.record(t, &approval, publicationIdentity(1), 2)
	if _, err := f.engine(t).Activate(context.Background(), r); err == nil {
		t.Fatal("bootstrap grant bypassed independent Agent instance")
	}
}

func TestIdentityProjectionRetainsAuthorAndContentPins(t *testing.T) {
	f, r, _, _, _ := reviewFixture(t)
	before := digest(r)
	renamed := r
	renamed.Plan.Author.Login = "renamed"
	if digest(renamed) != before || r.Plan.Author.Login == "renamed" {
		t.Fatal("display projection changed authority or mutated input")
	}
	for name, change := range map[string]func(*ReviewRequest){
		"database ID":     func(v *ReviewRequest) { v.Plan.DatabaseID++ },
		"node ID":         func(v *ReviewRequest) { v.Plan.NodeID += "-new" },
		"author ID":       func(v *ReviewRequest) { v.Plan.Author.ID++ },
		"author node":     func(v *ReviewRequest) { v.Plan.Author.NodeID += "-new" },
		"author type":     func(v *ReviewRequest) { v.Plan.Author.Type = "Bot" },
		"body":            func(v *ReviewRequest) { v.Plan.BodySHA256 = protocol.SHA256([]byte("changed")) },
		"canonical":       func(v *ReviewRequest) { v.Plan.CanonicalSHA256 = protocol.SHA256([]byte("changed")) },
		"policy body pin": func(v *ReviewRequest) { v.Publication.Authorization.SHA256 = protocol.SHA256([]byte("changed")) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := r
			change(&changed)
			if digest(changed) == before {
				t.Fatal("stable authority/content pin omitted")
			}
		})
	}
	// A real policy-source body change, even display-only, is still immutable
	// source drift rather than a fresh native-author display observation.
	f.mu.Lock()
	blob := f.sources[r.Publication.Authorization]
	blob.Bytes = append(blob.Bytes, ' ')
	f.sources[r.Publication.Authorization] = blob
	f.mu.Unlock()
	if _, err := f.engine(t).CheckFrozen(context.Background(), r); err == nil {
		t.Fatal("edited pinned policy source accepted")
	}
}

func TestReviewerTimeoutMayNarrowWithoutChangingProjectBudget(t *testing.T) {
	f, r, run, review, approval := reviewFixture(t)
	run.Budget.MaxRunSeconds--
	review.Run = f.record(t, &run, publicationIdentity(1), 2)
	r.Review = f.record(t, &review, publicationIdentity(3), 2)
	approval.Candidate = r.Review
	r.Approval = f.record(t, &approval, publicationIdentity(1), 2)
	if _, err := f.engine(t).Activate(context.Background(), r); err != nil {
		t.Fatal("safe reviewer timeout narrowing rejected", err)
	}
}
