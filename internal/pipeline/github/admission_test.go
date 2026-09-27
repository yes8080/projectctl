package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// PR13-P1-INTENT-UNKNOWN-REPOST: the server accepts the intent, but its
// response is lost and all subsequent list reads temporarily hide it.
func TestAcceptedInvisibleIntentDoesNotRegainFirstWriteOnRestart(t *testing.T) {
	f := newWriteFixture(t)
	base := f.server.Client().Transport
	transport := testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/2/comments") {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("[]")), Request: r}, nil
		}
		resp, err := base.RoundTrip(r)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/2/comments") {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, context.DeadlineExceeded
		}
		return resp, err
	})
	request := writeRequest(CreateIssue)
	g := f.gateway(t, transport)
	admission, err := g.AdmitFirstCreate(context.Background(), request, f.freshAllocation(t))
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		g = f.gateway(t, transport)
		var err error
		if attempt == 0 {
			_, err = g.ApplyFirst(context.Background(), request, admission)
		} else {
			_, err = g.Apply(context.Background(), request)
		}
		if !errors.Is(err, ErrUncertain) {
			t.Fatalf("attempt %d: expected uncertainty, got %v", attempt, err)
		}
	}
	// Retaining/copying the original in-memory permit is not a retry right.
	copy := admission
	if _, err := f.gateway(t, transport).ApplyFirst(context.Background(), request, copy); !errors.Is(err, ErrUncertain) {
		t.Fatalf("spent permit regained first-write eligibility: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	intents := 0
	for _, item := range f.comments[2] {
		if strings.Contains(item["body"].(string), operationMarker) {
			intents++
		}
	}
	if f.posts["/issues/2/comments"] != 1 || intents != 1 || f.posts["/issues"] != 0 {
		t.Fatalf("accepted intent posts=%d, persisted intents=%d, effect posts=%d; must be 1/1/0", f.posts["/issues/2/comments"], intents, f.posts["/issues"])
	}
}
