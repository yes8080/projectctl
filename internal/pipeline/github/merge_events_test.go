package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

const mergeEventsPath = "/repos/octo/pipeline/issues/3/events"

var mergeEventSHA = strings.Repeat("c", 40)

func mergeEventFixture(id int64, kind string) map[string]any {
	event := map[string]any{
		"id": id, "node_id": fmt.Sprintf("IE_%d", id), "event": kind,
		"url":   fmt.Sprintf("https://api.github.com/repos/octo/pipeline/issues/events/%d", id),
		"actor": testServerFixtureAuthor(), "created_at": "2026-09-27T00:00:00Z",
		"commit_id": nil, "commit_url": nil,
	}
	if kind == "merged" {
		event["commit_id"] = mergeEventSHA
		event["commit_url"] = "https://api.github.com/repos/octo/pipeline/commits/" + mergeEventSHA
	}
	return event
}

func mergeEventsGateway(t *testing.T, pr map[string]any, events http.HandlerFunc) *Gateway {
	t.Helper()
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("read path attempted %s", r.Method)
		}
		if r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("PR/events traversal changed the pinned API version")
		}
		switch r.URL.Path {
		case "/repos/octo/pipeline/pulls/3":
			testServerFixtureJSON(t, w, pr)
		case mergeEventsPath:
			if r.URL.Query().Get("per_page") != "100" {
				t.Error("merge events were not read through bounded pagination")
			}
			events(w, r)
		default:
			t.Errorf("unexpected endpoint (response URL must never redirect reads): %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	return g
}

func TestMergeEventIsAuthoritativeWithOptionalLegacyField(t *testing.T) {
	for _, mode := range []string{"missing", "null", "consistent"} {
		t.Run(mode, func(t *testing.T) {
			pr := designPRFixture()
			if mode == "missing" {
				delete(pr, "merge_commit_sha")
			}
			if mode == "null" {
				pr["merge_commit_sha"] = nil
			}
			var reads atomic.Int64
			g := mergeEventsGateway(t, pr, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				// Unrecognized nonempty event kinds and additive API fields are
				// normal GitHub evolution, not an excuse to skip native identity.
				future := mergeEventFixture(1, "future_github_event")
				future["new_api_metadata"] = map[string]any{"value": "ignored"}
				testServerFixtureJSON(t, w, []any{future, mergeEventFixture(2, "merged")})
			})
			got, err := g.ReadPullRequest(context.Background(), 3)
			if err != nil || !got.Merged || got.MergeSHA != mergeEventSHA || reads.Load() != 1 {
				t.Fatalf("authoritative merge event not selected: merge=%q reads=%d err=%v", got.MergeSHA, reads.Load(), err)
			}
		})
	}
}

func TestMergeEventsReadEveryPageBeforeSelecting(t *testing.T) {
	var reads atomic.Int64
	g := mergeEventsGateway(t, designPRFixture(), func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		switch r.URL.Query().Get("page") {
		case "1":
			items := make([]any, 100)
			for i := range items {
				items[i] = mergeEventFixture(int64(i+1), "labeled")
			}
			w.Header().Set("Link", "<"+mergeEventsPath+"?per_page=100&page=2>; rel=\"next\"")
			testServerFixtureJSON(t, w, items)
		case "2":
			testServerFixtureJSON(t, w, []any{mergeEventFixture(101, "merged")})
		default:
			t.Fatalf("unexpected page: %s", r.URL.RawQuery)
		}
	})
	got, err := g.ReadPullRequest(context.Background(), 3)
	if err != nil || got.MergeSHA != mergeEventSHA || reads.Load() != 2 {
		t.Fatalf("incomplete merge-event traversal: reads=%d err=%v", reads.Load(), err)
	}
}

