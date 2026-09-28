package plan

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/planner"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func (f *publicationFixture) Replay(ctx context.Context, r planner.Request) (planner.Inputs, error) {
	return f.Read(ctx, r)
}

func reviewFixture(t *testing.T) (*publicationFixture, ReviewRequest, protocol.Run, protocol.PlanAcceptance, protocol.Approval) {
	t.Helper()
	f, r := newPublicationFixture(t)
	state := f.publishAll(t, r)
	view, err := f.engine(t).ReviewInput(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	run := protocol.Run{Envelope: publicationEnvelope(protocol.KindRun, state.Plan.Subject, "reviewer-run"), Role: protocol.RolePlanReviewer, AgentInstance: "separate-reviewer-instance", Host: "isolated-review-host", Inputs: view.Inputs, InputSHA256: view.InputSHA256, AssignmentGeneration: 1, Attempt: 1, Budget: state.Plan.Budget}
	runRef := f.record(t, &run, publicationIdentity(1), 2)
	review := protocol.PlanAcceptance{Envelope: publicationEnvelope(protocol.KindPlanAcceptance, state.Plan.Subject, "plan-review"), Candidate: *state.PlanReference, Run: runRef, Assessments: []protocol.Assessment{}, UnresolvedFindings: []protocol.Reference{}, Decision: "PASS", Reason: "independent complete review"}
	for _, id := range reviewChecks {
		review.Assessments = append(review.Assessments, protocol.Assessment{CriterionID: id, Result: "PASS", Reason: "checked exact native plan facts", Evidence: []protocol.Reference{*state.PlanReference}})
	}
	reviewRef := f.record(t, &review, publicationIdentity(3), 2)
	policySHA, _ := f.policy.Digest()
	approval := protocol.Approval{Envelope: publicationEnvelope(protocol.KindApproval, state.Plan.Subject, "plan-approval"), Candidate: reviewRef, Decision: "APPROVED", Reason: "approve exact independent review", PolicySHA256: policySHA}
	approvalRef := f.record(t, &approval, publicationIdentity(1), 2)
	return f, ReviewRequest{Publication: r, Plan: *state.PlanReference, Review: reviewRef, Approval: approvalRef}, run, review, approval
}

func TestReviewExactIndependentActivationAndColdRecovery(t *testing.T) {
	f, r, _, _, _ := reviewFixture(t)
	before := f.postCount()
	first, err := f.engine(t).Activate(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.engine(t).Activate(context.Background(), r)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("restart changed frozen result", err)
	}
	if len(first.Plan.Members) != 2 || first.Plan.NativeTopologySHA256 == "" || !reference(first.Approval, r.Approval) || f.postCount() != before {
		t.Fatal("incomplete or mutating activation")
	}
	if _, err = f.engine(t).CheckFrozen(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	// Normal completion progress is not a dependency/member change. A later
	// head's ancestry is separately proven by the Controller, not this gate.
	f.mu.Lock()
	f.issues[first.Plan.Members[0].Issue]["state"] = "closed"
	f.mu.Unlock()
	if _, err = f.engine(t).Activate(context.Background(), r); err == nil {
		t.Fatal("initial activation accepted closed unpublished member")
	}
	if _, err = f.engine(t).CheckFrozen(context.Background(), r); err != nil {
		t.Fatal("normal completed member invalidated frozen topology", err)
	}
	f.mu.Lock()
	f.dependencies[first.Plan.Members[1].Issue] = nil
	f.mu.Unlock()
	if _, err = f.engine(t).CheckFrozen(context.Background(), r); err == nil {
		t.Fatal("native edge drift passed execution precheck")
	}
}

func TestReviewRejectsBindingIndependenceAndApprovalCounterexamples(t *testing.T) {
	for _, name := range []string{"same_instance", "wrong_role", "wrong_run_input", "wrong_run_refs", "wrong_run_subject", "expanded_budget", "run_results_cycle", "missing_check", "duplicate_check", "failed_check", "wrong_evidence", "unresolved_finding", "decision_fail", "wrong_candidate", "wrong_subject", "wrong_reviewer", "wrong_approval_candidate", "wrong_approver", "wrong_policy", "operation_conflict", "contract_operation_conflict", "review_deleted", "review_edited", "run_deleted", "approval_deleted", "contract_deleted", "later_input_drift"} {
		t.Run(name, func(t *testing.T) {
			f, r, run, review, approval := reviewFixture(t)
			reviewer, approver := publicationIdentity(3), publicationIdentity(1)
			switch name {
			case "same_instance":
				run.AgentInstance = "planner-instance"
			case "wrong_role":
				run.Role = protocol.RolePlanner
			case "wrong_run_input":
				run.InputSHA256 = protocol.SHA256([]byte("another plan input"))
			case "wrong_run_refs":
				run.Inputs = []protocol.Reference{r.Publication.Input.Baseline}
			case "wrong_run_subject":
				run.Subject.Milestone++
			case "expanded_budget":
				run.Budget.MaxRunSeconds++
			case "run_results_cycle":
				run.ResultReferences = []protocol.Reference{r.Review}
			case "missing_check":
				review.Assessments = review.Assessments[1:]
			case "duplicate_check":
				review.Assessments[0].CriterionID = review.Assessments[1].CriterionID
			case "failed_check":
				review.Decision = "FAIL"
				review.Assessments[0].Result = "FAIL"
			case "wrong_evidence":
				review.Assessments[0].Evidence = []protocol.Reference{r.Publication.Input.Baseline}
			case "unresolved_finding":
				review.Decision = "FAIL"
				review.UnresolvedFindings = []protocol.Reference{r.Publication.Input.Baseline}
			case "decision_fail":
				review.Decision = "FAIL"
			case "wrong_candidate":
				review.Candidate = r.Publication.Input.Baseline
			case "wrong_subject":
				review.Subject.Milestone++
			case "wrong_reviewer":
				reviewer = publicationIdentity(1)
			case "wrong_approver":
				approver = publicationIdentity(3)
			case "wrong_policy":
				approval.PolicySHA256 = protocol.SHA256([]byte("other policy"))
			case "operation_conflict":
				run.OperationID = "planner-run"
			case "contract_operation_conflict":
				v, err := f.gateway(t).FetchRecord(context.Background(), r.Plan)
				if err != nil {
					t.Fatal(err)
				}
				record, _ := v.Record()
				plan := record.(*protocol.Plan)
				v, err = f.gateway(t).FetchRecord(context.Background(), plan.Members[0].Contract)
				if err != nil {
					t.Fatal(err)
				}
				record, _ = v.Record()
				run.OperationID = record.Header().OperationID
			}
			if name == "duplicate_check" { // The strict protocol rejects this before a native record can exist.
				if _, err := protocol.Encode(&review); err == nil {
					t.Fatal("duplicate assessment encoded")
				}
				return
			}
			runRef := f.record(t, &run, publicationIdentity(1), 2)
			review.Run = runRef
			r.Review = f.record(t, &review, reviewer, 2)
			approval.Candidate = r.Review
			if name == "wrong_approval_candidate" {
				approval.Candidate = r.Plan
			}
			r.Approval = f.record(t, &approval, approver, 2)
			f.mu.Lock()
			deleteID := int64(0)
			switch name {
			case "review_deleted":
				deleteID = r.Review.DatabaseID
			case "run_deleted":
				deleteID = runRef.DatabaseID
			case "approval_deleted":
				deleteID = r.Approval.DatabaseID
			case "review_edited":
				for _, c := range f.comments[2] {
					if c["id"] == r.Review.DatabaseID {
						c["body"] = c["body"].(string) + " "
					}
				}
			case "contract_deleted":
				for parent, comments := range f.comments {
					for i, c := range comments {
						var head map[string]any
						b, _ := c["body"].(string)
						if len(b) > len(protocol.Marker) && json.Unmarshal([]byte(b[len(protocol.Marker):]), &head) == nil && head["kind"] == "contract" {
							f.comments[parent] = append(comments[:i], comments[i+1:]...)
							deleteID = -1
							break
						}
					}
					if deleteID == -1 {
						break
					}
				}
			case "later_input_drift":
				f.inputs.SHA256 = protocol.SHA256([]byte("new input"))
			}
			if deleteID > 0 {
				for parent, comments := range f.comments {
					for i, c := range comments {
						if c["id"] == deleteID {
							f.comments[parent] = append(comments[:i], comments[i+1:]...)
							break
						}
					}
				}
			}
			f.mu.Unlock()
			if _, err := f.engine(t).Activate(context.Background(), r); err == nil {
				t.Fatal("invalid review activated plan")
			}
		})
	}
}

func TestPartialAndUnapprovedPlanNeverActivate(t *testing.T) {
	f, r := newPublicationFixture(t)
	if _, err := f.engine(t).Activate(context.Background(), ReviewRequest{Publication: r}); err == nil {
		t.Fatal("partial plan activated")
	}
	state := f.publishAll(t, r)
	if _, err := f.engine(t).Activate(context.Background(), ReviewRequest{Publication: r, Plan: *state.PlanReference}); err == nil {
		t.Fatal("unapproved published plan activated")
	}
}
