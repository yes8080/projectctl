package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Force the losing observer to retain an empty intent snapshot while the
// winning holder finishes the unique intent/effect. No sleeps or scheduling
// luck are involved: the observer cannot fetch effects until the winner returns.
func TestReconcileSkewCompletedWinnerIsNotAnOrphanConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := newWriteFixture(t)
	winner := f.gateway(t, nil)
	request := writeRequest(CreateIssue)
	admission, err := winner.AdmitFirstCreate(ctx, request, f.freshAllocation(t))
	if err != nil {
		t.Fatal(err)
	}
	intentSnapshot := make(chan struct{})
	winnerFinished := make(chan struct{})
	var intercepted atomic.Bool
	base := f.server.Client().Transport
	observer := f.gateway(t, testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/2/comments") && intercepted.CompareAndSwap(false, true) {
			close(intentSnapshot)
			select {
			case <-winnerFinished:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("[]")), Request: r}, nil
		}
		return base.RoundTrip(r)
	}))
	type result struct {
		out Outcome
		err error
	}
	observed := make(chan result, 1)
	go func() {
		out, err := observer.Apply(ctx, request)
		observed <- result{out, err}
	}()
	select {
	case <-intentSnapshot:
	case <-ctx.Done():
		t.Fatal("observer did not reach intent barrier")
	}
	won, winnerErr := winner.ApplyFirst(ctx, request, admission)
	close(winnerFinished)
	var lost result
	select {
	case lost = <-observed:
	case <-ctx.Done():
		t.Fatal("observer did not finish bounded reconciliation")
	}
	if winnerErr != nil || won.State != "applied" {
		t.Fatalf("winner did not finish: %#v %v", won, winnerErr)
	}
	if lost.err != nil || lost.out.State != "reconciled" || lost.out.Intent == nil || lost.out.Issue == nil {
		t.Fatalf("complete matching pair misclassified by skewed observer: %#v %v", lost.out, lost.err)
	}
	admissionMatrixAssertPosts(t, f, 1, 1, "/issues")
}

func TestReconcileSkewRereadFailuresRemainUncertain(t *testing.T) {
	for _, fail := range []string{"intent", "effect"} {
		t.Run(fail, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			f := newWriteFixture(t)
			base := f.server.Client().Transport
			var intentReads, effectReads atomic.Int64
			g := f.gateway(t, testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodGet {
					failed := false
					switch r.URL.Path {
					case "/repos/octo/pipeline/issues/2/comments":
						failed = intentReads.Add(1) == 2 && fail == "intent"
					case "/repos/octo/pipeline/issues":
						failed = effectReads.Add(1) == 2 && fail == "effect"
					}
					if failed {
						return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unavailable")), Request: r}, nil
					}
				}
				return base.RoundTrip(r)
			}))
			request := writeRequest(CreateIssue)
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
			if !errors.Is(err, ErrUncertain) || errors.Is(err, ErrConflict) || out.State != "uncertain" {
				t.Fatalf("failed reread became a definitive outcome: %#v %v", out, err)
			}
			wantEffect := int64(2)
			if fail == "intent" {
				wantEffect = 1
			}
			if intentReads.Load() != 2 || effectReads.Load() != wantEffect {
				t.Fatalf("failed reread was retried or incomplete incorrectly: %d/%d", intentReads.Load(), effectReads.Load())
			}
			admissionMatrixAssertPosts(t, f, 0, 0, "/issues")
		})
	}
}
