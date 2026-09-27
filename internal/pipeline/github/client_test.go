package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func testServerFixtureProject() protocol.Project {
	return protocol.Project{Owner: "octo", Repo: "pipeline", ControlIssue: 2}
}

func testServerFixtureAuthor() protocol.GitHubIdentity {
	return protocol.GitHubIdentity{ID: 71, NodeID: "U_71", Login: "native-author", Type: "User"}
}

func testServerFixtureString(value string) *string { return &value }

func testServerFixtureGateway(t *testing.T, handler http.HandlerFunc) (*Gateway, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	gateway, err := New(Config{
		Project: testServerFixtureProject(), Publisher: testServerFixtureAuthor(),
		BaseURL: server.URL, AllowLoopbackTestServer: true, HTTPClient: server.Client(),
		Token: func(context.Context) (string, error) { return "synthetic-test-bearer", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateway, server
}

func testServerFixtureJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode fixture: %v", err)
	}
}

func testServerFixtureIssue(number int64, body *string) map[string]any {
	return map[string]any{
		"id": 1000 + number, "node_id": fmt.Sprintf("I_%d", number), "number": number,
		"url":            fmt.Sprintf("https://api.github.com/repos/octo/pipeline/issues/%d", number),
		"html_url":       fmt.Sprintf("https://github.com/octo/pipeline/issues/%d", number),
		"repository_url": "https://api.github.com/repos/octo/pipeline", "title": "native issue",
		"state": "open", "body": body, "user": testServerFixtureAuthor(), "milestone": nil,
	}
}

func testServerFixtureComment(number, id int64, body string) map[string]any {
	return map[string]any{
		"id": id, "node_id": fmt.Sprintf("IC_%d", id),
		"url":       fmt.Sprintf("https://api.github.com/repos/octo/pipeline/issues/comments/%d", id),
		"html_url":  fmt.Sprintf("https://github.com/octo/pipeline/issues/%d#issuecomment-%d", number, id),
		"issue_url": fmt.Sprintf("https://api.github.com/repos/octo/pipeline/issues/%d", number),
		"body":      body, "user": testServerFixtureAuthor(),
	}
}

func testServerFixtureMilestone(number int64, description *string) map[string]any {
	return map[string]any{
		"id": 2000 + number, "node_id": fmt.Sprintf("M_%d", number), "number": number,
		"url":      fmt.Sprintf("https://api.github.com/repos/octo/pipeline/milestones/%d", number),
		"html_url": fmt.Sprintf("https://github.com/octo/pipeline/milestone/%d", number),
		"title":    "native milestone", "state": "open", "description": description,
		"creator": testServerFixtureAuthor(),
	}
}

type testServerFixtureTransport func(*http.Request) (*http.Response, error)

func (f testServerFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientOriginAndCredentialBoundary(t *testing.T) {
	for _, origin := range []string{
		"http://api.github.com", "https://api.github.com.evil.test", "https://api.github.com:443",
		"https://secret@api.github.com", "https://api.github.com/repos/octo/pipeline",
		"https://api.github.com?secret=token", "https://api.github.com#fragment", "http://127.0.0.1:12345",
	} {
		t.Run(origin, func(t *testing.T) {
			_, err := New(Config{Project: testServerFixtureProject(), BaseURL: origin, Token: func(context.Context) (string, error) { return "unused", nil }})
			if err == nil {
				t.Fatal("untrusted origin accepted")
			}
		})
	}
	if _, err := New(Config{Project: testServerFixtureProject()}); err == nil {
		t.Fatal("credential provider is required")
	}
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-test-bearer" || r.Header.Get("Accept") != "application/vnd.github.raw+json" || r.Header.Get("X-GitHub-Api-Version") != apiVersion {
			t.Errorf("missing authentication, raw-body media type, or pinned API version")
		}
		testServerFixtureJSON(t, w, testServerFixtureAuthor())
	})
	if _, err := g.CurrentUser(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientNeverFollowsRedirectWithBearerToken(t *testing.T) {
	var destinationCalls atomic.Int64
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		testServerFixtureJSON(t, w, testServerFixtureAuthor())
	}))
	t.Cleanup(destination.Close)
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", destination.URL+"/stolen")
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)
			callerRedirects := 0
			client := server.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { callerRedirects++; return nil }
			g, err := New(Config{Project: testServerFixtureProject(), BaseURL: server.URL, AllowLoopbackTestServer: true, HTTPClient: client, Token: func(context.Context) (string, error) { return "synthetic-test-bearer", nil }})
			if err != nil {
				t.Fatal(err)
			}
			_, err = g.CurrentUser(context.Background())
			var responseErr *HTTPError
			if !errors.As(err, &responseErr) || responseErr.Status != status || callerRedirects != 0 {
				t.Fatalf("redirect was not returned as an HTTP error: %v", err)
			}
			if err := client.CheckRedirect(nil, nil); err != nil || callerRedirects != 1 {
				t.Fatal("New changed the caller's HTTP client")
			}
		})
	}
	if destinationCalls.Load() != 0 {
		t.Fatal("redirect destination received a credential-bearing request")
	}
}

