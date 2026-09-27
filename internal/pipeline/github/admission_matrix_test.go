package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func admissionMatrixAssertPosts(t *testing.T, f *writeFixture, intent, effect int, effectPath string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts["/issues/2/comments"] != intent || f.posts[effectPath] != effect {
		t.Fatalf("unexpected dispatch count: intents=%d effects=%d; want %d/%d", f.posts["/issues/2/comments"], f.posts[effectPath], intent, effect)
	}
	total := 0
	for _, count := range f.posts {
		total += count
	}
	if total != intent+effect {
		t.Fatalf("unexpected additional write path: %#v", f.posts)
	}
}

func admissionMatrixEmptyResponse(r *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("[]")), ContentLength: 2, Request: r}
}

func TestAdmissionMatrixExactScopeCannotBeReused(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*writeFixture, *Gateway, *Request)
	}{
		{"operation", func(_ *writeFixture, _ *Gateway, r *Request) { r.OperationID += "-other" }},
		{"title", func(_ *writeFixture, _ *Gateway, r *Request) { r.Title += " changed" }},
		{"body", func(_ *writeFixture, _ *Gateway, r *Request) { r.Body += "\nchanged" }},
		{"milestone", func(_ *writeFixture, _ *Gateway, r *Request) { r.Related = nil }},
		{"action", func(_ *writeFixture, _ *Gateway, r *Request) { r.Action = CreateMilestone; r.Related = nil }},
		{"project", func(_ *writeFixture, _ *Gateway, r *Request) { r.Project.Repo = "different" }},
		{"publisher", func(f *writeFixture, g *Gateway, _ *Request) {
			other := protocol.GitHubIdentity{ID: 99, NodeID: "U_99", Login: "other-publisher", Type: "User"}
			f.mu.Lock()
			f.user = other
			f.mu.Unlock()
			g.publisher = other
			// A new publisher may be policy-authorized, but cannot inherit this
			// previous publisher's already issued one-shot capability.
			g.authorize = func(context.Context, Intent, protocol.GitHubIdentity) error { return nil }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newWriteFixture(t)
			g := f.gateway(t, nil)
			request := writeRequest(CreateIssue)
			admission, err := g.AdmitFirstCreate(context.Background(), request, f.freshAllocation(t))
			if err != nil {
				t.Fatal(err)
			}
			changed := request
			test.change(f, g, &changed)
			if _, err := g.ApplyFirst(context.Background(), changed, admission); err == nil {
				t.Fatal("first-create admission escaped its exact request or publisher")
			}
			admissionMatrixAssertPosts(t, f, 0, 0, "/issues")
		})
	}
}

func TestAdmissionMatrixSourceReplayAndUnusableValues(t *testing.T) {
	f := newWriteFixture(t)
	g := f.gateway(t, nil)
	request := writeRequest(CreateIssue)
	source := f.freshAllocation(t)
	admission, err := g.AdmitFirstCreate(context.Background(), request, source)
	if err != nil {
		t.Fatal(err)
	}
	// The same trusted event source cannot mint a second capability, even when
	// the receiving Gateway is reconstructed. Empty remote lists are irrelevant.
	for _, receiver := range []*Gateway{g, f.gateway(t, nil)} {
		if _, err := receiver.AdmitFirstCreate(context.Background(), request, source); !errors.Is(err, ErrDenied) {
			t.Fatalf("consumed allocation source was replayed: %v", err)
		}
	}
	if _, err := g.AdmitFirstCreate(context.Background(), request, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("nil source minted authority: %v", err)
	}
	if _, err := json.Marshal(admission); err == nil {
		t.Fatal("first-create capability was serializable")
	}
	for _, raw := range []string{`{}`, `{"used":false,"generation":1}`, `null`} {
		var restored FirstCreateAdmission
		if err := json.Unmarshal([]byte(raw), &restored); err == nil {
			t.Fatalf("JSON restored first-create authority: %s", raw)
		}
		if _, err := g.ApplyFirst(context.Background(), request, restored); err == nil {
			t.Fatal("failed JSON restoration produced usable authority")
		}
	}
	if _, err := g.ApplyFirst(context.Background(), request, FirstCreateAdmission{}); !errors.Is(err, ErrUncertain) {
		t.Fatalf("zero admission authorized a write: %v", err)
	}
	admissionMatrixAssertPosts(t, f, 0, 0, "/issues")
}

