package design

import (
	"context"
	"reflect"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func pinnedFixtureRecord(t *testing.T, f *fixture, ref protocol.Reference) protocol.Record {
	t.Helper()
	v, err := f.FetchRecord(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	r, err := v.Record()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Re-publish a valid, independently authored evidence/review/approval graph.
// All outer pins remain sound, so the negative tests target the underlying Run
// identity instead of merely failing on an edited comment or stale outer hash.
func rebindBaselineProof(t *testing.T, f *fixture, r *BaselineRequest, ev *protocol.Evidence, rv *protocol.Acceptance) {
	t.Helper()
	approval := pinnedFixtureRecord(t, f, r.Approval).(*protocol.Approval)
	ev.Binding, ev.LogSHA256 = r.Binding, r.Candidate.SHA256
	ev.OperationID += "-repinned"
	r.Evidence = f.record(t, ev, native(2), false)
	rv.Binding = r.Binding
	for i := range rv.Assessments {
		rv.Assessments[i].Evidence = []protocol.Reference{r.Evidence}
	}
	rv.OperationID += "-repinned"
	r.Review = f.record(t, rv, native(3), false)
	approval.Candidate, approval.PolicySHA256 = r.Review, r.Binding.PolicySHA256
	approval.OperationID += "-repinned"
	r.Approval = f.record(t, approval, native(1), false)
}

func baselineRun(t *testing.T, f *fixture, r BaselineRequest, role protocol.Role) (protocol.Reference, *protocol.Run) {
	t.Helper()
	ref := pinnedFixtureRecord(t, f, r.Evidence).(*protocol.Evidence).Run
	if role == protocol.RoleDesignReviewer {
		ref = pinnedFixtureRecord(t, f, r.Review).(*protocol.Acceptance).Run
	}
	return ref, pinnedFixtureRecord(t, f, ref).(*protocol.Run)
}

func TestBaselineRunIdentityMatchesAdmissionAfterMergeAndRestart(t *testing.T) {
	f, e, r := baselineFixture(t)
	beforeCalls := f.calls
	designerRef, designer := baselineRun(t, f, r, protocol.RoleDesigner)
	_, reviewer := baselineRun(t, f, r, protocol.RoleDesignReviewer)
	if len(designer.Inputs) != 1 || !sameRef(designer.Inputs[0], r.Request.Contract) || len(reviewer.Inputs) != 2 || !sameRef(reviewer.Inputs[0], r.Request.Contract) || !sameRef(reviewer.Inputs[1], designerRef) {
		t.Fatal("admitted Runs did not retain exact contract/author pins")
	}
	// These Runs were actually admitted and executed at Binding.BaseSHA. Freeze
	// sees the merged branch and must not substitute its SHA into their digest.
	if r.Request.ExpectedTargetSHA == r.Binding.BaseSHA || designer.InputSHA256 == reviewer.InputSHA256 {
		t.Fatal("fixture does not distinguish pre-merge base and role inputs")
	}
	baseline, err := e.Freeze(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	restarted, _ := New(f, f, f)
	restarted.now = e.now
	again, err := restarted.Freeze(context.Background(), r)
	if err != nil || !reflect.DeepEqual(baseline, again) || f.calls != beforeCalls {
		t.Fatal("freeze used local receipt/runtime instead of native input facts", err)
	}
}

func TestBaselineRejectsValidRunsWithOtherInputIdentities(t *testing.T) {
	for _, role := range []protocol.Role{protocol.RoleDesigner, protocol.RoleDesignReviewer} {
		for _, mode := range []string{"other digest", "other contract", "missing pins", "duplicate pins", "extra pins", "reordered pins"} {
			if role == protocol.RoleDesigner && mode == "reordered pins" {
				continue
			}
			t.Run(string(role)+"/"+mode, func(t *testing.T) {
				f, e, r := baselineFixture(t)
				ev := pinnedFixtureRecord(t, f, r.Evidence).(*protocol.Evidence)
				rv := pinnedFixtureRecord(t, f, r.Review).(*protocol.Acceptance)
				_, run := baselineRun(t, f, r, role)
				switch mode {
				case "other digest":
					run.InputSHA256 = protocol.SHA256([]byte("another original input or candidate"))
				case "other contract":
					contract := pinnedFixtureRecord(t, f, r.Request.Contract).(*protocol.Contract)
					contract.OperationID += "-other"
					contract.Goal = "different approved work"
					run.Inputs[0] = f.record(t, contract, native(1), false)
				case "missing pins":
					// Run schema requires nonempty Inputs, so a reviewer can omit
					// author while a designer can substitute another valid pin.
					if role == protocol.RoleDesignReviewer {
						run.Inputs = run.Inputs[:1]
					} else {
						run.Inputs = []protocol.Reference{rv.Run}
					}
				case "duplicate pins":
					run.Inputs = append(run.Inputs, run.Inputs[0])
				case "extra pins":
					run.Inputs = append(run.Inputs, r.Evidence)
				case "reordered pins":
					run.Inputs[0], run.Inputs[1] = run.Inputs[1], run.Inputs[0]
				}
				run.OperationID += "-other-input"
				run.AgentInstance += "-other-input"
				other := f.record(t, run, native(1), false)
				if role == protocol.RoleDesigner {
					ev.Run = other
				} else {
					rv.Run = other
				}
				rebindBaselineProof(t, f, &r, ev, rv)
				if got, err := e.Freeze(context.Background(), r); err == nil || !reflect.DeepEqual(got, protocol.Baseline{}) {
					t.Fatal("valid old Run was promoted by newly pinned approval")
				}
			})
		}
	}
}

func TestBaselineRebuildsFullCanonicalInputIdentity(t *testing.T) {
	for _, mode := range []string{"original input", "startup commit", "startup version", "policy", "contract", "premerge base", "candidate content", "candidate commit", "author run reference"} {
		t.Run(mode, func(t *testing.T) {
			f, e, r := baselineFixture(t)
			ev := pinnedFixtureRecord(t, f, r.Evidence).(*protocol.Evidence)
			rv := pinnedFixtureRecord(t, f, r.Review).(*protocol.Acceptance)
			switch mode {
			case "original input":
				body := []byte("A different original requirement demands another architecture")
				ref := sourceRef("docs/input.md", body)
				ref.Commit = sha("9")
				f.manifest.Inputs[0].Source = ref
				f.addSource(ref, body)
			case "startup version":
				f.manifest.Version++
			case "policy":
				f.manifest.Policy.Budget.MaxAgentRuns++
			case "contract":
				contract := pinnedFixtureRecord(t, f, r.Request.Contract).(*protocol.Contract)
				contract.OperationID += "-new"
				contract.Goal = "new current contract"
				r.Request.Contract = f.record(t, contract, native(1), false)
				r.Binding.ContractSHA256 = r.Request.Contract.CanonicalSHA256
			case "premerge base":
				r.Binding.BaseSHA, f.pr.BaseSHA = sha("9"), sha("9")
			case "candidate content", "candidate commit":
				body, err := e.source(context.Background(), r.Candidate)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "candidate content" {
					d := goodDocument()
					d.Sections[0].Content = "another complete design"
					body = data(t, d)
					r.Candidate.SHA256 = protocol.SHA256(body)
				} else {
					r.Candidate.Commit = sha("9")
					f.pr.HeadSHA = r.Candidate.Commit
					head := f.commits[r.Binding.HeadSHA]
					head.SHA = r.Candidate.Commit
					f.commits[r.Candidate.Commit] = head
				}
				f.addSource(r.Candidate, body)
				merged := r.Candidate
				merged.Commit = f.pr.MergeSHA
				f.addSource(merged, body)
				r.Binding.HeadSHA, r.Binding.DesignSHA256 = r.Candidate.Commit, r.Candidate.SHA256
			case "author run reference":
				_, author := baselineRun(t, f, r, protocol.RoleDesigner)
				author.OperationID += "-another-valid-run"
				author.AgentInstance += "-another-valid-run"
				ev.Run = f.record(t, author, native(1), false)
			}
			if mode == "original input" || mode == "startup commit" || mode == "startup version" || mode == "policy" {
				body := data(t, f.manifest)
				r.Request.Anchor.Manifest = sourceRef("docs/startup.json", body)
				r.Request.Anchor.Manifest.Commit = sha("8")
				f.addSource(r.Request.Anchor.Manifest, body)
				r.Binding.PolicySHA256, _ = policyFor(r.Request.Project, f.manifest).Digest()
			}
			rebindBaselineProof(t, f, &r, ev, rv)
			// The surrounding facts and approvals are all updated, but the Runs
			// still describe the old context. Restart must not fill that gap.
			restarted, _ := New(f, f, f)
			restarted.now = e.now
			if _, err := restarted.Freeze(context.Background(), r); err == nil {
				t.Fatal("changed canonical identity accepted with old Runs")
			}
		})
	}
}

func TestBaselineRunEditAndDeletionRemainBlockedAfterSuccessfulFreeze(t *testing.T) {
	for _, role := range []protocol.Role{protocol.RoleDesigner, protocol.RoleDesignReviewer} {
		for _, mode := range []string{"edited", "deleted"} {
			t.Run(string(role)+"/"+mode, func(t *testing.T) {
				f, e, r := baselineFixture(t)
				if _, err := e.Freeze(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				ref, _ := baselineRun(t, f, r, role)
				if mode == "deleted" {
					delete(f.observations, ref.DatabaseID)
				} else {
					n := f.observations[ref.DatabaseID]
					n.Body += " "
					f.observations[ref.DatabaseID] = n
				}
				restarted, _ := New(f, f, f)
				restarted.now = e.now
				if _, err := restarted.Freeze(context.Background(), r); err == nil {
					t.Fatal("old in-memory proof replaced remote Run facts")
				}
			})
		}
	}
}