func TestMergeEventsRejectMalformedNativeFacts(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missing id":             func(e map[string]any) { delete(e, "id") },
		"null id":                func(e map[string]any) { e["id"] = nil },
		"zero id":                func(e map[string]any) { e["id"] = 0 },
		"negative id":            func(e map[string]any) { e["id"] = -1 },
		"fraction id":            func(e map[string]any) { e["id"] = 1.5 },
		"string id":              func(e map[string]any) { e["id"] = "1" },
		"missing node":           func(e map[string]any) { delete(e, "node_id") },
		"blank node":             func(e map[string]any) { e["node_id"] = " " },
		"null node":              func(e map[string]any) { e["node_id"] = nil },
		"missing event":          func(e map[string]any) { delete(e, "event") },
		"blank event":            func(e map[string]any) { e["event"] = " " },
		"padded event":           func(e map[string]any) { e["event"] = " merged " },
		"null event":             func(e map[string]any) { e["event"] = nil },
		"missing url":            func(e map[string]any) { delete(e, "url") },
		"wrong event id URL":     func(e map[string]any) { e["url"] = "https://api.github.com/repos/octo/pipeline/issues/events/999" },
		"wrong event host":       func(e map[string]any) { e["url"] = "https://evil.invalid/repos/octo/pipeline/issues/events/1" },
		"wrong event repository": func(e map[string]any) { e["url"] = "https://api.github.com/repos/other/pipeline/issues/events/1" },
		"wrong event path":       func(e map[string]any) { e["url"] = "https://api.github.com/repos/octo/pipeline/issues/3/events/1" },
		"event URL query":        func(e map[string]any) { e["url"] = e["url"].(string) + "?redirect=1" },
		"event URL fragment":     func(e map[string]any) { e["url"] = e["url"].(string) + "#other" },
		"missing actor":          func(e map[string]any) { delete(e, "actor") },
		"null actor":             func(e map[string]any) { e["actor"] = nil },
		"actor id":               func(e map[string]any) { a := testServerFixtureAuthor(); a.ID = 0; e["actor"] = a },
		"actor node":             func(e map[string]any) { a := testServerFixtureAuthor(); a.NodeID = ""; e["actor"] = a },
		"actor login":            func(e map[string]any) { a := testServerFixtureAuthor(); a.Login = ""; e["actor"] = a },
		"actor type":             func(e map[string]any) { a := testServerFixtureAuthor(); a.Type = "Unknown"; e["actor"] = a },
		"missing commit":         func(e map[string]any) { delete(e, "commit_id") },
		"null commit":            func(e map[string]any) { e["commit_id"] = nil },
		"short commit":           func(e map[string]any) { e["commit_id"] = "abc123" },
		"uppercase commit":       func(e map[string]any) { e["commit_id"] = strings.Repeat("A", 40) },
		"branch as commit":       func(e map[string]any) { e["commit_id"] = "main" },
		"missing commit URL":     func(e map[string]any) { delete(e, "commit_url") },
		"null commit URL":        func(e map[string]any) { e["commit_url"] = nil },
		"wrong commit URL sha": func(e map[string]any) {
			e["commit_url"] = "https://api.github.com/repos/octo/pipeline/commits/" + designReadSHA
		},
		"wrong commit URL repository": func(e map[string]any) {
			e["commit_url"] = "https://api.github.com/repos/other/pipeline/commits/" + mergeEventSHA
		},
		"wrong commit URL host": func(e map[string]any) { e["commit_url"] = "https://evil.invalid/commit" },
		"git object URL not commit event URL": func(e map[string]any) {
			e["commit_url"] = "https://api.github.com/repos/octo/pipeline/git/commits/" + mergeEventSHA
		},
		"commit URL query":    func(e map[string]any) { e["commit_url"] = e["commit_url"].(string) + "?ref=main" },
		"commit URL fragment": func(e map[string]any) { e["commit_url"] = e["commit_url"].(string) + "#other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := mergeEventFixture(1, "merged")
			mutate(e)
			g := mergeEventsGateway(t, designPRFixture(), func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, []any{e}) })
			if _, err := g.ReadPullRequest(context.Background(), 3); err == nil {
				t.Fatal("malformed merge event accepted")
			}
		})
	}
}

func TestMergeEventsRequireUniqueCompleteHistory(t *testing.T) {
	for _, mode := range []string{"none", "two merged same SHA", "two merged different SHA", "duplicate id", "duplicate node", "malformed unrelated event", "contradictory legacy"} {
		t.Run(mode, func(t *testing.T) {
			pr := designPRFixture()
			items := []any{mergeEventFixture(1, "merged")}
			switch mode {
			case "none":
				items = []any{mergeEventFixture(1, "closed")}
			case "two merged same SHA":
				items = append(items, mergeEventFixture(2, "merged"))
			case "two merged different SHA":
				e := mergeEventFixture(2, "merged")
				e["commit_id"] = designReadSHA
				e["commit_url"] = "https://api.github.com/repos/octo/pipeline/commits/" + designReadSHA
				items = append(items, e)
			case "duplicate id":
				e := mergeEventFixture(1, "closed")
				e["node_id"] = "IE_distinct"
				items = append(items, e)
			case "duplicate node":
				e := mergeEventFixture(2, "closed")
				e["node_id"] = "IE_1"
				items = append(items, e)
			case "malformed unrelated event":
				e := mergeEventFixture(2, "labeled")
				delete(e, "actor")
				items = append(items, e)
			case "contradictory legacy":
				pr["merge_commit_sha"] = designReadSHA
			}
			g := mergeEventsGateway(t, pr, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, items) })
			if _, err := g.ReadPullRequest(context.Background(), 3); err == nil {
				t.Fatal("ambiguous or contradictory event history accepted")
			}
		})
	}
}