func TestClientErrorsNeverEchoSecrets(t *testing.T) {
	const secret = "synthetic-secret-not-for-error-output"
	for _, status := range []int{400, 401, 403, 404, 410, 422, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, secret)
			})
			_, err := g.CurrentUser(context.Background())
			var responseErr *HTTPError
			if !errors.As(err, &responseErr) || responseErr.Status != status || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe or missing HTTP error: %v", err)
			}
		})
	}
	for _, source := range []string{"provider", "transport", "invalid credential"} {
		t.Run(source, func(t *testing.T) {
			config := Config{Project: testServerFixtureProject(), Token: func(context.Context) (string, error) { return secret, nil }}
			config.HTTPClient = &http.Client{Transport: testServerFixtureTransport(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("provider detail: %s", secret) })}
			if source == "provider" {
				config.Token = func(context.Context) (string, error) { return "", errors.New(secret) }
			}
			if source == "invalid credential" {
				config.Token = func(context.Context) (string, error) { return secret + "\n", nil }
			}
			g, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			_, err = g.CurrentUser(context.Background())
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("unsafe or missing credential/transport error: %v", err)
			}
		})
	}
}

func TestPaginationRejectsEscapesAndAmbiguityBeforeRequest(t *testing.T) {
	var leaked atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	t.Cleanup(sink.Close)
	tests := []struct {
		name string
		link func(*http.Request) string
	}{
		{"cross host", func(r *http.Request) string {
			return "<" + sink.URL + r.URL.Path + "?" + r.URL.RawQuery + ">; rel=\"next\""
		}},
		{"cycle", func(r *http.Request) string { return "<" + r.URL.String() + ">; rel=\"next\"" }},
		{"skip page", func(r *http.Request) string {
			q := r.URL.Query()
			q.Set("page", "3")
			return "<" + r.URL.Path + "?" + q.Encode() + ">; rel=\"next\""
		}},
		{"changed filters", func(r *http.Request) string {
			q := r.URL.Query()
			q.Set("page", "2")
			q.Set("state", "open")
			return "<" + r.URL.Path + "?" + q.Encode() + ">; rel=\"next\""
		}},
		{"changed resource", func(r *http.Request) string {
			q := r.URL.Query()
			q.Set("page", "2")
			return "</repos/octo/other/issues?" + q.Encode() + ">; rel=\"next\""
		}},
		{"duplicate page value", func(r *http.Request) string {
			q := r.URL.Query()
			q["page"] = []string{"2", "3"}
			return "<" + r.URL.Path + "?" + q.Encode() + ">; rel=\"next\""
		}},
		{"changed page size", func(r *http.Request) string {
			q := r.URL.Query()
			q.Set("page", "2")
			q.Set("per_page", "1")
			return "<" + r.URL.Path + "?" + q.Encode() + ">; rel=\"next\""
		}},
		{"unknown filter", func(r *http.Request) string {
			q := r.URL.Query()
			q.Set("page", "2")
			q.Set("extra", "secret")
			return "<" + r.URL.Path + "?" + q.Encode() + ">; rel=\"next\""
		}},
		{"malformed", func(*http.Request) string { return "not-a-link; rel=next" }},
		{"ambiguous next", func(r *http.Request) string {
			q := r.URL.Query()
			q.Set("page", "2")
			link := "<" + r.URL.Path + "?" + q.Encode() + ">; rel=\"next\""
			return link + ", " + link
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Link", test.link(r))
				testServerFixtureJSON(t, w, []any{testServerFixtureIssue(4, testServerFixtureString("partial"))})
			})
			items, err := g.ListIssues(context.Background())
			if err == nil || items != nil || calls.Load() != 1 {
				t.Fatalf("invalid Link followed or partial collection returned: count=%d calls=%d err=%v", len(items), calls.Load(), err)
			}
		})
	}
	if leaked.Load() != 0 {
		t.Fatal("cross-origin Link received the token")
	}
}

