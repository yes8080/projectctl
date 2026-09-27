package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

var reconcileSkewMatrixActions = []struct {
	action Action
	path   string
}{
	{CreateIssue, "/issues"},
	{CreateMilestone, "/milestones"},
	{PublishRecord, "/issues/4/comments"},
}

// The winner pauses before sending its first intent POST, after it has claimed
// the shared admission. The loser retains a real, fully buffered pre-intent
// collection until the winner's actual intent/effect requests have completed.
func TestReconcileSkewMatrixSharedPermitStaleSnapshotRecovers(t *testing.T) {
	for _, test := range reconcileSkewMatrixActions {
		t.Run(string(test.action), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			f := newWriteFixture(t)
			base := f.server.Client().Transport
			winnerAtIntent := make(chan struct{})
			loserCaptured := make(chan struct{})
			winnerFinished := make(chan struct{})
			var winnerPaused, loserPaused atomic.Bool
			winner := f.gateway(t, testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost && r.URL.Path == "/repos/octo/pipeline/issues/2/comments" && winnerPaused.CompareAndSwap(false, true) {
					close(winnerAtIntent)
					select {
					case <-loserCaptured:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return base.RoundTrip(r)
			}))
			request := writeRequest(test.action)
			admission, err := winner.AdmitFirstCreate(ctx, request, f.freshAllocation(t))
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				out Outcome
				err error
			}
			winnerResult := make(chan result, 1)
			go func() {
				out, err := winner.ApplyFirst(ctx, request, admission)
				winnerResult <- result{out, err}
				close(winnerFinished)
			}()
			select {
			case <-winnerAtIntent:
			case <-ctx.Done():
				t.Fatal("winner did not reach the pre-intent barrier")
			}
			var intentReads, effectReads atomic.Int64
			loser := f.gateway(t, testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
				isIntentList := r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline/issues/2/comments"
				if isIntentList {
					intentReads.Add(1)
				}
				if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline"+test.path {
					effectReads.Add(1)
				}
				resp, err := base.RoundTrip(r)
				if err != nil || !isIntentList || !loserPaused.CompareAndSwap(false, true) {
					return resp, err
				}
				data, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					return nil, err
				}
				var snapshot []struct {
					Body string `json:"body"`
				}
				if err := json.Unmarshal(data, &snapshot); err != nil {
					return nil, err
				}
				for _, comment := range snapshot {
					if strings.Contains(comment.Body, operationMarker) {
						return nil, errors.New("barrier failed: first loser snapshot already contains an intent")
					}
				}
				close(loserCaptured)
				select {
				case <-winnerFinished:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
				resp.Body = io.NopCloser(bytes.NewReader(data))
				resp.ContentLength = int64(len(data))
				return resp, nil
			}))
			copy := admission
			lost, loserErr := loser.ApplyFirst(ctx, request, copy)
			var won result
			select {
			case won = <-winnerResult:
			case <-ctx.Done():
				t.Fatal("winner did not complete within the bounded barrier")
			}
			if won.err != nil || won.out.State != "applied" {
				t.Fatalf("winner failed: %#v %v", won.out, won.err)
			}
			if loserErr != nil || lost.State != "reconciled" || lost.Intent == nil {
				t.Fatalf("matching pair after stale snapshot was not reconciled: %#v %v", lost, loserErr)
			}
			if intentReads.Load() != 2 || effectReads.Load() != 2 {
				t.Fatalf("want exactly two full passes, got intent/effect reads %d/%d", intentReads.Load(), effectReads.Load())
			}
			admissionMatrixAssertPosts(t, f, 1, 1, test.path)
		})
	}
}

// Seed exact native test facts without performing any POST. This lets the test
// distinguish a safe blocked fresh admission from accidental fallback creation.
func reconcileSkewMatrixSeedEffect(f *writeFixture, p prepared) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch p.intent.Action {
	case CreateIssue:
		item := testServerFixtureIssue(23, &p.objectBody)
		item["title"] = p.title
		if p.intent.Related != nil {
			item["milestone"] = f.milestones[p.intent.Related.Number]
		}
		f.issues[23] = item
	case CreateMilestone:
		item := testServerFixtureMilestone(23, &p.objectBody)
		item["title"] = p.title
		f.milestones[23] = item
	case PublishRecord:
		f.comments[p.intent.Target.Number] = append(f.comments[p.intent.Target.Number], testServerFixtureComment(p.intent.Target.Number, 331, p.objectBody))
	}
}

