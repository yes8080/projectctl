package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type nativeMergeEvent struct {
	ID        int64                   `json:"id"`
	NodeID    string                  `json:"node_id"`
	URL       string                  `json:"url"`
	Event     string                  `json:"event"`
	Actor     protocol.GitHubIdentity `json:"actor"`
	CommitID  *string                 `json:"commit_id"`
	CommitURL *string                 `json:"commit_url"`
	Issue     *struct {
		ID            int64  `json:"id"`
		NodeID        string `json:"node_id"`
		Number        int64  `json:"number"`
		URL           string `json:"url"`
		RepositoryURL string `json:"repository_url"`
		HTMLURL       string `json:"html_url"`
		PullRequest   *struct {
			URL     string `json:"url"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
	} `json:"issue"`
}

// API 2026-03-10 no longer exposes merge_commit_sha in a PR response. The
// issue-scoped native events endpoint is the sole merge-commit authority here.
// Complete pagination is essential: the first merged event is not sufficient
// when another page can contain a contradictory or duplicated event.
func (g *Gateway) mergedEventCommit(ctx context.Context, number int64) (string, error) {
	items, err := g.pages(ctx, fmt.Sprintf("%s/issues/%d/events", g.repoPath(), number), nil)
	if err != nil {
		return "", err
	}
	ids, nodes := map[int64]bool{}, map[string]bool{}
	mergeSHA := ""
	for _, item := range items {
		var event nativeMergeEvent
		if err := decodeAPI(item, &event); err != nil {
			return "", err
		}
		if stable(event.ID, event.NodeID) != nil || ids[event.ID] || nodes[event.NodeID] || event.URL != g.apiURL(fmt.Sprintf("%s/issues/events/%d", g.repoPath(), event.ID)) || event.Actor.Validate() != nil || event.Event == "" || event.Event != strings.TrimSpace(event.Event) {
			return "", fmt.Errorf("%w: malformed or duplicate native issue event", ErrConflict)
		}
		ids[event.ID], nodes[event.NodeID] = true, true
		// The issue-scoped endpoint commonly omits this optional repository-wide
		// event field. When supplied, it must agree with the exact requested PR.
		if issue := event.Issue; issue != nil {
			if stable(issue.ID, issue.NodeID) != nil || issue.Number != number || issue.URL != g.apiURL(fmt.Sprintf("%s/issues/%d", g.repoPath(), number)) || issue.RepositoryURL != g.apiURL(g.repoPath()) || issue.HTMLURL != fmt.Sprintf("%s/pull/%d", g.webPrefix(), number) || issue.PullRequest == nil || issue.PullRequest.URL != g.apiURL(fmt.Sprintf("%s/pulls/%d", g.repoPath(), number)) || issue.PullRequest.HTMLURL != fmt.Sprintf("%s/pull/%d", g.webPrefix(), number) {
				return "", fmt.Errorf("%w: native issue event belongs to another subject", ErrConflict)
			}
		}
		if event.Event != "merged" {
			continue
		}
		if mergeSHA != "" || event.CommitID == nil || !sourceHex(*event.CommitID, 40) || event.CommitURL == nil || *event.CommitURL != g.apiURL(g.repoPath()+"/commits/"+*event.CommitID) {
			return "", fmt.Errorf("%w: missing, malformed or contradictory native merge commit", ErrConflict)
		}
		mergeSHA = *event.CommitID
	}
	if mergeSHA == "" {
		return "", fmt.Errorf("%w: merged PR has no authoritative native merged event", ErrConflict)
	}
	return mergeSHA, nil
}
