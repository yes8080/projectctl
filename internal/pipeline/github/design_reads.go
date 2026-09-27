package github

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

// Commit is a native Git object observation, not a signature or merge proof.
type Commit struct {
	SHA     string
	TreeSHA string
}

// PullRequest captures same-repository native PR facts. MergeSHA is populated
// only for a merged PR: GitHub also supplies a test-merge SHA before merging.
type PullRequest struct {
	Resource
	Repository     Resource
	BaseRepository Resource
	URL            string
	Author         protocol.GitHubIdentity
	State          string
	Merged         bool
	Draft          bool
	HeadSHA        string
	BaseSHA        string
	HeadRef        string
	BaseRef        string
	MergeSHA       string
}

func branchName(name string) bool {
	if name == "" || len(name) > 4096 || !utf8.ValidString(name) || name == "@" || strings.HasPrefix(name, "-") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, " ~^:?*[\\%#") {
		return false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func (g *Gateway) readCommit(ctx context.Context, sha string) (Commit, error) {
	var native struct {
		SHA  string              `json:"sha"`
		URL  string              `json:"url"`
		Tree nativeSourcePointer `json:"tree"`
	}
	total := 0
	if err := g.sourceObject(ctx, "commits", sha, &total, &native); err != nil {
		return Commit{}, err
	}
	if native.SHA != sha || !g.sourceURL("commits", sha, native.URL) || !sourceHex(native.Tree.SHA, 40) || !g.sourceURL("trees", native.Tree.SHA, native.Tree.URL) {
		return Commit{}, fmt.Errorf("%w: native commit or tree identity mismatch", ErrConflict)
	}
	return Commit{SHA: native.SHA, TreeSHA: native.Tree.SHA}, nil
}

// ReadCommit accepts a complete immutable SHA, never a branch or mutable alias.
func (g *Gateway) ReadCommit(ctx context.Context, sha string) (Commit, error) {
	if !sourceHex(sha, 40) {
		return Commit{}, fmt.Errorf("full lowercase commit SHA required")
	}
	return g.readCommit(ctx, sha)
}

// ReadBranch resolves exactly refs/heads/name, then reads that immutable commit.
// It does not follow response URLs or use GitHub's matching-refs collection.
func (g *Gateway) ReadBranch(ctx context.Context, name string) (Commit, error) {
	if !branchName(name) {
		return Commit{}, fmt.Errorf("safe exact branch name required")
	}
	ctx, err := g.bindCredential(ctx)
	if err != nil {
		return Commit{}, err
	}
	var native struct {
		Ref    string `json:"ref"`
		NodeID string `json:"node_id"`
		URL    string `json:"url"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
			URL  string `json:"url"`
		} `json:"object"`
	}
	if err := g.get(ctx, g.repoPath()+"/git/ref/heads/"+name, &native); err != nil {
		return Commit{}, err
	}
	if native.Ref != "refs/heads/"+name || strings.TrimSpace(native.NodeID) == "" || native.URL != g.apiURL(g.repoPath()+"/git/refs/heads/"+name) || native.Object.Type != "commit" || !sourceHex(native.Object.SHA, 40) || !g.sourceURL("commits", native.Object.SHA, native.Object.URL) {
		return Commit{}, fmt.Errorf("%w: exact branch reference identity mismatch", ErrConflict)
	}
	return g.readCommit(ctx, native.Object.SHA)
}

type nativePullRepository struct {
	ID       int64  `json:"id"`
	NodeID   string `json:"node_id"`
	FullName string `json:"full_name"`
	URL      string `json:"url"`
	HTMLURL  string `json:"html_url"`
}

type nativePullEnd struct {
	Ref  string                `json:"ref"`
	SHA  string                `json:"sha"`
	Repo *nativePullRepository `json:"repo"`
}

func (g *Gateway) pullRepository(native *nativePullRepository) (Resource, error) {
	if native == nil || stable(native.ID, native.NodeID) != nil || native.FullName != g.project.Owner+"/"+g.project.Repo || native.URL != g.apiURL(g.repoPath()) || native.HTMLURL != g.webPrefix() {
		return Resource{}, fmt.Errorf("%w: PR repository identity mismatch", ErrConflict)
	}
	return Resource{Kind: "repository", DatabaseID: native.ID, NodeID: native.NodeID}, nil
}

// ReadPullRequest deliberately excludes cross-repository/fork PRs. This read
// does not establish ancestry, issue association, approval, or an atomic snapshot.
func (g *Gateway) ReadPullRequest(ctx context.Context, number int64) (PullRequest, error) {
	if number <= 0 {
		return PullRequest{}, fmt.Errorf("positive pull request number required")
	}
	var native struct {
		ID       int64                   `json:"id"`
		NodeID   string                  `json:"node_id"`
		Number   int64                   `json:"number"`
		URL      string                  `json:"url"`
		HTMLURL  string                  `json:"html_url"`
		IssueURL string                  `json:"issue_url"`
		User     protocol.GitHubIdentity `json:"user"`
		State    string                  `json:"state"`
		Merged   *bool                   `json:"merged"`
		Draft    *bool                   `json:"draft"`
		MergeSHA *string                 `json:"merge_commit_sha"`
		Head     nativePullEnd           `json:"head"`
		Base     nativePullEnd           `json:"base"`
	}
	if err := g.get(ctx, fmt.Sprintf("%s/pulls/%d", g.repoPath(), number), &native); err != nil {
		return PullRequest{}, err
	}
	if stable(native.ID, native.NodeID) != nil || native.Number != number || native.URL != g.apiURL(fmt.Sprintf("%s/pulls/%d", g.repoPath(), number)) || native.HTMLURL != fmt.Sprintf("%s/pull/%d", g.webPrefix(), number) || native.IssueURL != g.apiURL(fmt.Sprintf("%s/issues/%d", g.repoPath(), number)) || native.User.Validate() != nil {
		return PullRequest{}, fmt.Errorf("%w: native PR identity mismatch", ErrConflict)
	}
	if (native.State != "open" && native.State != "closed") || native.Merged == nil || native.Draft == nil || (*native.Merged && (native.State != "closed" || *native.Draft)) {
		return PullRequest{}, fmt.Errorf("%w: missing or inconsistent PR state", ErrConflict)
	}
	if !sourceHex(native.Head.SHA, 40) || !sourceHex(native.Base.SHA, 40) || !branchName(native.Head.Ref) || !branchName(native.Base.Ref) || (native.MergeSHA != nil && !sourceHex(*native.MergeSHA, 40)) || (*native.Merged && native.MergeSHA == nil) {
		return PullRequest{}, fmt.Errorf("%w: invalid PR commits or branches", ErrConflict)
	}
	head, err := g.pullRepository(native.Head.Repo)
	if err != nil {
		return PullRequest{}, err
	}
	base, err := g.pullRepository(native.Base.Repo)
	if err != nil {
		return PullRequest{}, err
	}
	if head != base {
		return PullRequest{}, fmt.Errorf("%w: cross-repository PR is unsupported", ErrConflict)
	}
	result := PullRequest{Resource: Resource{Kind: "pull_request", Number: number, DatabaseID: native.ID, NodeID: native.NodeID}, Repository: head, BaseRepository: base, URL: native.HTMLURL, Author: native.User, State: native.State, Merged: *native.Merged, Draft: *native.Draft, HeadSHA: native.Head.SHA, BaseSHA: native.Base.SHA, HeadRef: native.Head.Ref, BaseRef: native.Base.Ref}
	if result.Merged {
		result.MergeSHA = *native.MergeSHA
	}
	return result, nil
}
