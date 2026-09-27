package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

var designReadSHA = strings.Repeat("a", 40)
var designReadTree = strings.Repeat("b", 40)

func designCommitFixture() map[string]any {
	return map[string]any{"sha": designReadSHA, "url": "https://api.github.com/repos/octo/pipeline/git/commits/" + designReadSHA, "tree": map[string]any{"sha": designReadTree, "url": "https://api.github.com/repos/octo/pipeline/git/trees/" + designReadTree}}
}

func designBranchFixture() map[string]any {
	return map[string]any{"ref": "refs/heads/codex/issue-7", "node_id": "REF_7", "url": "https://api.github.com/repos/octo/pipeline/git/refs/heads/codex/issue-7", "object": map[string]any{"type": "commit", "sha": designReadSHA, "url": "https://api.github.com/repos/octo/pipeline/git/commits/" + designReadSHA}}
}

func designPRFixture() map[string]any {
	repository := func() map[string]any {
		return map[string]any{"id": 100, "node_id": "R_100", "full_name": "octo/pipeline", "url": "https://api.github.com/repos/octo/pipeline", "html_url": "https://github.com/octo/pipeline"}
	}
	return map[string]any{
		"id": 300, "node_id": "PR_300", "number": 3,
		"url": "https://api.github.com/repos/octo/pipeline/pulls/3", "html_url": "https://github.com/octo/pipeline/pull/3", "issue_url": "https://api.github.com/repos/octo/pipeline/issues/3",
		"user": testServerFixtureAuthor(), "state": "closed", "merged": true, "draft": false, "merge_commit_sha": strings.Repeat("c", 40),
		"head": map[string]any{"ref": "codex/issue-7", "sha": designReadSHA, "repo": repository()},
		"base": map[string]any{"ref": "main", "sha": designReadTree, "repo": repository()},
	}
}

func TestReadCommitAndBranchExactNativeFacts(t *testing.T) {
	var reads atomic.Int64
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Method != http.MethodGet || r.URL.RawQuery != "" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		switch r.URL.Path {
		case "/repos/octo/pipeline/git/ref/heads/codex/issue-7":
			testServerFixtureJSON(t, w, designBranchFixture())
		case "/repos/octo/pipeline/git/commits/" + designReadSHA:
			if r.Header.Get("Accept") != "application/vnd.github+json" {
				t.Error("Git object JSON media type missing")
			}
			testServerFixtureJSON(t, w, designCommitFixture())
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	want := Commit{SHA: designReadSHA, TreeSHA: designReadTree}
	if got, err := g.ReadCommit(context.Background(), designReadSHA); err != nil || got != want {
		t.Fatalf("commit = %#v %v", got, err)
	}
	if got, err := g.ReadBranch(context.Background(), "codex/issue-7"); err != nil || got != want {
		t.Fatalf("branch = %#v %v", got, err)
	}
	if reads.Load() != 3 {
		t.Fatal("branch resolution did not use one exact ref and one commit read")
	}
}

func TestDesignReadInputsRejectedBeforeHTTP(t *testing.T) {
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached HTTP") })
	for _, sha := range []string{"", "main", "HEAD", "a", strings.Repeat("A", 40), "../main", designReadSHA + "?x=1"} {
		if _, err := g.ReadCommit(context.Background(), sha); err == nil {
			t.Errorf("invalid commit %q", sha)
		}
	}
	for _, name := range []string{"", "@", ".hidden", "-option", "feature..x", "x@{0}", "/main", "main/", "x//y", "x/.hidden", "x.lock", "x/y.lock", "x.", "x y", "x%2Fy", "x?y", "x#y", "x\\y", "x:y", "x*y", "x[y", "x\ny", "x\x00y", string([]byte{0xff})} {
		if _, err := g.ReadBranch(context.Background(), name); err == nil {
			t.Errorf("invalid branch %q", name)
		}
	}
	for _, number := range []int64{0, -1} {
		if _, err := g.ReadPullRequest(context.Background(), number); err == nil {
			t.Error("invalid PR number")
		}
	}
}

func TestReadCommitRejectsNativeDrift(t *testing.T) {
	for _, mode := range []string{"sha", "url", "tree sha", "tree url", "missing tree"} {
		t.Run(mode, func(t *testing.T) {
			n := designCommitFixture()
			switch mode {
			case "sha":
				n["sha"] = strings.Repeat("c", 40)
			case "url":
				n["url"] = "https://evil.invalid/secret"
			case "tree sha":
				n["tree"].(map[string]any)["sha"] = "main"
			case "tree url":
				n["tree"].(map[string]any)["url"] = "https://api.github.com/repos/other/pipeline/git/trees/" + designReadTree
			case "missing tree":
				delete(n, "tree")
			}
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, n) })
			if _, err := g.ReadCommit(context.Background(), designReadSHA); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}

func TestReadBranchRejectsNativeDrift(t *testing.T) {
	for _, mode := range []string{"ref", "node", "url", "object type", "object sha", "object url", "array"} {
		t.Run(mode, func(t *testing.T) {
			n := designBranchFixture()
			switch mode {
			case "ref":
				n["ref"] = "refs/heads/codex/issue-70"
			case "node":
				delete(n, "node_id")
			case "url":
				n["url"] = "https://evil.invalid/secret"
			case "object type":
				n["object"].(map[string]any)["type"] = "tag"
			case "object sha":
				n["object"].(map[string]any)["sha"] = "main"
			case "object url":
				n["object"].(map[string]any)["url"] = "https://evil.invalid/commit"
			}
			var reads atomic.Int64
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				if mode == "array" {
					testServerFixtureJSON(t, w, []any{n})
					return
				}
				testServerFixtureJSON(t, w, n)
			})
			if _, err := g.ReadBranch(context.Background(), "codex/issue-7"); err == nil {
				t.Fatal("drift accepted")
			}
			if reads.Load() != 1 {
				t.Fatal("invalid native pointer caused another request")
			}
		})
	}
}

