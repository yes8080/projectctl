package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func TestReadCollectionsTraverseShortNativeArrayPages(t *testing.T) {
	tests := []struct {
		name, path string
		filters    map[string]string
		object     func(int64) any
		read       func(*Gateway) (int, error)
	}{
		{"issues", "/repos/octo/pipeline/issues", map[string]string{"state": "all", "sort": "created", "direction": "asc"}, func(n int64) any { return testServerFixtureIssue(n, nil) }, func(g *Gateway) (int, error) { x, e := g.ListIssues(context.Background()); return len(x), e }},
		{"comments", "/repos/octo/pipeline/issues/4/comments", nil, func(n int64) any { return testServerFixtureComment(4, 300+n, "raw body") }, func(g *Gateway) (int, error) {
			x, e := g.ListComments(context.Background(), Resource{Kind: "issue", Number: 4})
			return len(x), e
		}},
		{"milestones", "/repos/octo/pipeline/milestones", map[string]string{"state": "all", "sort": "due_on", "direction": "asc"}, func(n int64) any { return testServerFixtureMilestone(n, nil) }, func(g *Gateway) (int, error) { x, e := g.ListMilestones(context.Background()); return len(x), e }},
		{"blocked by", "/repos/octo/pipeline/issues/4/dependencies/blocked_by", nil, func(n int64) any { return testServerFixtureIssue(n, nil) }, func(g *Gateway) (int, error) {
			x, e := g.ListDependencies(context.Background(), 4, "blocked_by")
			return len(x), e
		}},
		{"blocking", "/repos/octo/pipeline/issues/4/dependencies/blocking", nil, func(n int64) any { return testServerFixtureIssue(n, nil) }, func(g *Gateway) (int, error) {
			x, e := g.ListDependencies(context.Background(), 4, "blocking")
			return len(x), e
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != test.path || r.URL.Query().Get("per_page") != "100" {
					t.Errorf("unexpected collection request: %s %s", r.Method, r.URL)
				}
				for key, value := range test.filters {
					if r.URL.Query().Get(key) != value {
						t.Errorf("lost %s filter", key)
					}
				}
				if r.URL.Query().Get("page") == "1" {
					q := r.URL.Query()
					q.Set("page", "2")
					w.Header().Add("Link", "<"+r.URL.Path+"?"+q.Encode()+">; rel=\"next\"")
					testServerFixtureJSON(t, w, []any{test.object(10)})
					return
				}
				if r.URL.Query().Get("page") != "2" {
					t.Errorf("unexpected page: %s", r.URL)
				}
				testServerFixtureJSON(t, w, []any{test.object(11)})
			})
			count, err := test.read(g)
			if err != nil || count != 2 || calls.Load() != 2 {
				t.Fatalf("short page truncated: count=%d calls=%d err=%v", count, calls.Load(), err)
			}
		})
	}
}

func TestReadCollectionsRejectDuplicateNativeObjects(t *testing.T) {
	tests := []struct {
		name    string
		objects []any
		read    func(*Gateway) error
	}{
		{"issues", []any{testServerFixtureIssue(4, nil), testServerFixtureIssue(4, nil)}, func(g *Gateway) error { _, e := g.ListIssues(context.Background()); return e }},
		{"comments", []any{testServerFixtureComment(4, 31, "body"), testServerFixtureComment(4, 31, "body")}, func(g *Gateway) error {
			_, e := g.ListComments(context.Background(), Resource{Kind: "issue", Number: 4})
			return e
		}},
		{"milestones", []any{testServerFixtureMilestone(1, nil), testServerFixtureMilestone(1, nil)}, func(g *Gateway) error { _, e := g.ListMilestones(context.Background()); return e }},
		{"dependencies", []any{testServerFixtureIssue(5, nil), testServerFixtureIssue(5, nil)}, func(g *Gateway) error { _, e := g.ListDependencies(context.Background(), 4, "blocked_by"); return e }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("page") == "1" {
					q := r.URL.Query()
					q.Set("page", "2")
					w.Header().Set("Link", "<"+r.URL.Path+"?"+q.Encode()+">; rel=next")
					testServerFixtureJSON(t, w, test.objects[:1])
					return
				}
				testServerFixtureJSON(t, w, test.objects[1:])
			})
			if err := test.read(g); !errors.Is(err, ErrConflict) {
				t.Fatalf("duplicate object was not a conflict: %v", err)
			}
		})
	}
	for _, field := range []string{"id", "node_id", "number"} {
		t.Run("issue collision "+field, func(t *testing.T) {
			a, b := testServerFixtureIssue(4, nil), testServerFixtureIssue(5, nil)
			b[field] = a[field]
			if field == "number" {
				b["url"] = a["url"]
				b["html_url"] = a["html_url"]
			}
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, []any{a, b}) })
			if _, err := g.ListIssues(context.Background()); !errors.Is(err, ErrConflict) {
				t.Fatalf("stable identity collision accepted: %v", err)
			}
		})
	}
}

