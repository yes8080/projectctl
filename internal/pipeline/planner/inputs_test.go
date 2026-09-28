package planner

import (
	"context"
	"testing"
)

func TestReplayInputsPreservesIdentityAcrossLaterMerge(t *testing.T) {
	f, r := newPlannerInputFixture(t)
	f.commits[r.ExpectedTargetSHA] = f.branch
	before, err := InspectInputs(context.Background(), f, r)
	if err != nil {
		t.Fatal(err)
	}
	f.branch.SHA = inputFixtureSHA("9")
	issue := f.issues[r.Issue.Number]
	issue.State = "closed"
	f.issues[r.Issue.Number] = issue
	if _, err := InspectInputs(context.Background(), f, r); err == nil {
		t.Fatal("initial publication accepted moved branch")
	}
	after, err := ReplayInputs(context.Background(), f, r)
	if err != nil || after.SHA256 != before.SHA256 {
		t.Fatal("historical identity changed", err)
	}
	// The caller must separately establish that the new head descends from the
	// approved base; this read-only identity replay intentionally makes no claim.
	delete(f.commits, r.ExpectedTargetSHA)
	if _, err := ReplayInputs(context.Background(), f, r); err == nil {
		t.Fatal("missing original commit accepted")
	}
}

func TestReplayInputsCommitAndPolicyDrift(t *testing.T) {
	for _, name := range []string{"tree", "sha", "default_branch", "manifest", "review"} {
		t.Run(name, func(t *testing.T) {
			f, r := newPlannerInputFixture(t)
			f.commits[r.ExpectedTargetSHA] = f.branch
			before, err := InspectInputs(context.Background(), f, r)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "tree":
				v := f.commits[r.ExpectedTargetSHA]
				v.TreeSHA = inputFixtureSHA("8")
				f.commits[r.ExpectedTargetSHA] = v
			case "sha":
				v := f.commits[r.ExpectedTargetSHA]
				v.SHA = inputFixtureSHA("8")
				f.commits[r.ExpectedTargetSHA] = v
			case "default_branch":
				f.repo.DefaultBranch = "other"
			case "manifest":
				delete(f.sources, r.Anchor.Manifest)
			case "review":
				delete(f.observations, f.baseline.Review.DatabaseID)
			}
			after, err := ReplayInputs(context.Background(), f, r)
			if err == nil && after.SHA256 == before.SHA256 {
				t.Fatal("drift retained old authorized identity")
			}
		})
	}
}
