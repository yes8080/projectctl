package plan

import (
	"context"
	"fmt"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

// Recheck the complete selected strong-reference graph, including historical
// records with earlier policy versions. Role authorization uses each phase's
// pinned policy in the input/review gates; this traversal does not incorrectly
// apply the current plan policy to an older design Approval.
func (e *Engine) graph(ctx context.Context, root protocol.Reference) error {
	seen := map[string]protocol.Reference{}
	visiting := map[string]bool{}
	operations := map[string]protocol.Reference{}
	nodes := map[string]protocol.Reference{}
	var visit func(protocol.Reference) error
	visit = func(ref protocol.Reference) error {
		key := fmt.Sprintf("%s:%d", ref.Kind, ref.DatabaseID)
		if visiting[key] {
			return ErrBlocked
		}
		if old, ok := seen[key]; ok {
			if !reference(old, ref) {
				return ErrBlocked
			}
			return nil
		}
		if len(seen)+len(visiting) >= 1000 {
			return ErrBlocked
		}
		visiting[key] = true
		defer delete(visiting, key)
		verified, err := e.remote.FetchRecord(ctx, ref)
		if err != nil || !reference(verified.Reference(), ref) {
			return ErrBlocked
		}
		record, err := verified.Record()
		if err != nil {
			return ErrBlocked
		}
		op := record.Header().OperationID
		if old, ok := operations[op]; ok && !reference(old, ref) {
			return ErrBlocked
		}
		operations[op] = ref
		if old, ok := nodes[ref.NodeID]; ok && !reference(old, ref) {
			return ErrBlocked
		}
		nodes[ref.NodeID] = ref
		refs := []protocol.Reference{}
		switch r := record.(type) {
		case *protocol.Baseline:
			refs = append(refs, r.Review, r.Approval)
		case *protocol.Plan:
			refs = append(refs, r.Baseline)
			if r.ParentRevision != nil {
				refs = append(refs, *r.ParentRevision)
			}
			for _, m := range r.Members {
				refs = append(refs, m.Contract)
			}
		case *protocol.Run:
			refs = append(refs, r.Inputs...)
			refs = append(refs, r.ResultReferences...)
		case *protocol.Evidence:
			refs = append(refs, r.Run)
		case *protocol.Acceptance:
			refs = append(refs, r.Run)
			for _, a := range r.Assessments {
				refs = append(refs, a.Evidence...)
			}
			refs = append(refs, r.UnresolvedFindings...)
		case *protocol.PlanAcceptance:
			refs = append(refs, r.Candidate, r.Run)
			for _, a := range r.Assessments {
				refs = append(refs, a.Evidence...)
			}
			refs = append(refs, r.UnresolvedFindings...)
		case *protocol.Approval:
			refs = append(refs, r.Candidate)
		case *protocol.Delivery:
			refs = append(refs, r.Plan, r.BugGate, r.Cleanup)
			refs = append(refs, r.IntegrationEvidence...)
		}
		for _, child := range refs {
			if err := visit(child); err != nil {
				return err
			}
		}
		seen[key] = ref
		return nil
	}
	if err := visit(root); err != nil {
		return err
	}
	for _, ref := range seen {
		if _, err := e.remote.FetchRecord(ctx, ref); err != nil {
			return ErrBlocked
		}
	}
	return nil
}