func TestReadPullRequestNativeFacts(t *testing.T) {
	for _, mode := range []string{"merged", "open test merge", "open null merge", "closed unmerged", "draft"} {
		t.Run(mode, func(t *testing.T) {
			n := designPRFixture()
			if mode != "merged" {
				n["merged"] = false
			}
			if mode == "open test merge" || mode == "open null merge" || mode == "draft" {
				n["state"] = "open"
			}
			if mode == "open null merge" {
				n["merge_commit_sha"] = nil
			}
			if mode == "draft" {
				n["draft"] = true
			}
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/octo/pipeline/pulls/3" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				testServerFixtureJSON(t, w, n)
			})
			got, err := g.ReadPullRequest(context.Background(), 3)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != "pull_request" || got.DatabaseID != 300 || got.NodeID != "PR_300" || got.Number != 3 || got.Repository != got.BaseRepository || got.Repository.DatabaseID != 100 || got.Author != testServerFixtureAuthor() || got.HeadSHA != designReadSHA || got.BaseSHA != designReadTree || got.HeadRef != "codex/issue-7" || got.BaseRef != "main" {
				t.Fatalf("incomplete native PR: %#v", got)
			}
			if (mode == "merged" && (!got.Merged || got.MergeSHA != strings.Repeat("c", 40))) || (mode != "merged" && (got.Merged || got.MergeSHA != "")) {
				t.Fatal("test merge confused with merged delivery")
			}
			if got.Draft != (mode == "draft") {
				t.Fatal("draft fact lost")
			}
		})
	}
}

