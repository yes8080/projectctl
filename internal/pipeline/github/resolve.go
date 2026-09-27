package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func (g *Gateway) observation(comment Comment, kind string) protocol.GitHubObservation {
	body := ""
	if comment.Content.Body != nil {
		body = *comment.Content.Body
	}
	return protocol.GitHubObservation{Project: g.project, Kind: kind, DatabaseID: comment.DatabaseID, NodeID: comment.NodeID, URL: comment.URL, Author: comment.Author, Body: body}
}

// FetchRecord re-reads the exact native object. An inaccessible/deleted record,
// changed body, different author or wrong object identity is never replaced by
// another comment, a local backup or a newer revision.
func (g *Gateway) FetchRecord(ctx context.Context, ref protocol.Reference) (protocol.VerifiedRecord, error) {
	var zero protocol.VerifiedRecord
	if err := ref.Validate(g.project); err != nil {
		return zero, err
	}
	var comment Comment
	var err error
	if ref.Kind == "issue_comment" {
		comment, err = g.ReadComment(ctx, ref.DatabaseID)
	} else {
		prefix := g.webPrefix() + "/pull/"
		tail := strings.TrimPrefix(ref.URL, prefix)
		numberText, _, ok := strings.Cut(tail, "#")
		if !ok {
			return zero, fmt.Errorf("invalid review reference")
		}
		number, e := strconv.ParseInt(numberText, 10, 64)
		if e != nil || number <= 0 {
			return zero, fmt.Errorf("invalid review parent")
		}
		var native nativeComment
		err = g.get(ctx, fmt.Sprintf("%s/pulls/%d/reviews/%d", g.repoPath(), number, ref.DatabaseID), &native)
		if err == nil {
			comment, err = g.comment(native, true)
		}
	}
	if err != nil {
		return zero, err
	}
	return protocol.VerifyGitHubRecord(g.observation(comment, ref.Kind), ref)
}

type ApprovedChain struct {
	Candidate protocol.VerifiedRecord
	// Records are ephemeral verified observations, never a persisted cache.
	Records []protocol.VerifiedRecord
}

// ResolveApproved starts at a caller-pinned authoritative approval, not a list's
// last comment. All precise record references are followed and validated. The
// caller still owns approval supersession, topology/source verification and
// higher-level acceptance semantics. This is not a scheduler or launch gate.
func (g *Gateway) ResolveApproved(ctx context.Context, approvalRef protocol.Reference, policy protocol.Policy) (ApprovedChain, error) {
	var zero ApprovedChain
	if policy.Project != g.project {
		return zero, fmt.Errorf("policy belongs to another project")
	}
	if err := policy.Validate(); err != nil {
		return zero, err
	}
	seen := map[string]protocol.VerifiedRecord{}
	visiting := map[string]bool{}
	operations := map[string]protocol.Reference{}
	order := []protocol.VerifiedRecord{}
	var visit func(protocol.Reference) (protocol.VerifiedRecord, error)
	visit = func(ref protocol.Reference) (protocol.VerifiedRecord, error) {
		var none protocol.VerifiedRecord
		key := ref.Kind + ":" + strconv.FormatInt(ref.DatabaseID, 10)
		if visiting[key] {
			return none, fmt.Errorf("cyclic record reference chain")
		}
		if existing, ok := seen[key]; ok {
			if !sameReference(existing.Reference(), ref) {
				return none, fmt.Errorf("%w: two pins for one object", ErrConflict)
			}
			return existing, nil
		}
		if len(seen)+len(visiting) >= 1000 {
			return none, fmt.Errorf("reference graph exceeds safety bound")
		}
		visiting[key] = true
		defer delete(visiting, key)
		verified, err := g.FetchRecord(ctx, ref)
		if err != nil {
			return none, err
		}
		record, err := verified.Record()
		if err != nil {
			return none, err
		}
		op := record.Header().OperationID
		if previous, ok := operations[op]; ok && !sameReference(previous, ref) {
			return none, fmt.Errorf("%w: operation_id has multiple objects", ErrConflict)
		}
		operations[op] = ref
		for _, child := range recordReferences(record) {
			if _, err := visit(child); err != nil {
				return none, err
			}
		}
		for _, typed := range typedReferences(record) {
			child, err := visit(typed.ref)
			if err != nil {
				return none, err
			}
			value, err := child.Record()
			if err != nil {
				return none, err
			}
			if value.Header().Kind != typed.kind {
				return none, fmt.Errorf("reference target has wrong record kind; want %s", typed.kind)
			}
		}
		if acceptance, ok := record.(*protocol.PlanAcceptance); ok {
			candidate, err := visit(acceptance.Candidate)
			if err != nil {
				return none, err
			}
			value, err := candidate.Record()
			if err != nil {
				return none, err
			}
			if value.Header().Subject != acceptance.Subject {
				return none, fmt.Errorf("plan acceptance candidate belongs to another milestone")
			}
		}
		if decision, ok := record.(*protocol.Approval); ok {
			candidate, err := visit(decision.Candidate)
			if err != nil {
				return none, err
			}
			if _, err := protocol.SelectApproved([]protocol.VerifiedRecord{candidate}, verified, policy); err != nil {
				return none, err
			}
		}
		if plan, ok := record.(*protocol.Plan); ok {
			for _, member := range plan.Members {
				issue, err := g.ReadIssue(ctx, member.Issue)
				if err != nil {
					return none, err
				}
				if issue.DatabaseID != member.IssueDatabaseID || issue.NodeID != member.IssueNodeID {
					return none, fmt.Errorf("plan member native identity drift")
				}
				contract, err := visit(member.Contract)
				if err != nil {
					return none, err
				}
				value, err := contract.Record()
				if err != nil {
					return none, err
				}
				if value.Header().Kind != protocol.KindContract || value.Header().Subject.Issue != member.Issue {
					return none, fmt.Errorf("plan member does not pin its contract")
				}
			}
		}
		seen[key] = verified
		order = append(order, verified)
		return verified, nil
	}
	approval, err := visit(approvalRef)
	if err != nil {
		return zero, err
	}
	root, err := approval.Record()
	if err != nil {
		return zero, err
	}
	decision, ok := root.(*protocol.Approval)
	if !ok {
		return zero, fmt.Errorf("root must be an explicit approval record")
	}
	candidate, err := visit(decision.Candidate)
	if err != nil {
		return zero, err
	}
	selected, err := protocol.SelectApproved([]protocol.VerifiedRecord{candidate}, approval, policy)
	if err != nil {
		return zero, err
	}
	// A second observation catches edits during traversal. It cannot promise an
	// atomic multi-object GitHub snapshot; the Controller revalidates before use.
	for _, record := range order {
		if _, err := g.FetchRecord(ctx, record.Reference()); err != nil {
			return zero, err
		}
	}
	return ApprovedChain{Candidate: selected, Records: order}, nil
}

