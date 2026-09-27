package design

import (
	"context"
	"reflect"
	"testing"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func baselineFixture(t *testing.T) (*fixture, *Engine, BaselineRequest) {
	f, e, r := newFixture(t)
	a, err := e.Admit(context.Background(), r, f.fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	author, err := e.Execute(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	candidate := sourceRef("docs/design.json.md", author.Output)
	candidate.Commit = sha("e")
	f.addSource(candidate, author.Output)
	rr := r
	rr.Role = protocol.RoleDesignReviewer
	rr.Candidate = &candidate
	rr.AuthorRun = &author.Run
	f.output = data(t, goodReview(candidate.SHA256))
	a, err = e.Admit(context.Background(), rr, f.fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	reviewReceipt, err := e.Execute(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	policyHash, _ := policyFor(r.Project, f.manifest).Digest()
	binding := protocol.Binding{PullRequest: 30, HeadSHA: candidate.Commit, BaseSHA: r.ExpectedTargetSHA, DesignSHA256: candidate.SHA256, ContractSHA256: r.Contract.CanonicalSHA256, PolicySHA256: policyHash}
	evidence := &protocol.Evidence{Envelope: envelope(protocol.KindEvidence, r.Project, protocol.Subject{PullRequest: 30}, "design-evidence"), Binding: binding, Run: author.Run, CheckID: "design-candidate", Argv: []string{"codex", "exec"}, EnvironmentSHA256: protocol.SHA256([]byte("isolated")), Result: "PASS", LogSHA256: candidate.SHA256}
	evRef := f.record(t, evidence, native(2), false)
	review := &protocol.Acceptance{Envelope: envelope(protocol.KindAcceptance, r.Project, protocol.Subject{PullRequest: 30}, "design-review"), Binding: binding, Run: reviewReceipt.Run, Assessments: []protocol.Assessment{}, UnresolvedFindings: []protocol.Reference{}, Decision: "PASS", Reason: "independent design checked"}
	for _, id := range requiredSections() {
		review.Assessments = append(review.Assessments, protocol.Assessment{CriterionID: id, Result: "PASS", Reason: "checked", Evidence: []protocol.Reference{evRef}})
	}
	rvRef := f.record(t, review, native(3), false)
	approval := &protocol.Approval{Envelope: envelope(protocol.KindApproval, r.Project, protocol.Subject{PullRequest: 30}, "design-approval"), Candidate: rvRef, PolicySHA256: policyHash, Decision: "APPROVED", Reason: "exact candidate approval"}
	apRef := f.record(t, approval, native(1), false)
	repo := gh.Resource{Kind: "repository", DatabaseID: r.Anchor.Repository.DatabaseID, NodeID: r.Anchor.Repository.NodeID}
	f.pr = gh.PullRequest{Resource: resource("pull_request", 30), Repository: repo, BaseRepository: repo, Author: native(2), State: "closed", Merged: true, HeadSHA: candidate.Commit, BaseSHA: r.ExpectedTargetSHA, BaseRef: "main", HeadRef: "codex/issue-20", MergeSHA: sha("f")}
	f.commits[candidate.Commit] = gh.Commit{SHA: candidate.Commit, TreeSHA: sha("b")}
	f.commits[f.pr.MergeSHA] = gh.Commit{SHA: f.pr.MergeSHA, TreeSHA: sha("b")}
	f.branch = f.commits[f.pr.MergeSHA]
	r.ExpectedTargetSHA = f.pr.MergeSHA
	merged := candidate
	merged.Commit = f.pr.MergeSHA
	f.addSource(merged, author.Output)
	issue := f.issues[20]
	issue.State = "closed"
	f.issues[20] = issue
	return f, e, BaselineRequest{Request: r, Candidate: candidate, PullRequest: f.pr.Resource, Binding: binding, Evidence: evRef, Review: rvRef, Approval: apRef, OperationID: "freeze-design-1", Version: 1}
}

func TestExactApprovedMergedCandidateBecomesBaseline(t *testing.T) {
	f, e, r := baselineFixture(t)
	b, err := e.Freeze(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if b.Design.Commit != f.pr.MergeSHA || b.Design.SHA256 != r.Candidate.SHA256 || !sameRef(b.Review, r.Review) || !sameRef(b.Approval, r.Approval) {
		t.Fatalf("wrong baseline %+v", b)
	}
	restarted, _ := New(f, f, f)
	restarted.now = e.now
	again, err := restarted.Freeze(context.Background(), r)
	if err != nil || !reflect.DeepEqual(b, again) {
		t.Fatal("restart changed exact selection", err)
	}
	if _, err := protocol.Encode(&b); err != nil {
		t.Fatal(err)
	}
}

func TestBaselineRejectsUnapprovedOrDriftedFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture, *BaselineRequest)
	}{
		{"unmerged", func(f *fixture, _ *BaselineRequest) { f.pr.Merged = false }},
		{"draft", func(f *fixture, _ *BaselineRequest) { f.pr.Draft = true }},
		{"different head", func(f *fixture, _ *BaselineRequest) { f.pr.HeadSHA = sha("d") }},
		{"different base", func(f *fixture, _ *BaselineRequest) { f.pr.BaseSHA = sha("d") }},
		{"different target", func(f *fixture, _ *BaselineRequest) { f.pr.BaseRef = "other" }},
		{"branch advanced", func(f *fixture, _ *BaselineRequest) { f.branch.SHA = sha("d") }},
		{"wrong repository", func(f *fixture, _ *BaselineRequest) { f.pr.BaseRepository.DatabaseID++ }},
		{"merge tree differs", func(f *fixture, _ *BaselineRequest) {
			v := f.commits[f.pr.MergeSHA]
			v.TreeSHA = sha("d")
			f.commits[f.pr.MergeSHA] = v
		}},
		{"missing approval", func(f *fixture, r *BaselineRequest) { delete(f.observations, r.Approval.DatabaseID) }},
		{"edited review", func(f *fixture, r *BaselineRequest) {
			o := f.observations[r.Review.DatabaseID]
			o.Body += " "
			f.observations[r.Review.DatabaseID] = o
		}},
		{"wrong publisher", func(f *fixture, r *BaselineRequest) {
			o := f.observations[r.Review.DatabaseID]
			o.Author = native(2)
			f.observations[r.Review.DatabaseID] = o
		}},
		{"unapproved revision", func(f *fixture, r *BaselineRequest) {
			d := goodDocument()
			d.Sections[0].Content = "new unapproved design"
			b := data(t, d)
			r.Candidate.SHA256 = protocol.SHA256(b)
			f.addSource(r.Candidate, b)
		}},
		{"design deleted", func(f *fixture, r *BaselineRequest) { delete(f.sources, r.Candidate) }},
		{"policy binding", func(_ *fixture, r *BaselineRequest) { r.Binding.PolicySHA256 = protocol.SHA256([]byte("other")) }},
		{"contract binding", func(_ *fixture, r *BaselineRequest) { r.Binding.ContractSHA256 = protocol.SHA256([]byte("other")) }},
		{"same PR number new object", func(f *fixture, _ *BaselineRequest) { f.pr.DatabaseID++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e, r := baselineFixture(t)
			tc.change(f, &r)
			if _, err := e.Freeze(context.Background(), r); err == nil {
				t.Fatal("invalid baseline admitted")
			}
		})
	}
}