func TestPullRequestsAreFilteredAndCannotMasqueradeAsIssues(t *testing.T) {
	pr := testServerFixtureIssue(5, nil)
	pr["pull_request"] = map[string]any{"url": "https://api.github.com/repos/octo/pipeline/pulls/5"}
	pr["html_url"] = "https://github.com/octo/pipeline/pull/5"
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octo/pipeline/issues":
			testServerFixtureJSON(t, w, []any{testServerFixtureIssue(4, nil), pr})
		case "/repos/octo/pipeline/issues/5":
			testServerFixtureJSON(t, w, pr)
		default:
			testServerFixtureJSON(t, w, []any{pr})
		}
	})
	issues, err := g.ListIssues(context.Background())
	if err != nil || len(issues) != 1 || issues[0].Number != 4 {
		t.Fatalf("PR list filtering failed: %#v %v", issues, err)
	}
	if _, err := g.ReadIssue(context.Background(), 5); err == nil {
		t.Fatal("PR accepted as issue")
	}
	if _, err := g.ListDependencies(context.Background(), 4, "blocked_by"); err == nil {
		t.Fatal("PR accepted as native dependency")
	}
	delete(pr, "pull_request")
	if _, err := g.ReadIssue(context.Background(), 5); err == nil {
		t.Fatal("pull URL accepted without native PR marker")
	}
}

func TestPullRequestMarkerRequiresNativeObjectAndURL(t *testing.T) {
	for _, marker := range []any{
		false, 123, "pull request", []any{}, map[string]any{},
		map[string]any{"url": "https://api.github.com/repos/octo/pipeline/pulls/6"},
		map[string]any{"url": "https://api.github.com/repos/other/pipeline/pulls/5"},
	} {
		t.Run(fmt.Sprint(marker), func(t *testing.T) {
			pr := testServerFixtureIssue(5, nil)
			pr["pull_request"] = marker
			pr["html_url"] = "https://github.com/octo/pipeline/pull/5"
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				testServerFixtureJSON(t, w, []any{pr})
			})
			if items, err := g.ListIssues(context.Background()); err == nil || items != nil {
				t.Fatalf("malformed PR marker was silently filtered as a real PR: %#v %v", items, err)
			}
		})
	}
}