func TestReconcileSkewMatrixUnresolvedRereadCannotFallbackToCreation(t *testing.T) {
	for _, test := range reconcileSkewMatrixActions {
		for _, mode := range []string{"intent still missing", "both absent on reread"} {
			t.Run(string(test.action)+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				f := newWriteFixture(t)
				base := f.server.Client().Transport
				var intentReads, effectReads atomic.Int64
				g := f.gateway(t, testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline/issues/2/comments" {
						intentReads.Add(1)
					}
					if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline"+test.path {
						read := effectReads.Add(1)
						if mode == "both absent on reread" && read >= 2 {
							return admissionMatrixEmptyResponse(r), nil
						}
					}
					return base.RoundTrip(r)
				}))
				request := writeRequest(test.action)
				admission, err := g.AdmitFirstCreate(ctx, request, f.freshAllocation(t))
				if err != nil {
					t.Fatal(err)
				}
				p, err := g.prepare(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				reconcileSkewMatrixSeedEffect(f, p)
				out, err := g.ApplyFirst(ctx, request, admission)
				if !errors.Is(err, ErrUncertain) || errors.Is(err, ErrConflict) || out.State == "reconciled" || out.State == "applied" {
					t.Fatalf("unresolved observation skew was misclassified: %#v %v", out, err)
				}
				if intentReads.Load() != 2 || effectReads.Load() != 2 {
					t.Fatalf("reread was not bounded to two complete passes: %d/%d", intentReads.Load(), effectReads.Load())
				}
				admissionMatrixAssertPosts(t, f, 0, 0, test.path)
			})
		}
	}
}

func TestReconcileSkewMatrixPositiveConflictsAreNotRetriedAway(t *testing.T) {
	for _, location := range []string{"intent", "effect"} {
		for _, mutation := range []string{"author", "body", "duplicate"} {
			t.Run(location+"/"+mutation, func(t *testing.T) {
				f := newWriteFixture(t)
				base := f.server.Client().Transport
				var intentReads, effectReads atomic.Int64
				g := f.gateway(t, testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
					if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline/issues/2/comments" {
						intentReads.Add(1)
					}
					if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline/issues" {
						effectReads.Add(1)
					}
					return base.RoundTrip(r)
				}))
				request := writeRequest(CreateIssue)
				p, err := g.prepare(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				reconcileSkewMatrixSeedEffect(f, p)
				f.mu.Lock()
				intent := testServerFixtureComment(2, 301, p.intentBody)
				f.comments[2] = append(f.comments[2], intent)
				changed := intent
				if location == "effect" {
					changed = f.issues[23]
				}
				switch mutation {
				case "author":
					changed["user"] = protocol.GitHubIdentity{ID: 99, NodeID: "U_99", Login: "other-author", Type: "User"}
				case "body":
					if location == "intent" {
						changed["body"] = p.intentBody + "\n"
					} else {
						changed["body"] = p.objectBody + "\n"
					}
				case "duplicate":
					if location == "intent" {
						f.comments[2] = append(f.comments[2], testServerFixtureComment(2, 302, p.intentBody))
					} else {
						duplicate := testServerFixtureIssue(24, &p.objectBody)
						duplicate["title"] = p.title
						duplicate["milestone"] = f.milestones[1]
						f.issues[24] = duplicate
					}
				}
				f.mu.Unlock()
				if _, err := g.Apply(context.Background(), request); !errors.Is(err, ErrConflict) {
					t.Fatalf("positive contradictory facts were softened: %v", err)
				}
				wantEffectReads := int64(1)
				if location == "intent" {
					wantEffectReads = 0
				}
				if intentReads.Load() != 1 || effectReads.Load() != wantEffectReads {
					t.Fatalf("positive conflict was retried: intent/effect=%d/%d", intentReads.Load(), effectReads.Load())
				}
				admissionMatrixAssertPosts(t, f, 0, 0, "/issues")
			})
		}
	}
}

func TestReconcileSkewMatrixRereadCannotSubstituteNativeEffectIdentity(t *testing.T) {
	f := newWriteFixture(t)
	base := f.server.Client().Transport
	var intentReads, effectReads atomic.Int64
	var p prepared
	g := f.gateway(t, testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline/issues/2/comments" {
			if intentReads.Add(1) == 1 {
				return admissionMatrixEmptyResponse(r), nil
			}
		}
		if r.Method == http.MethodGet && r.URL.Path == "/repos/octo/pipeline/issues" && effectReads.Add(1) == 2 {
			// Every observed object individually has the correct author, body and
			// native URLs, but a different native object cannot replace the first.
			f.mu.Lock()
			delete(f.issues, 23)
			replacement := testServerFixtureIssue(24, &p.objectBody)
			replacement["title"] = p.title
			replacement["milestone"] = f.milestones[1]
			f.issues[24] = replacement
			f.mu.Unlock()
		}
		return base.RoundTrip(r)
	}))
	request := writeRequest(CreateIssue)
	var err error
	p, err = g.prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	reconcileSkewMatrixSeedEffect(f, p)
	f.mu.Lock()
	f.comments[2] = append(f.comments[2], testServerFixtureComment(2, 301, p.intentBody))
	f.mu.Unlock()
	if out, err := g.Apply(context.Background(), request); !errors.Is(err, ErrConflict) || out.State == "reconciled" {
		t.Fatalf("different native effect with identical payload was substituted: %#v %v", out, err)
	}
	if intentReads.Load() != 2 || effectReads.Load() != 2 {
		t.Fatalf("identity substitution check did not use exactly two complete passes: %d/%d", intentReads.Load(), effectReads.Load())
	}
	admissionMatrixAssertPosts(t, f, 0, 0, "/issues")
}
