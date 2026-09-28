package plan

import (
	"context"

	"github.com/yes8080/projectctl/internal/pipeline/design"
	"github.com/yes8080/projectctl/internal/pipeline/planner"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

var reviewChecks = []string{"coverage", "dag", "acceptance", "environment", "budgets", "completion"}

type historicalInputs struct{ reader HistoricalInputReader }

func (h historicalInputs) Read(ctx context.Context, r planner.Request) (planner.Inputs, error) {
	return h.reader.Replay(ctx, r)
}

// CheckFrozen replays the approved chain and native topology before later work.
// Normal branch advancement and native Issue closure do not rewrite a frozen
// plan; current-head ancestry and task completion remain Controller checks.
func (e *Engine) CheckFrozen(ctx context.Context, r ReviewRequest) (Frozen, error) {
	historical, ok := e.inputs.(HistoricalInputReader)
	if !ok {
		return Frozen{}, ErrBlocked
	}
	copy := *e
	copy.inputs = historicalInputs{historical}
	copy.historical = true
	return copy.Activate(ctx, r)
}

// ReviewContext is fresh immutable input for a separate read-only reviewer.
// It contains remote contracts, not the Planner's conversation or self-review.
// It is not a Run, an approval, or authorization to start an Agent.
type ReviewContext struct {
	Plan          protocol.Reference
	Inputs        []protocol.Reference
	InputSHA256   string
	Candidate     protocol.Plan
	Contracts     []protocol.Contract
	Checks        []string
	Design        design.Document
	Policy        startup.Policy
	Authorization protocol.Policy
}

func (e *Engine) ReviewInput(ctx context.Context, r Request) (ReviewContext, error) {
	state, err := e.Inspect(ctx, r)
	if err != nil || state.Plan == nil || state.PlanReference == nil {
		return ReviewContext{}, ErrBlocked
	}
	p, err := e.prepare(ctx, r)
	if err != nil || p.digest != state.InputSHA256 {
		return ReviewContext{}, ErrBlocked
	}
	view := ReviewContext{Plan: *state.PlanReference, Inputs: []protocol.Reference{*state.PlanReference, r.Input.Baseline, r.PlannerRun}, Candidate: *state.Plan, Contracts: []protocol.Contract{}, Checks: append([]string(nil), reviewChecks...), Design: p.input.Design, Policy: p.input.Manifest.Policy, Authorization: p.policy}
	for _, member := range state.Plan.Members {
		record, err := e.record(ctx, member.Contract, p.policy, protocol.RoleController)
		if err != nil {
			return ReviewContext{}, ErrBlocked
		}
		contract, ok := record.(*protocol.Contract)
		if !ok || contract.Subject.Issue != member.Issue {
			return ReviewContext{}, ErrBlocked
		}
		view.Contracts = append(view.Contracts, *contract)
	}
	view.InputSHA256 = digest(struct {
		Publication string
		View        ReviewContext
	}{p.digest, view})
	return view, nil
}

// Activate is read-only. Only a caller-selected native Controller Approval of a
// precise independent PlanAcceptance can select the exact published Plan. Merely
// publishing a Plan, closing an Issue or receiving a model PASS cannot activate it.
// Independence is separate Agent instances and exact authorized Run inputs. An
// explicit bootstrap policy may grant multiple roles to one native principal;
// this gate does not claim credential or native-account isolation.
func (e *Engine) Activate(ctx context.Context, r ReviewRequest) (Frozen, error) {
	first, err := e.activate(ctx, r)
	if err != nil {
		return Frozen{}, err
	}
	second, err := e.activate(ctx, r)
	if err != nil || digest(first) != digest(second) || ctx.Err() != nil {
		return Frozen{}, ErrBlocked
	}
	return second, nil
}

func (e *Engine) activate(ctx context.Context, r ReviewRequest) (Frozen, error) {
	view, err := e.ReviewInput(ctx, r.Publication)
	if err != nil || !reference(view.Plan, r.Plan) {
		return Frozen{}, ErrBlocked
	}
	p, err := e.prepare(ctx, r.Publication)
	if err != nil {
		return Frozen{}, ErrBlocked
	}
	reviewVerified, err := e.remote.FetchRecord(ctx, r.Review)
	if err != nil || !reference(reviewVerified.Reference(), r.Review) || protocol.Authorize(reviewVerified, p.policy, protocol.RolePlanReviewer) != nil {
		return Frozen{}, ErrBlocked
	}
	value, err := reviewVerified.Record()
	if err != nil {
		return Frozen{}, ErrBlocked
	}
	review, ok := value.(*protocol.PlanAcceptance)
	if !ok || review.Subject != view.Candidate.Subject || !reference(review.Candidate, r.Plan) || review.Decision != "PASS" || len(review.UnresolvedFindings) != 0 || len(review.Assessments) != len(reviewChecks) {
		return Frozen{}, ErrBlocked
	}
	expected := map[string]bool{}
	for _, check := range reviewChecks {
		expected[check] = true
	}
	for _, assessment := range review.Assessments {
		if !expected[assessment.CriterionID] || assessment.Result != "PASS" || len(assessment.Evidence) != 1 || !reference(assessment.Evidence[0], r.Plan) {
			return Frozen{}, ErrBlocked
		}
		delete(expected, assessment.CriterionID)
	}
	value, err = e.record(ctx, review.Run, p.policy, protocol.RoleController)
	if err != nil {
		return Frozen{}, ErrBlocked
	}
	run, ok := value.(*protocol.Run)
	if !ok || run.Role != protocol.RolePlanReviewer || run.Subject != view.Candidate.Subject || run.InputSHA256 != view.InputSHA256 || run.AgentInstance == p.plannerRun.AgentInstance || run.OperationID == p.plannerRun.OperationID || reference(review.Run, r.Publication.PlannerRun) || len(run.Inputs) != len(view.Inputs) || len(run.ResultReferences) != 0 || !budgetWithin(run.Budget, view.Candidate.Budget) || run.Attempt > view.Candidate.Budget.MaxPlanningRounds {
		return Frozen{}, ErrBlocked
	}
	for i, ref := range view.Inputs {
		if !reference(run.Inputs[i], ref) {
			return Frozen{}, ErrBlocked
		}
	}
	approval, err := e.remote.FetchRecord(ctx, r.Approval)
	if err != nil || !reference(approval.Reference(), r.Approval) {
		return Frozen{}, ErrBlocked
	}
	selected, err := protocol.SelectApproved([]protocol.VerifiedRecord{reviewVerified}, approval, p.policy)
	if err != nil || !reference(selected.Reference(), r.Review) {
		return Frozen{}, ErrBlocked
	}
	if err := e.graph(ctx, r.Approval); err != nil {
		return Frozen{}, ErrBlocked
	}
	// Distinct objects cannot reuse an operation identity in the selected graph.
	decision, err := approval.Record()
	if err != nil {
		return Frozen{}, ErrBlocked
	}
	operations := map[string]bool{}
	for _, record := range []protocol.Record{&view.Candidate, review, run, decision, &p.plannerRun} {
		op := record.Header().OperationID
		if operations[op] {
			return Frozen{}, ErrBlocked
		}
		operations[op] = true
	}
	return Frozen{Plan: view.Candidate, Reference: r.Plan, Review: r.Review, Approval: r.Approval, InputSHA256: view.InputSHA256}, nil
}