func TestNativeContentIdentityAndRawDigestArePreserved(t *testing.T) {
	body := "\r\n  markdown <>& 中文\t\n"
	description := "  milestone description\r\n"
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octo/pipeline/issues/4":
			n := testServerFixtureIssue(4, &body)
			n["milestone"] = testServerFixtureMilestone(1, &description)
			testServerFixtureJSON(t, w, n)
		case "/repos/octo/pipeline/issues/comments/31":
			testServerFixtureJSON(t, w, testServerFixtureComment(4, 31, body))
		case "/repos/octo/pipeline/milestones/1":
			n := testServerFixtureMilestone(1, &description)
			n["body"] = "wrong field"
			n["user"] = protocol.GitHubIdentity{ID: 99, NodeID: "U_99", Login: "not-creator", Type: "User"}
			testServerFixtureJSON(t, w, n)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
		}
	})
	i, err := g.ReadIssue(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.ReadComment(context.Background(), 31)
	if err != nil {
		t.Fatal(err)
	}
	m, err := g.ReadMilestone(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if i.DatabaseID != 1004 || i.NodeID != "I_4" || i.Author != testServerFixtureAuthor() || i.Milestone == nil || i.Milestone.DatabaseID != 2001 {
		t.Fatalf("lost native issue facts: %#v", i)
	}
	if c.DatabaseID != 31 || c.NodeID != "IC_31" || c.Parent != (Resource{Kind: "issue", Number: 4}) || c.Author != testServerFixtureAuthor() {
		t.Fatalf("lost comment facts: %#v", c)
	}
	if m.Author != testServerFixtureAuthor() || m.DatabaseID != 2001 || m.NodeID != "M_1" {
		t.Fatalf("Milestone creator not preserved: %#v", m)
	}
	for _, item := range []struct {
		got  Content
		want string
	}{{i.Content, body}, {c.Content, body}, {m.Content, description}} {
		if item.got.Body == nil || *item.got.Body != item.want || item.got.BodySHA256 != protocol.SHA256([]byte(item.want)) || item.got.IsRecord {
			t.Fatalf("native body was changed or misclassified: %#v", item.got)
		}
	}
}

func TestContentDoesNotInventBodiesOrApproveMalformedRecords(t *testing.T) {
	if got := content(nil); !reflect.DeepEqual(got, Content{}) {
		t.Fatalf("nil native body invented content: %#v", got)
	}
	got := content(testServerFixtureString(""))
	if got.Body == nil || got.BodySHA256 != protocol.SHA256(nil) {
		t.Fatal("empty and absent native body were conflated")
	}
	body := protocol.Marker + "  {\"schema\":\"unknown\",\"kind\":\"approval\",\"actor\":\"owner\"}\n"
	got = content(&body)
	digest, err := protocol.DigestJSON([]byte(strings.TrimSpace(strings.TrimPrefix(body, protocol.Marker))))
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsRecord || got.RecordValid || got.BodySHA256 != protocol.SHA256([]byte(body)) || got.CanonicalSHA256 != digest {
		t.Fatalf("malformed record trusted or digests lost: %#v", got)
	}
	body = protocol.Marker + ` {"id":1,"id":2}`
	got = content(&body)
	if !got.IsRecord || got.RecordValid || got.CanonicalSHA256 != "" {
		t.Fatal("ambiguous record got a canonical digest")
	}
}

func TestContentValidRecordKeepsRawAndCanonicalDigestsDistinct(t *testing.T) {
	record := &protocol.Approval{
		Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindApproval, Project: testServerFixtureProject(), Subject: protocol.Subject{Issue: 4}, OperationID: "test-record-1", Version: 1},
		Candidate: protocol.Reference{
			Kind: "issue_comment", DatabaseID: 31, NodeID: "IC_31",
			URL:        "https://github.com/octo/pipeline/issues/4#issuecomment-31",
			BodySHA256: strings.Repeat("a", 64), CanonicalSHA256: strings.Repeat("b", 64), Author: testServerFixtureAuthor(),
		},
		PolicySHA256: strings.Repeat("c", 64), Decision: "REJECTED", Reason: "offline fixture",
	}
	encoded, err := protocol.EncodeComment(record)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := protocol.Encode(record)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(encoded, protocol.Marker+" ", protocol.Marker+"\n  ", 1) + "\r\n"
	got := content(&body)
	if !got.IsRecord || !got.RecordValid || got.Body == nil || *got.Body != body || got.BodySHA256 != protocol.SHA256([]byte(body)) || got.CanonicalSHA256 != protocol.SHA256(canonical) {
		t.Fatalf("valid record content or digest changed: %#v", got)
	}
	if got.BodySHA256 == got.CanonicalSHA256 {
		t.Fatal("raw comment and canonical JSON domains were conflated")
	}
}

