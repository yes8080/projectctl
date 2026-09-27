package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type Resource struct {
	Kind       string `json:"kind"`
	Number     int64  `json:"number,omitempty"`
	DatabaseID int64  `json:"database_id,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
}

type Content struct {
	Body            *string
	BodySHA256      string
	CanonicalSHA256 string
	IsRecord        bool
	RecordValid     bool
}

type Issue struct {
	Resource
	URL       string
	Title     string
	State     string
	CreatedAt string
	Author    protocol.GitHubIdentity
	Content   Content
	Milestone *Resource
}

type Comment struct {
	DatabaseID int64
	NodeID     string
	URL        string
	Parent     Resource
	Author     protocol.GitHubIdentity
	Content    Content
}

type Milestone struct {
	Resource
	URL     string
	Title   string
	State   string
	Author  protocol.GitHubIdentity
	Content Content
}

type nativeIssue struct {
	ID            int64                   `json:"id"`
	NodeID        string                  `json:"node_id"`
	Number        int64                   `json:"number"`
	APIURL        string                  `json:"url"`
	URL           string                  `json:"html_url"`
	RepositoryURL string                  `json:"repository_url"`
	Title         string                  `json:"title"`
	State         string                  `json:"state"`
	CreatedAt     string                  `json:"created_at"`
	Body          *string                 `json:"body"`
	User          protocol.GitHubIdentity `json:"user"`
	PullRequest   json.RawMessage         `json:"pull_request"`
	Milestone     *nativeMilestone        `json:"milestone"`
}

type nativeComment struct {
	ID             int64                   `json:"id"`
	NodeID         string                  `json:"node_id"`
	APIURL         string                  `json:"url"`
	URL            string                  `json:"html_url"`
	IssueURL       string                  `json:"issue_url"`
	PullRequestURL string                  `json:"pull_request_url"`
	Body           *string                 `json:"body"`
	User           protocol.GitHubIdentity `json:"user"`
}

type nativeMilestone struct {
	ID          int64                   `json:"id"`
	NodeID      string                  `json:"node_id"`
	Number      int64                   `json:"number"`
	APIURL      string                  `json:"url"`
	URL         string                  `json:"html_url"`
	Title       string                  `json:"title"`
	State       string                  `json:"state"`
	Description *string                 `json:"description"`
	Creator     protocol.GitHubIdentity `json:"creator"`
}

func content(body *string) Content {
	if body == nil {
		return Content{}
	}
	value := *body
	c := Content{Body: &value, BodySHA256: protocol.SHA256([]byte(value))}
	if !strings.HasPrefix(value, protocol.Marker) {
		return c
	}
	c.IsRecord = true
	data := []byte(strings.TrimSpace(strings.TrimPrefix(value, protocol.Marker)))
	if digest, err := protocol.DigestJSON(data); err == nil {
		c.CanonicalSHA256 = digest
	}
	_, err := protocol.DecodeComment(value)
	c.RecordValid = err == nil
	return c
}

func (g *Gateway) apiURL(path string) string { return "https://api.github.com" + path }
func (g *Gateway) webPrefix() string {
	return "https://github.com/" + g.project.Owner + "/" + g.project.Repo
}

func stable(id int64, node string) error {
	if id <= 0 || strings.TrimSpace(node) == "" {
		return fmt.Errorf("native stable object identity is missing")
	}
	return nil
}

func (g *Gateway) issue(n nativeIssue) (Issue, error) {
	var zero Issue
	if err := stable(n.ID, n.NodeID); err != nil {
		return zero, err
	}
	if n.Number <= 0 || n.APIURL != g.apiURL(fmt.Sprintf("%s/issues/%d", g.repoPath(), n.Number)) || n.RepositoryURL != g.apiURL(g.repoPath()) {
		return zero, fmt.Errorf("native issue belongs to another resource")
	}
	kind, webKind := "issue", "issues"
	if len(n.PullRequest) > 0 && string(n.PullRequest) != "null" {
		var pr struct {
			URL     string `json:"url"`
			HTMLURL string `json:"html_url"`
		}
		if err := decodeAPI(n.PullRequest, &pr); err != nil {
			return zero, err
		}
		if pr.URL != g.apiURL(fmt.Sprintf("%s/pulls/%d", g.repoPath(), n.Number)) || (pr.HTMLURL != "" && pr.HTMLURL != fmt.Sprintf("%s/pull/%d", g.webPrefix(), n.Number)) {
			return zero, fmt.Errorf("invalid pull_request marker")
		}
		kind, webKind = "pull_request", "pull"
	}
	if n.URL != fmt.Sprintf("%s/%s/%d", g.webPrefix(), webKind, n.Number) {
		return zero, fmt.Errorf("native issue/PR URL mismatch")
	}
	if n.State != "open" && n.State != "closed" {
		return zero, fmt.Errorf("unknown native issue state")
	}
	if err := n.User.Validate(); err != nil {
		return zero, err
	}
	result := Issue{Resource: Resource{Kind: kind, Number: n.Number, DatabaseID: n.ID, NodeID: n.NodeID}, URL: n.URL, Title: n.Title, State: n.State, CreatedAt: n.CreatedAt, Author: n.User, Content: content(n.Body)}
	if n.Milestone != nil {
		m, err := g.milestone(*n.Milestone)
		if err != nil {
			return zero, err
		}
		result.Milestone = &m.Resource
	}
	return result, nil
}

type Repository struct {
	Resource
	URL           string
	DefaultBranch string
}

func (g *Gateway) ReadRepository(ctx context.Context) (Repository, error) {
	var native struct {
		ID            int64  `json:"id"`
		NodeID        string `json:"node_id"`
		FullName      string `json:"full_name"`
		URL           string `json:"html_url"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.get(ctx, g.repoPath(), &native); err != nil {
		return Repository{}, err
	}
	if err := stable(native.ID, native.NodeID); err != nil {
		return Repository{}, err
	}
	if native.FullName != g.project.Owner+"/"+g.project.Repo || native.URL != g.webPrefix() || strings.TrimSpace(native.DefaultBranch) == "" {
		return Repository{}, fmt.Errorf("invalid native repository identity")
	}
	return Repository{Resource: Resource{Kind: "repository", DatabaseID: native.ID, NodeID: native.NodeID}, URL: native.URL, DefaultBranch: native.DefaultBranch}, nil
}