func TestAdmissionMatrixCopiesShareConcurrentConsumption(t *testing.T) {
	f := newWriteFixture(t)
	g := f.gateway(t, nil)
	request := writeRequest(CreateIssue)
	admission, err := g.AdmitFirstCreate(context.Background(), request, f.freshAllocation(t))
	if err != nil {
		t.Fatal(err)
	}
	const callers = 12
	start := make(chan struct{})
	type attempt struct {
		out Outcome
		err error
	}
	results := make(chan attempt, callers)
	for i := 0; i < callers; i++ {
		copy := admission
		go func() {
			<-start
			out, err := g.ApplyFirst(context.Background(), request, copy)
			results <- attempt{out: out, err: err}
		}()
	}
	close(start)
	applied := 0
	for i := 0; i < callers; i++ {
		result := <-results
		if result.err != nil && !errors.Is(result.err, ErrUncertain) {
			t.Errorf("unexpected concurrent admission error: %v", result.err)
		}
		if result.out.State == "applied" && result.err == nil {
			applied++
		}
	}
	if applied != 1 {
		t.Fatalf("shared copies must allow exactly one dispatch, got %d", applied)
	}
	admissionMatrixAssertPosts(t, f, 1, 1, "/issues")
}

func TestAdmissionMatrixRunGenerationIsReverifiedAndFailureBurnsPermit(t *testing.T) {
	for _, mutation := range []string{"generation", "body", "deleted"} {
		t.Run(mutation, func(t *testing.T) {
			f := newWriteFixture(t)
			g := f.gateway(t, nil)
			request := writeRequest(CreateIssue)
			admission, err := g.AdmitFirstCreate(context.Background(), request, f.freshAllocation(t))
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			var run map[string]any
			for _, item := range f.comments[2] {
				if strings.HasPrefix(item["body"].(string), protocol.Marker) {
					run = item
					break
				}
			}
			if run == nil {
				f.mu.Unlock()
				t.Fatal("fixture allocation Run missing")
			}
			original := run["body"].(string)
			switch mutation {
			case "generation":
				run["body"] = strings.Replace(original, `"assignment_generation":1`, `"assignment_generation":2`, 1)
				if run["body"] == original {
					f.mu.Unlock()
					t.Fatal("fixture generation was not changed")
				}
			case "body":
				run["body"] = original + "\n"
			case "deleted":
				f.comments[2] = nil
			}
			f.mu.Unlock()
			if _, err := g.ApplyFirst(context.Background(), request, admission); err == nil {
				t.Fatal("changed or missing allocation Run was not reread")
			}
			admissionMatrixAssertPosts(t, f, 0, 0, "/issues")
			// Restoring readability does not restore consumed authority.
			f.mu.Lock()
			run["body"] = original
			f.comments[2] = []map[string]any{run}
			f.mu.Unlock()
			if _, err := g.ApplyFirst(context.Background(), request, admission); !errors.Is(err, ErrUncertain) {
				t.Fatalf("failed allocation verification made the permit reusable: %v", err)
			}
			admissionMatrixAssertPosts(t, f, 0, 0, "/issues")
		})
	}
}

