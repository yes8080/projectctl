package plan

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func (f *publicationFixture) publishAll(t *testing.T, r Request) State {
	t.Helper()
	for step := 0; step < 20; step++ {
		e := f.engine(t)
		state, err := e.Inspect(context.Background(), r)
		if err == nil {
			if state.Status != "candidate" || state.Plan == nil || state.PlanReference == nil || state.Next != nil {
				t.Fatalf("unexpected terminal publication: %#v", state)
			}
			return state
		}
		if !errors.Is(err, ErrPartial) || state.Status != "partial" || state.Next == nil {
			t.Fatalf("inspect step %d: %#v %v", step, state, err)
		}
		if state.Status == "active" {
			t.Fatal("partial publication became active")
		}
		a, err := e.Admit(context.Background(), r, f.freshAllocation(t))
		if err != nil {
			t.Fatalf("admit step %d: %v", step, err)
		}
		_, err = e.Apply(context.Background(), a)
		if err != nil && !errors.Is(err, ErrPartial) {
			t.Fatalf("apply step %d: %v", step, err)
		}
	}
	t.Fatal("publication exceeded deterministic step bound")
	return State{}
}

func (f *publicationFixture) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, count := range f.posts {
		n += count
	}
	return n
}

func (f *publicationFixture) untilFault(t *testing.T, r Request) (*Engine, Admission, error) {
	t.Helper()
	for step := 0; step < 20; step++ {
		e := f.engine(t)
		a, err := e.Admit(context.Background(), r, f.freshAllocation(t))
		if err != nil {
			t.Fatalf("admit before fault: %v", err)
		}
		_, err = e.Apply(context.Background(), a)
		f.mu.Lock()
		faulted := f.faultUsed
		f.mu.Unlock()
		if faulted {
			return e, a, err
		}
		if err != nil && !errors.Is(err, ErrPartial) {
			t.Fatalf("before fault: %v", err)
		}
	}
	t.Fatal("fault was not exercised")
	return nil, Admission{}, nil
}

func TestPublicationAcceptedHiddenEffectNeverRepostsAfterRestart(t *testing.T) {
	for _, test := range []struct {
		name   string
		action gh.Action
		kind   protocol.Kind
	}{{"milestone", gh.CreateMilestone, ""}, {"issue", gh.CreateIssue, ""}, {"contract", gh.PublishRecord, protocol.KindContract}, {"edge", gh.AddDependency, ""}, {"candidate", gh.PublishRecord, protocol.KindPlan}} {
		t.Run(test.name, func(t *testing.T) {
			f, r := newPublicationFixture(t)
			f.faultAction, f.faultRecordKind = test.action, test.kind
			e, a, err := f.untilFault(t, r)
			if !errors.Is(err, gh.ErrUncertain) {
				t.Fatalf("accepted hidden outcome must be uncertain: %v", err)
			}
			before := f.postCount()
			for i := 0; i < 3; i++ {
				state, err := f.engine(t).Inspect(context.Background(), r)
				if !errors.Is(err, ErrPartial) || state.Status != "partial" || state.Next == nil || state.PlanReference != nil {
					t.Fatalf("hidden effect became complete: %#v %v", state, err)
				}
			}
			if _, err = e.Apply(context.Background(), a); !errors.Is(err, ErrBlocked) {
				t.Fatalf("spent admission reusable: %v", err)
			}
			if _, err = f.engine(t).Admit(context.Background(), r, f.freshAllocation(t)); err == nil {
				t.Fatal("Controller reminted an uncertain generation")
			}
			if f.postCount() != before {
				t.Fatal("recovery repeated a POST")
			}
			f.mu.Lock()
			f.hidden = map[string]bool{}
			f.mu.Unlock()
			state := f.publishAll(t, r)
			if state.Status != "candidate" {
				t.Fatal("visible remote effect did not resume")
			}
			if f.postCount() != 14 {
				t.Fatalf("resumed operation duplicated: %d POSTs", f.postCount())
			}
		})
	}
}

