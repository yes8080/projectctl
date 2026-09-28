package plan

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
)

type liveRecordingTransport struct{ calls int }

func (r *liveRecordingTransport) RoundTrip(q *http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}, Request: q}, nil
}

func TestLiveHostWriteScopeAndCumulativeLimits(t *testing.T) {
	for _, tt := range []struct {
		name, method, path, body string
		comments, prComments     int
		allowed                  bool
	}{
		{"control append", "POST", "/issues/2/comments", `{"body":"issue9-test/op"}`, 0, 0, true},
		{"one PR3 append", "POST", "/issues/3/comments", `{"body":"issue9-test/op"}`, 0, 0, true},
		{"PR3 second denied", "POST", "/issues/3/comments", `{"body":"issue9-test/op"}`, 1, 1, false},
		{"cumulative limit", "POST", "/issues/2/comments", `{"body":"issue9-test/op"}`, 24, 1, false},
		{"no prefix", "POST", "/issues/2/comments", `{"body":"unrelated"}`, 0, 0, false},
		{"other old issue", "POST", "/issues/4/comments", `{"body":"issue9-test/op"}`, 0, 0, false},
		{"new issue comments", "POST", "/issues/100/comments", `{"body":"issue9-test/op"}`, 0, 0, true},
		{"old issue close", "PATCH", "/issues/2", `{"state":"closed"}`, 0, 0, false},
		{"new exact close", "PATCH", "/issues/100", `{"state":"closed"}`, 0, 0, true},
		{"close plus edit denied", "PATCH", "/issues/100", `{"state":"closed","title":"changed"}`, 0, 0, false},
		{"milestone denied", "POST", "/milestones", `{"body":"issue9-test/op"}`, 0, 0, false},
		{"edge denied", "POST", "/issues/100/dependencies/blocked_by", `{"issue_id":10}`, 0, 0, false},
		{"delete denied", "DELETE", "/issues/comments/1", `{}`, 0, 0, false},
		{"correct issue milestone", "POST", "/issues", `{"body":"issue9-test/op","milestone":1}`, 0, 0, true},
		{"wrong issue milestone", "POST", "/issues", `{"body":"issue9-test/op","milestone":2}`, 0, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := &liveRecordingTransport{}
			h := &liveHost{prefix: "issue9-test", phase: "publish", comments: tt.comments, prComments: tt.prComments, created: map[int64]gh.Issue{100: {}}, transport: record}
			q, _ := http.NewRequest(tt.method, "https://api.github.com/repos/yes8080/projectctl"+tt.path, strings.NewReader(tt.body))
			resp, err := h.RoundTrip(q)
			if resp != nil {
				resp.Body.Close()
			}
			if (err == nil) != tt.allowed || record.calls != map[bool]int{true: 1, false: 0}[tt.allowed] {
				t.Fatalf("allowed=%v err=%v calls=%d", tt.allowed, err, record.calls)
			}
		})
	}
}

func TestLiveHostNoEmptyCleanupSuccess(t *testing.T) {
	h := &liveHost{created: map[int64]gh.Issue{}}
	if err := h.cleanup(context.Background()); err == nil {
		t.Fatal("empty/unknown cleanup falsely succeeded")
	}
}

func TestLiveCleanupExpectedBodyMatchesRealGateway(t *testing.T) {
	f, r := newPublicationFixture(t)
	state := f.publishAll(t, r)
	for _, slice := range f.candidate.Contracts {
		for _, member := range state.Plan.Members {
			v, err := f.gateway(t).ReadIssue(context.Background(), member.Issue)
			if err != nil {
				t.Fatal(err)
			}
			if v.Title != slice.Goal {
				continue
			}
			want := liveExpectedIssueBody(r.Input.Project, publicationResource("issue", 2), publicationResource("repository", 0), *v.Milestone, r.Candidate, r.OperationPrefix, slice)
			if v.Content.Body == nil || *v.Content.Body != want {
				t.Fatal("cleanup cannot reconstruct exact production Gateway object body")
			}
		}
	}
}

func TestLiveFixtureRejectsDuplicateAndUnknownFields(t *testing.T) {
	for _, b := range []string{`{"candidate":{},"candidate":{}}`, `{"unknown":true}`} {
		var v liveBundle
		if liveDecode([]byte(b), &v) == nil {
			t.Fatal("invalid fixture accepted")
		}
	}
}
