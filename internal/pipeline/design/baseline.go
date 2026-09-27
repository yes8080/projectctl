package design

import (
	"context"
	"reflect"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type BaselineRequest struct {
	Request     Request
	Candidate   protocol.SourceReference
	PullRequest gh.Resource
	Binding     protocol.Binding
	Evidence    protocol.Reference
	Review      protocol.Reference
	Approval    protocol.Reference
	OperationID string
	Version     int64
}

// Freeze is read-only and returns a publishable existing protocol.Baseline. Its
// caller must use the serialized trusted publisher and revalidate before writing.
// No approval, merge or baseline record is created by this method.
func (e *Engine) Freeze(ctx context.Context, r BaselineRequest) (protocol.Baseline, error) {
	first, err := e.freeze(ctx, r)
	if err != nil {
		return protocol.Baseline{}, err
	}
	second, err := e.freeze(ctx, r)
	if err != nil || !reflect.DeepEqual(first, second) {
		return protocol.Baseline{}, ErrBlocked
	}
	return second, nil
}
func (e *Engine) freeze(ctx context.Context, r BaselineRequest) (protocol.Baseline, error) {
	var zero protocol.Baseline
	// The target branch is now the merge commit. Binding.BaseSHA remains the
	// approved pre-merge base, not a mutable substitute for the reviewed base.
	p, err := e.prepareMode(ctx, r.Request, true)
	if err != nil {
		return zero, err
	}
	policyDigest, err := p.policy.Digest()
	if err != nil {
		return zero, ErrBlocked
	}
	if !designPath(p.manifest, r.Candidate.Path) {
		return zero, ErrBlocked
	}
	if r.Request.Role != protocol.RoleDesigner || r.Binding.PolicySHA256 != policyDigest || r.Binding.ContractSHA256 != r.Request.Contract.CanonicalSHA256 || r.Binding.HeadSHA != r.Candidate.Commit || r.Binding.DesignSHA256 != r.Candidate.SHA256 || r.Binding.PullRequest != r.PullRequest.Number || r.PullRequest.Kind != "pull_request" || r.PullRequest.DatabaseID <= 0 || r.PullRequest.NodeID == "" {
		return zero, ErrBlocked
	}
	data, err := e.source(ctx, r.Candidate)
	if err != nil {
		return zero, err
	}
	doc, err := DecodeDocument(data)
	if err != nil || len(doc.OpenQuestions) > 0 {
		return zero, ErrBlocked
	}
	evidence, err := e.remote.FetchRecord(ctx, r.Evidence)
	if err != nil || protocol.Authorize(evidence, p.policy, protocol.RoleDesigner) != nil {
		return zero, ErrBlocked
	}
	review, err := e.remote.FetchRecord(ctx, r.Review)
	if err != nil || protocol.Authorize(review, p.policy, protocol.RoleDesignReviewer) != nil || protocol.RequireIndependent(evidence, review) != nil {
		return zero, ErrBlocked
	}
	approval, err := e.remote.FetchRecord(ctx, r.Approval)
	if err != nil {
		return zero, ErrBlocked
	}
	if _, err = protocol.SelectApproved([]protocol.VerifiedRecord{review}, approval, p.policy); err != nil {
		return zero, ErrBlocked
	}
	v, err := evidence.Record()
	if err != nil {
		return zero, ErrBlocked
	}
	ev, ok := v.(*protocol.Evidence)
	if !ok || ev.Binding != r.Binding || ev.Result != "PASS" || ev.ExitCode != 0 || ev.CheckID != "design-candidate" || ev.LogSHA256 != r.Candidate.SHA256 {
		return zero, ErrBlocked
	}
	v, err = review.Record()
	if err != nil {
		return zero, ErrBlocked
	}
	rv, ok := v.(*protocol.Acceptance)
	if !ok || rv.Binding != r.Binding || rv.Decision != "PASS" || len(rv.UnresolvedFindings) != 0 {
		return zero, ErrBlocked
	}
	seen := map[string]bool{}
	for _, a := range rv.Assessments {
		if a.Result != "PASS" || seen[a.CriterionID] || len(a.Evidence) != 1 || !sameRef(a.Evidence[0], r.Evidence) {
			return zero, ErrBlocked
		}
		seen[a.CriterionID] = true
	}
	if len(seen) != len(requiredSections()) {
		return zero, ErrBlocked
	}
	for _, id := range requiredSections() {
		if !seen[id] {
			return zero, ErrBlocked
		}
	}
	var runs []*protocol.Run
	for i, ref := range []protocol.Reference{ev.Run, rv.Run} {
		vr, err := e.remote.FetchRecord(ctx, ref)
		if err != nil || protocol.Authorize(vr, p.policy, protocol.RoleController) != nil {
			return zero, ErrBlocked
		}
		v, err := vr.Record()
		if err != nil {
			return zero, ErrBlocked
		}
		run, ok := v.(*protocol.Run)
		if !ok || run.Subject != (protocol.Subject{Issue: r.Request.Issue.Number}) || run.Project != r.Request.Project {
			return zero, ErrBlocked
		}
		role := protocol.RoleDesigner
		if i == 1 {
			role = protocol.RoleDesignReviewer
		}
		if run.Role != role {
			return zero, ErrBlocked
		}
		runs = append(runs, run)
	}
	if runs[0].AgentInstance == runs[1].AgentInstance || sameRef(ev.Run, rv.Run) {
		return zero, ErrBlocked
	}
	pr, err := e.remote.ReadPullRequest(ctx, r.PullRequest.Number)
	if err != nil || pr.Resource != r.PullRequest || !pr.Merged || pr.Draft || pr.State != "closed" || pr.HeadSHA != r.Binding.HeadSHA || pr.BaseSHA != r.Binding.BaseSHA || pr.BaseRef != r.Request.TargetBranch || pr.MergeSHA != r.Request.ExpectedTargetSHA || !sameIdentity(pr.Author, r.Evidence.Author) {
		return zero, ErrBlocked
	}
	for _, repo := range []gh.Resource{pr.Repository, pr.BaseRepository} {
		if repo.DatabaseID != r.Request.Anchor.Repository.DatabaseID || repo.NodeID != r.Request.Anchor.Repository.NodeID {
			return zero, ErrBlocked
		}
	}
	head, err := e.remote.ReadCommit(ctx, pr.HeadSHA)
	if err != nil || head.SHA != pr.HeadSHA || !validSHA(head.TreeSHA) {
		return zero, ErrBlocked
	}
	merged, err := e.remote.ReadCommit(ctx, pr.MergeSHA)
	if err != nil || merged.SHA != pr.MergeSHA || merged.TreeSHA != head.TreeSHA {
		return zero, ErrBlocked
	}
	mergedSource := r.Candidate
	mergedSource.Commit = pr.MergeSHA
	if mergedData, err := e.source(ctx, mergedSource); err != nil || !reflect.DeepEqual(data, mergedData) {
		return zero, ErrBlocked
	}
	inputs := make([]protocol.SourceReference, 0, len(p.manifest.Inputs))
	for _, in := range p.manifest.Inputs {
		inputs = append(inputs, in.Source)
	}
	baseline := protocol.Baseline{Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindBaseline, Project: r.Request.Project, Subject: protocol.Subject{Issue: r.Request.Project.ControlIssue}, OperationID: r.OperationID, Version: r.Version}, Inputs: inputs, Design: mergedSource, PolicySHA256: policyDigest, Review: r.Review, Approval: r.Approval}
	if _, err = protocol.Encode(&baseline); err != nil {
		return zero, ErrBlocked
	}
	return baseline, nil
}