func TestPublicationHiddenAcceptedIntentNeverCreatesEffect(t *testing.T) {
	f, r := newPublicationFixture(t)
	f.faultIntent = true
	e, a, err := f.untilFault(t, r)
	if !errors.Is(err, gh.ErrUncertain) {
		t.Fatal(err)
	}
	if f.postCount() != 1 {
		t.Fatal("effect was sent after lost intent response")
	}
	f.mu.Lock()
	f.hidden = map[string]bool{}
	f.mu.Unlock()
	for i := 0; i < 3; i++ {
		state, err := f.engine(t).Inspect(context.Background(), r)
		if !errors.Is(err, ErrPartial) || state.Status != "partial" {
			t.Fatal("intent-only history became a candidate", err)
		}
	}
	if _, err = e.Apply(context.Background(), a); !errors.Is(err, ErrBlocked) {
		t.Fatal("permit replay", err)
	}
	if _, err = f.engine(t).Admit(context.Background(), r, f.freshAllocation(t)); err == nil {
		t.Fatal("uncertain intent got a replacement event")
	}
	if f.postCount() != 1 {
		t.Fatal("intent-only recovery emitted a POST")
	}
}

func TestPublicationAdmissionCopiesAllowOneStep(t *testing.T) {
	f, r := newPublicationFixture(t)
	e := f.engine(t)
	a, err := e.Admit(context.Background(), r, f.freshAllocation(t))
	if err != nil {
		t.Fatal(err)
	}
	var progressed atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(copy Admission) {
			defer wg.Done()
			<-start
			state, err := e.Apply(context.Background(), copy)
			if errors.Is(err, ErrPartial) && state.Status == "partial" {
				progressed.Add(1)
			} else if !errors.Is(err, ErrBlocked) {
				t.Errorf("unexpected concurrent outcome: %#v %v", state, err)
			}
		}(a)
	}
	close(start)
	wg.Wait()
	if progressed.Load() != 1 || f.postCount() != 2 {
		t.Fatalf("progressed=%d POSTs=%d", progressed.Load(), f.postCount())
	}
}

func TestPublicationDuplicateObjectsNeverBecomeCandidate(t *testing.T) {
	for _, action := range []gh.Action{gh.CreateMilestone, gh.CreateIssue, gh.PublishRecord, gh.AddDependency} {
		t.Run(string(action), func(t *testing.T) {
			f, r := newPublicationFixture(t)
			f.duplicateAction = action
			failed := false
			for i := 0; i < 20; i++ {
				e := f.engine(t)
				a, err := e.Admit(context.Background(), r, f.freshAllocation(t))
				if err != nil {
					failed = true
					break
				}
				_, err = e.Apply(context.Background(), a)
				if err != nil && !errors.Is(err, ErrPartial) {
					failed = true
					break
				}
			}
			if !failed {
				t.Fatal("duplicate effect was accepted")
			}
			before := f.postCount()
			state, err := f.engine(t).Inspect(context.Background(), r)
			if err == nil || state.Status == "candidate" || state.Status == "active" {
				t.Fatalf("duplicate recovered as success: %#v %v", state, err)
			}
			if f.postCount() != before {
				t.Fatal("duplicate recovery wrote")
			}
		})
	}
}