func (g *Gateway) milestone(n nativeMilestone) (Milestone, error) {
	var zero Milestone
	if err := stable(n.ID, n.NodeID); err != nil {
		return zero, err
	}
	if n.Number <= 0 || n.APIURL != g.apiURL(fmt.Sprintf("%s/milestones/%d", g.repoPath(), n.Number)) {
		return zero, fmt.Errorf("native milestone API identity mismatch")
	}
	u, err := url.Parse(n.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return zero, fmt.Errorf("invalid milestone URL")
	}
	// GitHub has returned both /milestone/N and legacy /milestones/TITLE URLs.
	numeric := fmt.Sprintf("/%s/%s/milestone/%d", g.project.Owner, g.project.Repo, n.Number)
	legacy := "/" + g.project.Owner + "/" + g.project.Repo + "/milestones/"
	if u.Path != numeric && (!strings.HasPrefix(u.Path, legacy) || len(u.Path) == len(legacy)) {
		return zero, fmt.Errorf("milestone belongs to another repository")
	}
	if n.State != "open" && n.State != "closed" {
		return zero, fmt.Errorf("unknown milestone state")
	}
	if err := n.Creator.Validate(); err != nil {
		return zero, err
	}
	return Milestone{Resource: Resource{Kind: "milestone", Number: n.Number, DatabaseID: n.ID, NodeID: n.NodeID}, URL: n.URL, Title: n.Title, State: n.State, Author: n.Creator, Content: content(n.Description)}, nil
}