func TestAdmissionMatrixExistingIntentBurnsPermitBeforeLaterInvisibility(t *testing.T) {
	f := newWriteFixture(t)
	base := f.server.Client().Transport
	var hideIntent atomic.Bool
	transport := testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if hideIntent.Load() && r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline/issues/2/comments" {
			return admissionMatrixEmptyResponse(r), nil
		}
		return base.RoundTrip(r)
	})
	g := f.gateway(t, transport)
	request := writeRequest(CreateIssue)
	admission, err := g.AdmitFirstCreate(context.Background(), request, f.freshAllocation(t))
	if err != nil {
		t.Fatal(err)
	}
	p, err := g.prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.comments[2] = append(f.comments[2], testServerFixtureComment(2, 301, p.intentBody))
	f.mu.Unlock()
	copy := admission
	if _, err := g.ApplyFirst(context.Background(), request, admission); !errors.Is(err, ErrUncertain) {
		t.Fatalf("existing intent without effect did not stop first create: %v", err)
	}
	hideIntent.Store(true)
	g = f.gateway(t, transport)
	if _, err := g.ApplyFirst(context.Background(), request, copy); !errors.Is(err, ErrUncertain) {
		t.Fatalf("later empty list restored previously inspected admission: %v", err)
	}
	admissionMatrixAssertPosts(t, f, 0, 0, "/issues")
}

func TestAdmissionMatrixAcceptedInvisibleEffectsNeverRepeatAcrossRestart(t *testing.T) {
	for _, test := range []struct {
		action Action
		path   string
	}{
		{CreateIssue, "/issues"},
		{CreateMilestone, "/milestones"},
		{PublishRecord, "/issues/4/comments"},
		{AddDependency, "/issues/4/dependencies/blocked_by"},
	} {
		t.Run(string(test.action), func(t *testing.T) {
			f := newWriteFixture(t)
			base := f.server.Client().Transport
			var accepted, hideEffect atomic.Bool
			hideEffect.Store(true)
			transport := testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline"+test.path && accepted.Load() && hideEffect.Load() {
					return admissionMatrixEmptyResponse(r), nil
				}
				resp, err := base.RoundTrip(r)
				if r.Method == http.MethodPost && r.URL.Path == "/repos/octo/pipeline"+test.path {
					if err != nil {
						return resp, err
					}
					if resp.StatusCode != http.StatusCreated {
						resp.Body.Close()
						return nil, fmt.Errorf("fixture did not accept effect")
					}
					// The real fixture has committed the effect. Lose its response,
					// then conceal collection visibility without deleting the fact.
					resp.Body.Close()
					accepted.Store(true)
					return nil, context.DeadlineExceeded
				}
				return resp, err
			})
			g := f.gateway(t, transport)
			request := writeRequest(test.action)
			admission, err := g.AdmitFirstCreate(context.Background(), request, f.freshAllocation(t))
			if err != nil {
				t.Fatal(err)
			}
			copied := admission
			out, err := g.ApplyFirst(context.Background(), request, admission)
			if !errors.Is(err, ErrUncertain) || out.Intent == nil || !accepted.Load() {
				t.Fatalf("accepted invisible effect must remain uncertain: %#v %v", out, err)
			}
			admissionMatrixAssertPosts(t, f, 1, 1, test.path)
			for attempt := 0; attempt < 2; attempt++ {
				g = f.gateway(t, transport)
				if _, err := g.Apply(context.Background(), request); !errors.Is(err, ErrUncertain) {
					t.Fatalf("restart invented a definitive hidden outcome: %v", err)
				}
				if _, err := g.ApplyFirst(context.Background(), request, copied); !errors.Is(err, ErrUncertain) {
					t.Fatalf("copied admission regained authority after restart: %v", err)
				}
				admissionMatrixAssertPosts(t, f, 1, 1, test.path)
			}
			// When the original committed object becomes visible, recovery can
			// confirm it using reads only; there is no retry or replacement.
			hideEffect.Store(false)
			g = f.gateway(t, transport)
			out, err = g.Apply(context.Background(), request)
			if err != nil || out.State != "reconciled" || out.Intent == nil {
				t.Fatalf("visible original effect was not reconciled: %#v %v", out, err)
			}
			switch test.action {
			case CreateIssue, AddDependency:
				if out.Issue == nil {
					t.Fatal("original native issue effect missing")
				}
			case CreateMilestone:
				if out.Milestone == nil {
					t.Fatal("original native milestone effect missing")
				}
			case PublishRecord:
				if out.Comment == nil {
					t.Fatal("original native record comment missing")
				}
			}
			admissionMatrixAssertPosts(t, f, 1, 1, test.path)
		})
	}
}