func TestReadPullRequestRejectsMissingAndConflictingFacts(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]any)
	}{
		{"id", func(n map[string]any) { n["id"] = 0 }},
		{"node", func(n map[string]any) { delete(n, "node_id") }},
		{"number", func(n map[string]any) { n["number"] = 4 }},
		{"api url", func(n map[string]any) { n["url"] = "https://evil.invalid/pr" }},
		{"web url", func(n map[string]any) { n["html_url"] = "https://github.com/other/pipeline/pull/3" }},
		{"issue url", func(n map[string]any) { n["issue_url"] = "https://api.github.com/repos/octo/pipeline/issues/4" }},
		{"author", func(n map[string]any) { delete(n, "user") }},
		{"state", func(n map[string]any) { n["state"] = "unknown" }},
		{"merged absent", func(n map[string]any) { delete(n, "merged") }},
		{"merged null", func(n map[string]any) { n["merged"] = nil }},
		{"merged string", func(n map[string]any) { n["merged"] = "false" }},
		{"draft absent", func(n map[string]any) { delete(n, "draft") }},
		{"draft null", func(n map[string]any) { n["draft"] = nil }},
		{"merged open", func(n map[string]any) { n["state"] = "open" }},
		{"merged draft", func(n map[string]any) { n["draft"] = true }},
		{"merge absent", func(n map[string]any) { delete(n, "merge_commit_sha") }},
		{"merge malformed", func(n map[string]any) { n["merge_commit_sha"] = "main" }},
		{"head sha", func(n map[string]any) { n["head"].(map[string]any)["sha"] = "head" }},
		{"base sha", func(n map[string]any) { n["base"].(map[string]any)["sha"] = "base" }},
		{"head ref", func(n map[string]any) { n["head"].(map[string]any)["ref"] = "../main" }},
		{"base ref", func(n map[string]any) { n["base"].(map[string]any)["ref"] = "" }},
		{"deleted fork", func(n map[string]any) { n["head"].(map[string]any)["repo"] = nil }},
		{"fork name", func(n map[string]any) {
			n["head"].(map[string]any)["repo"].(map[string]any)["full_name"] = "other/pipeline"
		}},
		{"fork id", func(n map[string]any) { n["head"].(map[string]any)["repo"].(map[string]any)["id"] = 101 }},
		{"fork node", func(n map[string]any) { n["head"].(map[string]any)["repo"].(map[string]any)["node_id"] = "R_other" }},
		{"repository api url", func(n map[string]any) {
			n["base"].(map[string]any)["repo"].(map[string]any)["url"] = "https://evil.invalid/repo"
		}},
		{"repository web url", func(n map[string]any) {
			n["base"].(map[string]any)["repo"].(map[string]any)["html_url"] = "https://github.com/other/pipeline"
		}},
		{"repository id", func(n map[string]any) { n["base"].(map[string]any)["repo"].(map[string]any)["id"] = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			n := designPRFixture()
			test.change(n)
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, n) })
			if _, err := g.ReadPullRequest(context.Background(), 3); err == nil {
				t.Fatal("missing/conflicting native facts accepted")
			}
		})
	}
}

func TestDesignReadsFailOnHTTPAndNeverFollowRedirect(t *testing.T) {
	for _, status := range []int{301, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var reads atomic.Int64
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				w.Header().Set("Location", "https://evil.invalid/token")
				w.WriteHeader(status)
			})
			if _, err := g.ReadCommit(context.Background(), designReadSHA); err == nil {
				t.Fatal("commit HTTP failure ignored")
			}
			if _, err := g.ReadBranch(context.Background(), "main"); err == nil {
				t.Fatal("branch HTTP failure ignored")
			}
			if _, err := g.ReadPullRequest(context.Background(), 3); err == nil {
				t.Fatal("PR HTTP failure ignored")
			}
			if reads.Load() != 3 {
				t.Fatal("HTTP operation retried or followed redirect")
			}
		})
	}
}

func TestIssueCreatedAtIsNativeObservation(t *testing.T) {
	const created = "2026-09-28T10:11:12Z"
	n := testServerFixtureIssue(4, nil)
	n["created_at"] = created
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { testServerFixtureJSON(t, w, n) })
	got, err := g.ReadIssue(context.Background(), 4)
	if err != nil || got.CreatedAt != created {
		t.Fatalf("created_at lost: %#v %v", got, err)
	}
	// A missing timestamp is not manufactured locally. The phase using it must
	// reject unknown time instead of substituting its process clock or a cache.
	delete(n, "created_at")
	got, err = g.ReadIssue(context.Background(), 4)
	if err != nil || got.CreatedAt != "" {
		t.Fatalf("missing native time substituted: %#v %v", got, err)
	}
}