func TestMergeEventsNeverUsePartialFirstPage(t *testing.T) {
	for _, mode := range []string{"second merged", "duplicate id", "duplicate node", "HTTP 403", "HTTP 404", "HTTP 500", "invalid JSON", "null array", "pagination cycle", "escaped page"} {
		t.Run(mode, func(t *testing.T) {
			var reads atomic.Int64
			g := mergeEventsGateway(t, designPRFixture(), func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				if r.URL.Query().Get("page") == "1" {
					link := mergeEventsPath + "?per_page=100&page=2"
					if mode == "pagination cycle" {
						link = mergeEventsPath + "?per_page=100&page=1"
					}
					if mode == "escaped page" {
						link = "/repos/other/pipeline/issues/3/events?per_page=100&page=2"
					}
					w.Header().Set("Link", "<"+link+">; rel=\"next\"")
					testServerFixtureJSON(t, w, []any{mergeEventFixture(1, "merged")})
					return
				}
				if strings.HasPrefix(mode, "HTTP ") {
					status, _ := strconv.Atoi(strings.TrimPrefix(mode, "HTTP "))
					w.WriteHeader(status)
					return
				}
				if mode == "invalid JSON" {
					_, _ = fmt.Fprint(w, "[")
					return
				}
				if mode == "null array" {
					_, _ = fmt.Fprint(w, "null")
					return
				}
				e := mergeEventFixture(2, "closed")
				if mode == "second merged" {
					e = mergeEventFixture(2, "merged")
				}
				if mode == "duplicate id" {
					e = mergeEventFixture(1, "closed")
					e["node_id"] = "IE_distinct"
				}
				if mode == "duplicate node" {
					e["node_id"] = "IE_1"
				}
				testServerFixtureJSON(t, w, []any{e})
			})
			if _, err := g.ReadPullRequest(context.Background(), 3); err == nil {
				t.Fatal("first page was treated as complete authoritative merge history")
			}
			if mode != "pagination cycle" && mode != "escaped page" && reads.Load() != 2 {
				t.Fatalf("later page not verified: reads=%d", reads.Load())
			}
		})
	}
}

func TestMergeEventsStrictJSONEnvelope(t *testing.T) {
	valid, err := json.Marshal([]any{mergeEventFixture(1, "merged")})
	if err != nil {
		t.Fatal(err)
	}
	for name, wire := range map[string]string{
		"duplicate field":         strings.Replace(string(valid), `"event":`, `"event":"closed","event":`, 1),
		"escaped duplicate":       strings.Replace(string(valid), `"event":`, `"\u0065vent":"closed","event":`, 1),
		"case alias":              strings.Replace(string(valid), `"commit_id":`, `"Commit_Id":`, 1),
		"actor duplicate":         strings.Replace(string(valid), `"login":`, `"login":"other","login":`, 1),
		"null array":              "null",
		"object instead of array": "{}",
		"trailing JSON":           string(valid) + "[]",
	} {
		t.Run(name, func(t *testing.T) {
			g := mergeEventsGateway(t, designPRFixture(), func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, wire) })
			if _, err := g.ReadPullRequest(context.Background(), 3); err == nil {
				t.Fatal("ambiguous event JSON accepted")
			}
		})
	}
}