func TestReadRejectsWrongNativeIdentityAndParent(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing ID", func(n map[string]any) { delete(n, "id") }},
		{"missing node", func(n map[string]any) { delete(n, "node_id") }},
		{"missing author", func(n map[string]any) { delete(n, "user") }},
		{"wrong API repository", func(n map[string]any) { n["repository_url"] = "https://api.github.com/repos/other/pipeline" }},
		{"wrong web repository", func(n map[string]any) { n["html_url"] = "https://github.com/other/pipeline/issues/4" }},
		{"wrong requested number", func(n map[string]any) {
			for k, v := range testServerFixtureIssue(5, nil) {
				n[k] = v
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			n := testServerFixtureIssue(4, nil)
			test.mutate(n)
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, n) })
			if _, err := g.ReadIssue(context.Background(), 4); err == nil {
				t.Fatal("inconsistent native identity accepted")
			}
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"different parent", func(n map[string]any) {
			for k, v := range testServerFixtureComment(5, 31, "body") {
				n[k] = v
			}
		}},
		{"API parent mismatch", func(n map[string]any) { n["issue_url"] = "https://api.github.com/repos/octo/pipeline/issues/5" }},
		{"fragment mismatch", func(n map[string]any) { n["html_url"] = "https://github.com/octo/pipeline/issues/4#issuecomment-32" }},
		{"missing raw body", func(n map[string]any) { delete(n, "body") }},
		{"missing author", func(n map[string]any) { delete(n, "user") }},
	} {
		t.Run("comment "+test.name, func(t *testing.T) {
			n := testServerFixtureComment(4, 31, "body")
			test.mutate(n)
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, []any{n}) })
			if _, err := g.ListComments(context.Background(), Resource{Kind: "issue", Number: 4}); err == nil {
				t.Fatal("wrong comment parent/body/author accepted")
			}
		})
	}
}

func TestMilestoneLegacyURLAndNullableDescription(t *testing.T) {
	for _, webURL := range []string{"https://github.com/octo/pipeline/milestone/1", "https://github.com/octo/pipeline/milestones/v1.0"} {
		t.Run(webURL, func(t *testing.T) {
			n := testServerFixtureMilestone(1, nil)
			n["html_url"] = webURL
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, n) })
			m, err := g.ReadMilestone(context.Background(), 1)
			if err != nil || m.Content.Body != nil || m.Content.BodySHA256 != "" {
				t.Fatalf("native milestone URL/nullable description failed: %#v %v", m, err)
			}
		})
	}
	for _, mutate := range []func(map[string]any){func(n map[string]any) { delete(n, "creator") }, func(n map[string]any) { n["html_url"] = "https://github.com/other/pipeline/milestone/1" }, func(n map[string]any) { n["url"] = "https://api.github.com/repos/octo/pipeline/milestones/2" }} {
		n := testServerFixtureMilestone(1, nil)
		mutate(n)
		g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, n) })
		if _, err := g.ReadMilestone(context.Background(), 1); err == nil {
			t.Fatal("unverified milestone identity accepted")
		}
	}
}

func TestReadInvalidArgumentsMakeNoHTTPRequest(t *testing.T) {
	var calls atomic.Int64
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = fmt.Fprint(w, "[]") })
	for _, call := range []func() error{
		func() error { _, e := g.ReadIssue(context.Background(), 0); return e }, func() error { _, e := g.ReadComment(context.Background(), 0); return e }, func() error { _, e := g.ReadMilestone(context.Background(), -1); return e },
		func() error {
			_, e := g.ListComments(context.Background(), Resource{Kind: "milestone", Number: 1})
			return e
		}, func() error { _, e := g.ListDependencies(context.Background(), 4, "guessed"); return e }, func() error { _, e := g.ListDependencies(context.Background(), 0, "blocking"); return e },
	} {
		if err := call(); err == nil {
			t.Fatal("invalid resource accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid resource reached GitHub")
	}
}
