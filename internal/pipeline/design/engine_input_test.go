package design

import (
	"context"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

// Models a correctly pinned native replacement, for testing semantic identity
// independently from VerifyGitHubRecord's edited-body rejection.
func repinFixtureRun(t *testing.T, f *fixture, ref protocol.Reference, run *protocol.Run) protocol.Reference {
	t.Helper()
	body, err := protocol.EncodeComment(run)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := protocol.Encode(run)
	if err != nil {
		t.Fatal(err)
	}
	ref.BodySHA256, ref.CanonicalSHA256 = protocol.SHA256([]byte(body)), protocol.SHA256(canonical)
	native := f.observations[ref.DatabaseID]
	native.Body = body
	f.observations[ref.DatabaseID] = native
	for i := range f.comments {
		if f.comments[i].DatabaseID == ref.DatabaseID {
			f.comments[i].Content.Body = &body
			f.comments[i].Content.BodySHA256 = ref.BodySHA256
			f.comments[i].Content.CanonicalSHA256 = ref.CanonicalSHA256
		}
	}
	return ref
}

func independentReviewFixture(t *testing.T) (*fixture, *Engine, Request) {
	t.Helper()
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
	r.Role, r.Candidate, r.AuthorRun = protocol.RoleDesignReviewer, &candidate, &author.Run
	f.output = data(t, goodReview(candidate.SHA256))
	return f, e, r
}

func TestAdmissionVerifiesExactRunInputPins(t *testing.T) {
	for _, role := range []protocol.Role{protocol.RoleDesigner, protocol.RoleDesignReviewer} {
		for _, mode := range []string{"digest", "duplicate", "wrong pin", "missing author", "reordered"} {
			if role == protocol.RoleDesigner && (mode == "missing author" || mode == "reordered") {
				continue
			}
			t.Run(string(role)+"/"+mode, func(t *testing.T) {
				f, e, r := newFixture(t)
				if role == protocol.RoleDesignReviewer {
					f, e, r = independentReviewFixture(t)
				}
				calls := f.calls
				fresh := f.fresh(t)
				_, err := e.Admit(context.Background(), r, func(ctx context.Context, request Request, inputHash string) (protocol.Reference, error) {
					ref, err := fresh(ctx, request, inputHash)
					if err != nil {
						return ref, err
					}
					run := pinnedFixtureRecord(t, f, ref).(*protocol.Run)
					switch mode {
					case "digest":
						run.InputSHA256 = protocol.SHA256([]byte("different request"))
					case "duplicate":
						run.Inputs = append(run.Inputs, run.Inputs[0])
					case "wrong pin":
						run.Inputs[0] = ref
					case "missing author":
						run.Inputs = run.Inputs[:1]
					case "reordered":
						run.Inputs[0], run.Inputs[1] = run.Inputs[1], run.Inputs[0]
					}
					return repinFixtureRun(t, f, ref, run), nil
				})
				if err == nil || f.calls != calls {
					t.Fatal("semantically unrelated Run admitted")
				}
			})
		}
	}
}

func TestReviewAdmissionRejectsOldAuthorInputBeforeAllocation(t *testing.T) {
	for _, mode := range []string{"digest", "contract pin", "duplicate pin"} {
		t.Run(mode, func(t *testing.T) {
			f, e, r := independentReviewFixture(t)
			author := pinnedFixtureRecord(t, f, *r.AuthorRun).(*protocol.Run)
			switch mode {
			case "digest":
				author.InputSHA256 = protocol.SHA256([]byte("another original input"))
			case "contract pin":
				author.Inputs[0] = *r.AuthorRun
			case "duplicate pin":
				author.Inputs = append(author.Inputs, author.Inputs[0])
			}
			ref := repinFixtureRun(t, f, *r.AuthorRun, author)
			r.AuthorRun = &ref
			allocated := false
			_, err := e.Admit(context.Background(), r, func(context.Context, Request, string) (protocol.Reference, error) {
				allocated = true
				return protocol.Reference{}, nil
			})
			if err == nil || allocated || f.calls != 1 {
				t.Fatal("old author context caused reviewer allocation or execution")
			}
		})
	}
}

func TestExecuteRechecksInputPinsAfterAdmission(t *testing.T) {
	f, e, r := newFixture(t)
	a, err := e.Admit(context.Background(), r, f.fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	run := pinnedFixtureRecord(t, f, a.p.run).(*protocol.Run)
	run.Inputs = append(run.Inputs, run.Inputs[0])
	// Even an internal test that updates the ephemeral pin cannot cause Execute
	// to bypass the semantic check shared with admission and Freeze.
	a.p.run = repinFixtureRun(t, f, a.p.run, run)
	if _, err := e.Execute(context.Background(), a); err == nil || f.calls != 0 {
		t.Fatal("execution skipped exact Run inputs")
	}
}