func sameIdentity(a, b protocol.GitHubIdentity) bool {
	return a.ID == b.ID && a.NodeID == b.NodeID && a.Type == b.Type
}
func sameReference(a, b protocol.Reference) bool {
	return a.Kind == b.Kind && a.DatabaseID == b.DatabaseID && a.NodeID == b.NodeID && a.URL == b.URL && a.BodySHA256 == b.BodySHA256 && a.CanonicalSHA256 == b.CanonicalSHA256 && sameIdentity(a.Author, b.Author)
}

func recordReferences(record protocol.Record) []protocol.Reference {
	var result []protocol.Reference
	switch r := record.(type) {
	case *protocol.Baseline:
		result = append(result, r.Review, r.Approval)
	case *protocol.Plan:
		result = append(result, r.Baseline)
		if r.ParentRevision != nil {
			result = append(result, *r.ParentRevision)
		}
		for _, member := range r.Members {
			result = append(result, member.Contract)
		}
	case *protocol.Run:
		result = append(result, r.Inputs...)
		result = append(result, r.ResultReferences...)
	case *protocol.Evidence:
		result = append(result, r.Run)
	case *protocol.Acceptance:
		result = append(result, r.Run)
		for _, ac := range r.Assessments {
			result = append(result, ac.Evidence...)
		}
		result = append(result, r.UnresolvedFindings...)
	case *protocol.PlanAcceptance:
		result = append(result, r.Candidate, r.Run)
		for _, ac := range r.Assessments {
			result = append(result, ac.Evidence...)
		}
		result = append(result, r.UnresolvedFindings...)
	case *protocol.Delivery:
		result = append(result, r.Plan, r.BugGate, r.Cleanup)
		result = append(result, r.IntegrationEvidence...)
	case *protocol.Approval:
		result = append(result, r.Candidate)
	}
	// Contract's bootstrap URL/parent navigation fields are not strong References
	// and are deliberately not promoted into authorization proofs here.
	return result
}

type typedReference struct {
	ref  protocol.Reference
	kind protocol.Kind
}

func typedReferences(record protocol.Record) []typedReference {
	var result []typedReference
	switch r := record.(type) {
	case *protocol.Baseline:
		result = append(result, typedReference{r.Review, protocol.KindAcceptance}, typedReference{r.Approval, protocol.KindApproval})
	case *protocol.Plan:
		result = append(result, typedReference{r.Baseline, protocol.KindBaseline})
		if r.ParentRevision != nil {
			result = append(result, typedReference{*r.ParentRevision, protocol.KindPlan})
		}
		for _, member := range r.Members {
			result = append(result, typedReference{member.Contract, protocol.KindContract})
		}
	case *protocol.Evidence:
		result = append(result, typedReference{r.Run, protocol.KindRun})
	case *protocol.Acceptance:
		result = append(result, typedReference{r.Run, protocol.KindRun})
		for _, ac := range r.Assessments {
			for _, ref := range ac.Evidence {
				result = append(result, typedReference{ref, protocol.KindEvidence})
			}
		}
	case *protocol.PlanAcceptance:
		result = append(result, typedReference{r.Candidate, protocol.KindPlan}, typedReference{r.Run, protocol.KindRun})
	case *protocol.Delivery:
		result = append(result, typedReference{r.Plan, protocol.KindPlan})
	}
	return result
}