func (g *Gateway) comment(n nativeComment, review bool) (Comment, error) {
	var zero Comment
	if err := stable(n.ID, n.NodeID); err != nil {
		return zero, err
	}
	if err := n.User.Validate(); err != nil {
		return zero, err
	}
	if n.Body == nil {
		return zero, fmt.Errorf("comment/review body is missing")
	}
	u, err := url.Parse(n.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawPath != "" || u.RawFragment != "" || u.RawQuery != "" || u.ForceQuery {
		return zero, fmt.Errorf("invalid native comment URL")
	}
	prefix := "/" + g.project.Owner + "/" + g.project.Repo + "/"
	if !strings.HasPrefix(u.Path, prefix) {
		return zero, fmt.Errorf("comment belongs to another repository")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
	if len(parts) != 2 || (parts[0] != "issues" && parts[0] != "pull") {
		return zero, fmt.Errorf("invalid comment parent")
	}
	number, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != parts[1] {
		return zero, fmt.Errorf("invalid comment parent number")
	}
	kind := "issue"
	if parts[0] == "pull" {
		kind = "pull_request"
	}
	if review {
		want := g.apiURL(fmt.Sprintf("%s/pulls/%d", g.repoPath(), number))
		if kind != "pull_request" || n.PullRequestURL != want || u.Fragment != fmt.Sprintf("pullrequestreview-%d", n.ID) {
			return zero, fmt.Errorf("native review identity mismatch")
		}
	} else {
		if n.APIURL != g.apiURL(fmt.Sprintf("%s/issues/comments/%d", g.repoPath(), n.ID)) || n.IssueURL != g.apiURL(fmt.Sprintf("%s/issues/%d", g.repoPath(), number)) || u.Fragment != fmt.Sprintf("issuecomment-%d", n.ID) {
			return zero, fmt.Errorf("native comment identity mismatch")
		}
	}
	return Comment{DatabaseID: n.ID, NodeID: n.NodeID, URL: n.URL, Parent: Resource{Kind: kind, Number: number}, Author: n.User, Content: content(n.Body)}, nil
}

func (g *Gateway) readIssue(ctx context.Context, number int64) (Issue, error) {
	if number <= 0 {
		return Issue{}, fmt.Errorf("positive issue number required")
	}
	var raw nativeIssue
	if err := g.get(ctx, fmt.Sprintf("%s/issues/%d", g.repoPath(), number), &raw); err != nil {
		return Issue{}, err
	}
	item, err := g.issue(raw)
	if err != nil {
		return Issue{}, err
	}
	if item.Number != number {
		return Issue{}, fmt.Errorf("GitHub returned another issue")
	}
	return item, nil
}

func (g *Gateway) ReadIssue(ctx context.Context, number int64) (Issue, error) {
	item, err := g.readIssue(ctx, number)
	if err != nil {
		return Issue{}, err
	}
	if item.Kind != "issue" {
		return Issue{}, fmt.Errorf("pull request masquerading as issue")
	}
	return item, nil
}

func (g *Gateway) ListIssues(ctx context.Context) ([]Issue, error) {
	raw, err := g.pages(ctx, g.repoPath()+"/issues", url.Values{"state": {"all"}, "sort": {"created"}, "direction": {"asc"}})
	if err != nil {
		return nil, err
	}
	items := []Issue{}
	seen := identities{}
	for _, data := range raw {
		var n nativeIssue
		if err := decodeAPI(data, &n); err != nil {
			return nil, err
		}
		item, err := g.issue(n)
		if err != nil {
			return nil, err
		}
		if err := seen.add(item.DatabaseID, item.NodeID, item.Number); err != nil {
			return nil, err
		}
		if item.Kind == "issue" {
			items = append(items, item)
		}
	}
	return items, nil
}

func (g *Gateway) ListComments(ctx context.Context, parent Resource) ([]Comment, error) {
	if (parent.Kind != "issue" && parent.Kind != "pull_request") || parent.Number <= 0 {
		return nil, fmt.Errorf("explicit issue/PR comment resource required")
	}
	raw, err := g.pages(ctx, fmt.Sprintf("%s/issues/%d/comments", g.repoPath(), parent.Number), nil)
	if err != nil {
		return nil, err
	}
	items := []Comment{}
	seen := identities{}
	for _, data := range raw {
		var n nativeComment
		if err := decodeAPI(data, &n); err != nil {
			return nil, err
		}
		item, err := g.comment(n, false)
		if err != nil {
			return nil, err
		}
		if item.Parent.Kind != parent.Kind || item.Parent.Number != parent.Number {
			return nil, fmt.Errorf("comment returned for wrong parent")
		}
		if err := seen.add(item.DatabaseID, item.NodeID, 0); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (g *Gateway) ReadComment(ctx context.Context, id int64) (Comment, error) {
	if id <= 0 {
		return Comment{}, fmt.Errorf("positive comment ID required")
	}
	var n nativeComment
	if err := g.get(ctx, fmt.Sprintf("%s/issues/comments/%d", g.repoPath(), id), &n); err != nil {
		return Comment{}, err
	}
	item, err := g.comment(n, false)
	if err != nil {
		return Comment{}, err
	}
	if item.DatabaseID != id {
		return Comment{}, fmt.Errorf("GitHub returned another comment")
	}
	return item, nil
}

func (g *Gateway) ListMilestones(ctx context.Context) ([]Milestone, error) {
	raw, err := g.pages(ctx, g.repoPath()+"/milestones", url.Values{"state": {"all"}, "sort": {"due_on"}, "direction": {"asc"}})
	if err != nil {
		return nil, err
	}
	items := []Milestone{}
	seen := identities{}
	for _, data := range raw {
		var n nativeMilestone
		if err := decodeAPI(data, &n); err != nil {
			return nil, err
		}
		item, err := g.milestone(n)
		if err != nil {
			return nil, err
		}
		if err := seen.add(item.DatabaseID, item.NodeID, item.Number); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (g *Gateway) ReadMilestone(ctx context.Context, number int64) (Milestone, error) {
	if number <= 0 {
		return Milestone{}, fmt.Errorf("positive milestone number required")
	}
	var n nativeMilestone
	if err := g.get(ctx, fmt.Sprintf("%s/milestones/%d", g.repoPath(), number), &n); err != nil {
		return Milestone{}, err
	}
	item, err := g.milestone(n)
	if err != nil {
		return Milestone{}, err
	}
	if item.Number != number {
		return Milestone{}, fmt.Errorf("wrong milestone number")
	}
	return item, nil
}

func (g *Gateway) ListDependencies(ctx context.Context, issue int64, direction string) ([]Issue, error) {
	if issue <= 0 || (direction != "blocked_by" && direction != "blocking") {
		return nil, fmt.Errorf("explicit native dependency direction required")
	}
	raw, err := g.pages(ctx, fmt.Sprintf("%s/issues/%d/dependencies/%s", g.repoPath(), issue, direction), nil)
	if err != nil {
		return nil, err
	}
	items := []Issue{}
	seen := identities{}
	for _, data := range raw {
		var n nativeIssue
		if err := decodeAPI(data, &n); err != nil {
			return nil, err
		}
		item, err := g.issue(n)
		if err != nil {
			return nil, err
		}
		if item.Kind != "issue" {
			return nil, fmt.Errorf("dependency is not a native issue")
		}
		if err := seen.add(item.DatabaseID, item.NodeID, item.Number); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (g *Gateway) CurrentUser(ctx context.Context) (protocol.GitHubIdentity, error) {
	var identity protocol.GitHubIdentity
	if err := g.get(ctx, "/user", &identity); err != nil {
		return identity, err
	}
	return identity, identity.Validate()
}

type identities struct {
	ids     map[int64]bool
	nodes   map[string]bool
	numbers map[int64]bool
}

func (s *identities) add(id int64, node string, number int64) error {
	if s.ids == nil {
		s.ids = map[int64]bool{}
		s.nodes = map[string]bool{}
		s.numbers = map[int64]bool{}
	}
	if s.ids[id] || s.nodes[node] || (number > 0 && s.numbers[number]) {
		return fmt.Errorf("%w: duplicate object in paginated collection", ErrConflict)
	}
	s.ids[id], s.nodes[node] = true, true
	if number > 0 {
		s.numbers[number] = true
	}
	return nil
}