func TestPublicationEveryNativeDriftOrDeletionInvalidatesCandidate(t *testing.T) {
	for _, mode := range []string{"milestone body", "milestone title", "milestone closed", "milestone deleted", "issue body", "issue title", "issue closed", "issue deleted", "issue author", "issue milestone", "contract edited", "contract deleted", "edge deleted", "extra edge", "extra milestone member", "candidate edited", "candidate deleted", "planner Run edited", "policy source edited", "candidate source deleted", "input digest drift"} {
		t.Run(mode, func(t *testing.T) {
			f, r := newPublicationFixture(t)
			state := f.publishAll(t, r)
			before := f.postCount()
			first, last := state.Plan.Members[0], state.Plan.Members[len(state.Plan.Members)-1]
			f.mu.Lock()
			switch mode {
			case "milestone body":
				f.milestones[state.Plan.Subject.Milestone]["description"] = "edited"
			case "milestone title":
				f.milestones[state.Plan.Subject.Milestone]["title"] = "edited"
			case "milestone closed":
				f.milestones[state.Plan.Subject.Milestone]["state"] = "closed"
			case "milestone deleted":
				delete(f.milestones, state.Plan.Subject.Milestone)
			case "issue body":
				f.issues[first.Issue]["body"] = "edited"
			case "issue title":
				f.issues[first.Issue]["title"] = "edited"
			case "issue closed":
				f.issues[first.Issue]["state"] = "closed"
			case "issue deleted":
				delete(f.issues, first.Issue)
			case "issue author":
				f.issues[first.Issue]["user"] = publicationIdentity(9)
			case "issue milestone":
				f.issues[first.Issue]["milestone"] = f.milestones[1]
			case "contract edited", "contract deleted", "candidate edited", "candidate deleted", "planner Run edited":
				ref := first.Contract
				if strings.HasPrefix(mode, "candidate") {
					ref = *state.PlanReference
				}
				if strings.HasPrefix(mode, "planner") {
					ref = r.PlannerRun
				}
				for n, comments := range f.comments {
					for i, c := range comments {
						if c["id"] == ref.DatabaseID {
							if strings.HasSuffix(mode, "deleted") {
								f.comments[n] = append(comments[:i], comments[i+1:]...)
							} else {
								c["body"] = c["body"].(string) + " "
							}
							break
						}
					}
				}
			case "edge deleted":
				f.dependencies[last.Issue] = nil
			case "extra edge":
				f.dependencies[first.Issue] = append(f.dependencies[first.Issue], 7)
			case "extra milestone member":
				f.issues[7]["milestone"] = f.milestones[state.Plan.Subject.Milestone]
			case "policy source edited":
				v := f.sources[r.Authorization]
				v.Bytes = append(v.Bytes, ' ')
				f.sources[r.Authorization] = v
			case "candidate source deleted":
				delete(f.sources, r.Candidate)
			case "input digest drift":
				f.inputs.SHA256 = protocol.SHA256([]byte("different input"))
			}
			f.mu.Unlock()
			for i := 0; i < 2; i++ {
				got, err := f.engine(t).Inspect(context.Background(), r)
				if err == nil || got.Status == "candidate" || got.Status == "active" {
					t.Fatalf("drift admitted: %#v %v", got, err)
				}
			}
			if f.postCount() != before {
				t.Fatal("drift caused repair writes")
			}
		})
	}
}

func TestPublicationExistingMilestoneIsPinnedAndNeverModified(t *testing.T) {
	f, r := newPublicationFixture(t)
	r.ExistingMilestone = &MilestonePin{Resource: publicationResource("milestone", 1), Title: "fixture milestone", BodySHA256: protocol.SHA256([]byte("pre-existing fixture milestone"))}
	f.mu.Lock()
	before := publicationJSON(t, f.milestones[1])
	f.mu.Unlock()
	state := f.publishAll(t, r)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts["/milestones"] != 0 || !reflect.DeepEqual(before, publicationJSON(t, f.milestones[1])) {
		t.Fatal("pre-existing milestone was mutated")
	}
	for _, member := range state.Plan.Members {
		m := f.issues[member.Issue]["milestone"].(map[string]any)
		if fmt.Sprint(m["number"]) != "1" {
			t.Fatal("fixture Issue escaped Milestone #1")
		}
	}
}

func TestPublicationFreshGatewayColdRecovery(t *testing.T) {
	f, r := newPublicationFixture(t)
	state := f.publishAll(t, r)
	if len(state.Plan.Members) != 2 {
		t.Fatalf("member count: %d", len(state.Plan.Members))
	}
	f.mu.Lock()
	before := 0
	for _, n := range f.posts {
		before += n
	}
	f.mu.Unlock()
	for i := 0; i < 3; i++ {
		again, err := f.engine(t).Inspect(context.Background(), r)
		if err != nil || again.Status != "candidate" || !reference(*again.PlanReference, *state.PlanReference) {
			t.Fatal("cold recovery lost exact publication", err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	after := 0
	for _, n := range f.posts {
		after += n
	}
	if before != after || before != 14 {
		t.Fatalf("duplicate writes or missing operation pair: before=%d after=%d", before, after)
	}
}