func TestPaginationDropsEarlierPagesOnLaterFailure(t *testing.T) {
	for _, failure := range []string{"http", "malformed JSON", "object envelope", "null array"} {
		t.Run(failure, func(t *testing.T) {
			var calls atomic.Int64
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Query().Get("page") == "1" {
					q := r.URL.Query()
					q.Set("page", "2")
					w.Header().Set("Link", "<"+r.URL.Path+"?"+q.Encode()+">; rel=\"next\"")
					testServerFixtureJSON(t, w, []any{testServerFixtureIssue(4, nil)})
					return
				}
				switch failure {
				case "http":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "malformed JSON":
					_, _ = fmt.Fprint(w, "[{broken]")
				case "object envelope":
					_, _ = fmt.Fprint(w, `{"items":[]}`)
				case "null array":
					_, _ = fmt.Fprint(w, "null")
				}
			})
			items, err := g.ListIssues(context.Background())
			if err == nil || items != nil || calls.Load() != 2 {
				t.Fatalf("later-page failure did not discard the complete traversal: %#v, %v", items, err)
			}
		})
	}
}

func TestAPIJSONDoesNotOverwriteNativeIdentity(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`{"id":1,"id":2}`), []byte(`{"id":1,"\u0069d":2}`),
		[]byte(`{"user":{"id":1,"id":2}}`), []byte(`{} {}`), []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'},
	} {
		var decoded map[string]any
		if err := decodeAPI(input, &decoded); err == nil {
			t.Fatalf("ambiguous API JSON accepted: %q", input)
		}
	}
	var decoded struct {
		ID int64 `json:"id"`
	}
	if err := decodeAPI([]byte(`{"id":7,"new_provider_field":{"enabled":true}}`), &decoded); err != nil || decoded.ID != 7 {
		t.Fatalf("additive GitHub fields must remain compatible: %#v %v", decoded, err)
	}
}

func TestAPIJSONRejectsCaseAliasesOfModeledIdentityFields(t *testing.T) {
	for _, input := range []string{
		`{"id":71,"ID":99,"node_id":"U_71","login":"native-author","type":"User"}`,
		`{"ID":71,"node_id":"U_71","login":"native-author","type":"User"}`,
		`{"id":71,"node_id":"U_71","Node_ID":"U_99","login":"native-author","type":"User"}`,
	} {
		var identity protocol.GitHubIdentity
		if err := decodeAPI([]byte(input), &identity); err == nil {
			t.Fatalf("modeled identity field alias accepted: %s", input)
		}
	}
	for _, input := range []string{
		`{"user":{"id":71,"ID":99}}`,
		`{"user":{"ID":71}}`,
		`{"User":{"id":71}}`,
	} {
		var issue nativeIssue
		if err := decodeAPI([]byte(input), &issue); err == nil {
			t.Fatalf("nested modeled identity alias accepted: %s", input)
		}
	}
	var issue nativeIssue
	if err := decodeAPI([]byte(`{"id":1004,"new_provider_field":{"ID":99}}`), &issue); err != nil || issue.ID != 1004 {
		t.Fatalf("truly unknown provider extension must remain compatible: %#v %v", issue, err)
	}
}

func TestDirectRequestRejectsForeignOriginBeforeCredentialLookup(t *testing.T) {
	lookups := 0
	g, err := New(Config{Project: testServerFixtureProject(), Token: func(context.Context) (string, error) { lookups++; return "secret", nil }})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://attacker.invalid/repos/octo/pipeline/issues")
	if _, _, err := g.request(context.Background(), http.MethodGet, u, nil); err == nil || lookups != 0 {
		t.Fatal("out-of-scope request reached the credential provider")
	}
}