func TestUnmergedPRDoesNotPromoteTestMergeCommit(t *testing.T) {
	for _, state := range []string{"open", "closed"} {
		t.Run(state, func(t *testing.T) {
			pr := designPRFixture()
			pr["merged"] = false
			pr["state"] = state
			g := mergeEventsGateway(t, pr, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unmerged PR must not request merge proof") })
			got, err := g.ReadPullRequest(context.Background(), 3)
			if err != nil || got.Merged || got.MergeSHA != "" {
				t.Fatalf("test merge promoted to real merge: %#v %v", got, err)
			}
		})
	}
}

func TestMergeEventOptionalIssueMustMatchExactPR(t *testing.T) {
	for _, mode := range []string{"omitted", "null", "valid", "id", "node", "number", "api url", "repository url", "web url", "missing pull", "null pull", "pull api url", "pull web url", "missing pull api url", "missing pull web url"} {
		t.Run(mode, func(t *testing.T) {
			event := mergeEventFixture(1, "merged")
			issue := map[string]any{
				"id": 1003, "node_id": "I_PR_3", "number": 3,
				"url":            "https://api.github.com/repos/octo/pipeline/issues/3",
				"repository_url": "https://api.github.com/repos/octo/pipeline",
				"html_url":       "https://github.com/octo/pipeline/pull/3",
				"pull_request":   map[string]any{"url": "https://api.github.com/repos/octo/pipeline/pulls/3", "html_url": "https://github.com/octo/pipeline/pull/3"},
			}
			if mode != "omitted" {
				event["issue"] = issue
			}
			switch mode {
			case "null":
				event["issue"] = nil
			case "id":
				issue["id"] = 0
			case "node":
				issue["node_id"] = ""
			case "number":
				issue["number"] = 4
			case "api url":
				issue["url"] = "https://api.github.com/repos/octo/pipeline/issues/4"
			case "repository url":
				issue["repository_url"] = "https://api.github.com/repos/other/pipeline"
			case "web url":
				issue["html_url"] = "https://github.com/octo/pipeline/issues/3"
			case "missing pull":
				delete(issue, "pull_request")
			case "null pull":
				issue["pull_request"] = nil
			case "pull api url":
				issue["pull_request"].(map[string]any)["url"] = "https://api.github.com/repos/octo/pipeline/pulls/4"
			case "pull web url":
				issue["pull_request"].(map[string]any)["html_url"] = "https://github.com/other/pipeline/pull/3"
			case "missing pull api url":
				delete(issue["pull_request"].(map[string]any), "url")
			case "missing pull web url":
				delete(issue["pull_request"].(map[string]any), "html_url")
			}
			g := mergeEventsGateway(t, designPRFixture(), func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, []any{event}) })
			got, err := g.ReadPullRequest(context.Background(), 3)
			valid := mode == "omitted" || mode == "null" || mode == "valid"
			if (err == nil) != valid || (valid && got.MergeSHA != mergeEventSHA) {
				t.Fatalf("embedded issue match valid=%v: merge=%q err=%v", valid, got.MergeSHA, err)
			}
		})
	}
}

func TestMergeEventActorNeedNotBePRAuthor(t *testing.T) {
	event := mergeEventFixture(1, "merged")
	actor := testServerFixtureAuthor()
	actor.ID, actor.NodeID, actor.Login, actor.Type = 999, "B_999", "merge-bot", "Bot"
	event["actor"] = actor
	g := mergeEventsGateway(t, designPRFixture(), func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, []any{event}) })
	got, err := g.ReadPullRequest(context.Background(), 3)
	if err != nil || got.MergeSHA != mergeEventSHA {
		t.Fatalf("a valid distinct merge actor was rejected: %v", err)
	}
}

func TestMergeEventHTTPFailureNeverFallsBackToLegacy(t *testing.T) {
	for _, status := range []int{301, 403, 404, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var reads atomic.Int64
			g := mergeEventsGateway(t, designPRFixture(), func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				w.Header().Set("Location", "https://evil.invalid/events")
				w.WriteHeader(status)
			})
			if _, err := g.ReadPullRequest(context.Background(), 3); err == nil || reads.Load() != 1 {
				t.Fatalf("events failure fell back or retried: reads=%d err=%v", reads.Load(), err)
			}
		})
	}
}

func TestMergeEventsUseOneCredentialSnapshotAcrossEveryPage(t *testing.T) {
	var tokenCalls, reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer bounded-token-1" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Errorf("credential snapshot/API version drift on %s", r.URL.Path)
		}
		if r.URL.Path == "/repos/octo/pipeline/pulls/3" {
			testServerFixtureJSON(t, w, designPRFixture())
			return
		}
		if r.URL.Path != mergeEventsPath {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", "<"+mergeEventsPath+"?per_page=100&page=2>; rel=\"next\"")
			testServerFixtureJSON(t, w, []any{mergeEventFixture(1, "closed")})
			return
		}
		testServerFixtureJSON(t, w, []any{mergeEventFixture(2, "merged")})
	}))
	t.Cleanup(server.Close)
	g, err := New(Config{Project: testServerFixtureProject(), BaseURL: server.URL, AllowLoopbackTestServer: true, HTTPClient: server.Client(), Token: func(context.Context) (string, error) { return fmt.Sprintf("bounded-token-%d", tokenCalls.Add(1)), nil }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.ReadPullRequest(context.Background(), 3)
	if err != nil || got.MergeSHA != mergeEventSHA || tokenCalls.Load() != 1 || reads.Load() != 3 {
		t.Fatalf("incomplete or unbound traversal: calls=%d reads=%d err=%v", tokenCalls.Load(), reads.Load(), err)
	}
}
