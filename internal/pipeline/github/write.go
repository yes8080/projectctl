package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type Action string

const (
	CreateIssue     Action = "create_issue"
	CreateMilestone Action = "create_milestone"
	PublishRecord   Action = "publish_record"
	AddDependency   Action = "add_dependency"
)

const operationMarker = "<!-- projectctl:operation:v1 -->"
const operationSchema = "projectctl.operation/v1"

// Intent is a remote operation journal entry, not a second task/contract copy.
// It carries only resource identities and a digest of the exact desired request.
type Intent struct {
	Schema        string           `json:"schema"`
	Kind          string           `json:"kind"`
	OperationID   string           `json:"operation_id"`
	Phase         string           `json:"phase"`
	Project       protocol.Project `json:"project"`
	Action        Action           `json:"action"`
	Control       Resource         `json:"control"`
	Target        Resource         `json:"subject"`
	Related       *Resource        `json:"related,omitempty"`
	RequestSHA256 string           `json:"request_sha256"`
}

// Request is a trusted Controller call, not an authorization format accepted
// from Agents. Authorize must independently approve the exact Intent/identity.
// Existing resources require number + database ID + node ID, not a guessed URL.
type Request struct {
	Project     protocol.Project
	OperationID string
	Action      Action
	Control     Resource
	Target      Resource
	Related     *Resource
	Title       string
	Body        string
	Record      protocol.Record
}

type Outcome struct {
	OperationID string
	State       string // applied, reconciled, or uncertain; never inferred from a cache
	Intent      *Comment
	Issue       *Issue
	Milestone   *Milestone
	Comment     *Comment
}

type prepared struct {
	intent     Intent
	intentBody string
	objectBody string
	title      string
	actor      protocol.GitHubIdentity
}

var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)

func (g *Gateway) prepare(ctx context.Context, request Request) (prepared, error) {
	var zero prepared
	if g.authorize == nil {
		return zero, ErrDenied
	}
	if request.Project != g.project || !operationIDPattern.MatchString(request.OperationID) {
		return zero, fmt.Errorf("explicit project and stable operation_id required")
	}
	if request.Control.Kind != "issue" || request.Control.Number != g.project.ControlIssue {
		return zero, fmt.Errorf("explicit project control issue required")
	}
	if err := g.verifyResource(ctx, request.Control); err != nil {
		return zero, err
	}
	if err := g.verifyResource(ctx, request.Target); err != nil {
		return zero, err
	}
	var related *Resource
	if request.Related != nil {
		copy := *request.Related
		related = &copy
		if err := g.verifyResource(ctx, copy); err != nil {
			return zero, err
		}
	}
	body := request.Body
	if !utf8.ValidString(body) || !utf8.ValidString(request.Title) {
		return zero, fmt.Errorf("invalid UTF-8 write content")
	}
	switch request.Action {
	case CreateIssue:
		if request.Target.Kind != "repository" || (related != nil && related.Kind != "milestone") || request.Record != nil || strings.TrimSpace(request.Title) == "" {
			return zero, fmt.Errorf("invalid explicit issue creation request")
		}
	case CreateMilestone:
		if request.Target.Kind != "repository" || related != nil || request.Record != nil || strings.TrimSpace(request.Title) == "" {
			return zero, fmt.Errorf("invalid explicit milestone creation request")
		}
	case PublishRecord:
		if request.Record == nil || related != nil || request.Title != "" || request.Body != "" {
			return zero, fmt.Errorf("record publication requires only a typed record and exact parent")
		}
		var err error
		body, err = protocol.EncodeComment(request.Record)
		if err != nil {
			return zero, err
		}
		header := request.Record.Header()
		if header.Project != g.project || header.OperationID != request.OperationID {
			return zero, fmt.Errorf("record project/operation_id differs from write request")
		}
		if err := recordTarget(header, request.Target); err != nil {
			return zero, err
		}
	case AddDependency:
		if request.Target.Kind != "issue" || related == nil || related.Kind != "issue" || related.Number == request.Target.Number || request.Title != "" || request.Body != "" || request.Record != nil {
			return zero, fmt.Errorf("dependency requires two distinct exact issue resources")
		}
	default:
		return zero, fmt.Errorf("unsupported write action")
	}
	if request.Action != PublishRecord && (strings.Contains(body, operationMarker) || strings.Contains(body, protocol.Marker)) {
		return zero, fmt.Errorf("object navigation text must not contain managed record markers")
	}
	if len(request.Title) > 256 || len(body) > 60000 {
		return zero, fmt.Errorf("write content exceeds publication bound")
	}
	fingerprint := struct {
		Project     protocol.Project `json:"project"`
		OperationID string           `json:"operation_id"`
		Action      Action           `json:"action"`
		Control     Resource         `json:"control"`
		Target      Resource         `json:"target"`
		Related     *Resource        `json:"related,omitempty"`
		Title       string           `json:"title"`
		Body        string           `json:"body"`
	}{g.project, request.OperationID, request.Action, request.Control, request.Target, related, request.Title, body}
	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		return zero, fmt.Errorf("invalid write fingerprint")
	}
	digest, err := protocol.DigestJSON(encoded)
	if err != nil {
		return zero, err
	}
	intent := Intent{Schema: operationSchema, Kind: "operation", OperationID: request.OperationID, Phase: "intent", Project: g.project, Action: request.Action, Control: request.Control, Target: request.Target, Related: related, RequestSHA256: digest}
	intentBody, err := encodeIntent(intent)
	if err != nil {
		return zero, err
	}
	objectBody := body
	if request.Action == CreateIssue || request.Action == CreateMilestone {
		objectIntent := intent
		objectIntent.Phase = "object"
		marker, err := encodeIntent(objectIntent)
		if err != nil {
			return zero, err
		}
		objectBody = body + "\n\n" + marker
	}
	if len(objectBody) > 65000 || len(intentBody) > 65000 {
		return zero, fmt.Errorf("comment/body exceeds GitHub publication bound")
	}
	if err := g.publisher.Validate(); err != nil {
		return zero, ErrDenied
	}
	actor, err := g.CurrentUser(ctx)
	if err != nil {
		return zero, err
	}
	if !sameIdentity(actor, g.publisher) {
		return zero, fmt.Errorf("%w: authenticated publisher identity mismatch", ErrDenied)
	}
	approvalInput := intent
	if related != nil {
		copy := *related
		approvalInput.Related = &copy
	}
	if err := g.authorize(ctx, approvalInput, actor); err != nil {
		return zero, ErrDenied
	}
	return prepared{intent: intent, intentBody: intentBody, objectBody: objectBody, title: request.Title, actor: actor}, nil
}

func (g *Gateway) verifyResource(ctx context.Context, want Resource) error {
	if err := stable(want.DatabaseID, want.NodeID); err != nil {
		return err
	}
	var got Resource
	switch want.Kind {
	case "issue", "pull_request":
		item, err := g.readIssue(ctx, want.Number)
		if err != nil {
			return err
		}
		got = item.Resource
	case "milestone":
		item, err := g.ReadMilestone(ctx, want.Number)
		if err != nil {
			return err
		}
		got = item.Resource
	case "repository":
		if want.Number != 0 {
			return fmt.Errorf("repository has no issue number")
		}
		var repo struct {
			ID       int64  `json:"id"`
			NodeID   string `json:"node_id"`
			FullName string `json:"full_name"`
		}
		if err := g.get(ctx, g.repoPath(), &repo); err != nil {
			return err
		}
		if repo.FullName != g.project.Owner+"/"+g.project.Repo {
			return fmt.Errorf("repository identity mismatch")
		}
		got = Resource{Kind: "repository", DatabaseID: repo.ID, NodeID: repo.NodeID}
	default:
		return fmt.Errorf("unsupported explicit resource")
	}
	if got != want {
		return fmt.Errorf("native resource identity drift")
	}
	return nil
}

func recordTarget(header protocol.Envelope, target Resource) error {
	if target.Kind != "issue" && target.Kind != "pull_request" {
		return fmt.Errorf("record requires explicit issue/PR parent")
	}
	match := false
	switch header.Kind {
	case protocol.KindContract:
		match = target.Kind == "issue" && target.Number == header.Subject.Issue
	case protocol.KindEvidence, protocol.KindAcceptance:
		match = target.Kind == "pull_request" && target.Number == header.Subject.PullRequest
	case protocol.KindRun:
		match = (target.Kind == "issue" && (target.Number == header.Subject.Issue || target.Number == header.Project.ControlIssue)) || (target.Kind == "pull_request" && target.Number == header.Subject.PullRequest)
	default:
		match = target.Kind == "issue" && target.Number == header.Project.ControlIssue
	}
	if !match {
		return fmt.Errorf("record publication target disagrees with subject")
	}
	return nil
}

func encodeIntent(intent Intent) (string, error) {
	data, err := json.Marshal(intent)
	if err != nil {
		return "", err
	}
	canonical, err := protocol.CanonicalJSON(data)
	if err != nil {
		return "", err
	}
	return operationMarker + " " + string(canonical), nil
}

func parseIntent(body string) (*Intent, error) {
	count := strings.Count(body, operationMarker)
	if count == 0 {
		return nil, nil
	}
	if count != 1 {
		return nil, fmt.Errorf("%w: multiple operation markers", ErrConflict)
	}
	_, text, _ := strings.Cut(body, operationMarker)
	canonical, err := protocol.CanonicalJSON([]byte(strings.TrimSpace(text)))
	if err != nil {
		return nil, fmt.Errorf("%w: malformed operation marker", ErrConflict)
	}
	var intent Intent
	d := json.NewDecoder(bytes.NewReader(canonical))
	d.DisallowUnknownFields()
	if err := d.Decode(&intent); err != nil || intent.Schema != operationSchema || intent.Kind != "operation" || !operationIDPattern.MatchString(intent.OperationID) || (intent.Phase != "intent" && intent.Phase != "object") {
		return nil, fmt.Errorf("%w: invalid operation metadata", ErrConflict)
	}
	return &intent, nil
}

func equalIntent(a, b Intent) bool {
	left, _ := encodeIntent(a)
	right, _ := encodeIntent(b)
	return left == right
}

func (g *Gateway) inspect(ctx context.Context, p prepared) (Outcome, bool, error) {
	result := Outcome{OperationID: p.intent.OperationID, State: "uncertain"}
	comments, err := g.ListComments(ctx, p.intent.Control)
	if err != nil {
		return result, false, err
	}
	for _, comment := range comments {
		intent, err := parseIntent(*comment.Content.Body)
		if err != nil {
			return result, false, err
		}
		if intent == nil || intent.OperationID != p.intent.OperationID {
			continue
		}
		if result.Intent != nil {
			return result, false, fmt.Errorf("%w: duplicate intent comments", ErrConflict)
		}
		if !equalIntent(*intent, p.intent) || *comment.Content.Body != p.intentBody || !sameIdentity(comment.Author, p.actor) {
			return result, false, fmt.Errorf("%w: operation intent drift or publisher mismatch", ErrConflict)
		}
		copy := comment
		result.Intent = &copy
	}
	found := false
	matchObject := func(body *string, author protocol.GitHubIdentity, title string) (bool, error) {
		if body == nil {
			return false, nil
		}
		intent, err := parseIntent(*body)
		if err != nil {
			return false, err
		}
		if intent == nil || intent.OperationID != p.intent.OperationID {
			return false, nil
		}
		expected := p.intent
		expected.Phase = "object"
		if !equalIntent(*intent, expected) || *body != p.objectBody || title != p.title || !sameIdentity(author, p.actor) {
			return false, fmt.Errorf("%w: created object drift", ErrConflict)
		}
		return true, nil
	}
	switch p.intent.Action {
	case CreateIssue:
		items, err := g.ListIssues(ctx)
		if err != nil {
			return result, false, err
		}
		for _, item := range items {
			match, err := matchObject(item.Content.Body, item.Author, item.Title)
			if err != nil {
				return result, false, err
			}
			if !match {
				continue
			}
			if found {
				return result, false, fmt.Errorf("%w: duplicate created issues", ErrConflict)
			}
			if (p.intent.Related == nil) != (item.Milestone == nil) || (p.intent.Related != nil && *p.intent.Related != *item.Milestone) {
				return result, false, fmt.Errorf("%w: native milestone relation drift", ErrConflict)
			}
			copy := item
			result.Issue = &copy
			found = true
		}
	case CreateMilestone:
		items, err := g.ListMilestones(ctx)
		if err != nil {
			return result, false, err
		}
		for _, item := range items {
			match, err := matchObject(item.Content.Body, item.Author, item.Title)
			if err != nil {
				return result, false, err
			}
			if !match {
				continue
			}
			if found {
				return result, false, fmt.Errorf("%w: duplicate created milestones", ErrConflict)
			}
			copy := item
			result.Milestone = &copy
			found = true
		}
	case PublishRecord:
		items, err := g.ListComments(ctx, p.intent.Target)
		if err != nil {
			return result, false, err
		}
		for _, item := range items {
			if !item.Content.IsRecord {
				continue
			}
			data := []byte(strings.TrimSpace(strings.TrimPrefix(*item.Content.Body, protocol.Marker)))
			canonical, err := protocol.CanonicalJSON(data)
			if err != nil {
				return result, false, fmt.Errorf("%w: malformed managed record", ErrConflict)
			}
			var header struct {
				OperationID string `json:"operation_id"`
			}
			if err := json.Unmarshal(canonical, &header); err != nil || header.OperationID == "" {
				return result, false, fmt.Errorf("%w: managed record has no operation identity", ErrConflict)
			}
			if header.OperationID != p.intent.OperationID {
				continue
			}
			if found {
				return result, false, fmt.Errorf("%w: duplicate record comments", ErrConflict)
			}
			if !item.Content.RecordValid || *item.Content.Body != p.objectBody || !sameIdentity(item.Author, p.actor) {
				return result, false, fmt.Errorf("%w: published record drift", ErrConflict)
			}
			copy := item
			result.Comment = &copy
			found = true
		}
	case AddDependency:
		items, err := g.ListDependencies(ctx, p.intent.Target.Number, "blocked_by")
		if err != nil {
			return result, false, err
		}
		for _, item := range items {
			if item.Number != p.intent.Related.Number && item.DatabaseID != p.intent.Related.DatabaseID {
				continue
			}
			if item.Resource != *p.intent.Related {
				return result, false, fmt.Errorf("%w: dependency identity drift", ErrConflict)
			}
			copy := item
			result.Issue = &copy
			found = true
		}
	}
	if found && result.Intent == nil && p.intent.Action != AddDependency {
		return result, false, fmt.Errorf("%w: object exists but its intent is missing", ErrConflict)
	}
	if found {
		result.State = "reconciled"
	}
	return result, found, nil
}

// Reconcile performs only reads, even if no intent/object is found. A fresh
// Gateway can recover a completed operation from GitHub alone.
func (g *Gateway) Reconcile(ctx context.Context, request Request) (Outcome, error) {
	if g.authorize == nil {
		return Outcome{}, ErrDenied
	}
	bound, err := g.bindCredential(ctx)
	if err != nil {
		return Outcome{}, err
	}
	ctx = bound
	p, err := g.prepare(ctx, request)
	if err != nil {
		return Outcome{}, err
	}
	result, found, err := g.inspect(ctx, p)
	if err != nil {
		return result, err
	}
	if !found || result.Intent == nil {
		return result, ErrUncertain
	}
	return result, nil
}

// Apply is a recovery entry point and never sends a POST. In particular, an
// empty list cannot distinguish a new operation from a temporarily invisible
// accepted intent. First creation requires ApplyFirst and a fresh admission.
func (g *Gateway) Apply(ctx context.Context, request Request) (Outcome, error) {
	return g.Reconcile(ctx, request)
}

// ApplyFirst may register an intent and send one effect only with an unspent
// admission from a trusted Controller fresh-allocation event. Existing intent
// and unknown outcome paths remain reconciliation-only, even with admission.
func (g *Gateway) ApplyFirst(ctx context.Context, request Request, admission FirstCreateAdmission) (Outcome, error) {
	if g.authorize == nil {
		return Outcome{}, ErrDenied
	}
	bound, err := g.bindCredential(ctx)
	if err != nil {
		return Outcome{}, err
	}
	ctx = bound
	p, err := g.prepare(ctx, request)
	if err != nil {
		return Outcome{}, err
	}
	if err := admission.matches(g, p); err != nil {
		return Outcome{}, err
	}
	fresh := admission.claim()
	result, found, err := g.inspect(ctx, p)
	if err != nil {
		return result, err
	}
	if found && result.Intent != nil {
		return result, nil
	}
	if result.Intent != nil || !fresh {
		return result, ErrUncertain
	}
	// The shared capability was already consumed before inspection. Revalidate
	// its remote authorization before dispatch; failure cannot restore it.
	if err := admission.verify(ctx, g); err != nil {
		return result, err
	}
	_, _, sendErr := g.request(ctx, http.MethodPost, g.endpoint(fmt.Sprintf("%s/issues/%d/comments", g.repoPath(), p.intent.Control.Number)), struct {
		Body string `json:"body"`
	}{p.intentBody})
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	result, found, err = g.inspect(reconcileCtx, p)
	cancel()
	if err != nil {
		return result, fmt.Errorf("%w: intent reconciliation failed: %w", ErrUncertain, err)
	}
	if result.Intent == nil || sendErr != nil {
		return result, ErrUncertain
	}
	if found {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ErrUncertain
	}
	path := ""
	var payload any
	switch p.intent.Action {
	case CreateIssue:
		path = g.repoPath() + "/issues"
		var milestone *int64
		if p.intent.Related != nil {
			n := p.intent.Related.Number
			milestone = &n
		}
		payload = struct {
			Title     string `json:"title"`
			Body      string `json:"body"`
			Milestone *int64 `json:"milestone,omitempty"`
		}{p.title, p.objectBody, milestone}
	case CreateMilestone:
		path = g.repoPath() + "/milestones"
		payload = struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}{p.title, p.objectBody}
	case PublishRecord:
		path = fmt.Sprintf("%s/issues/%d/comments", g.repoPath(), p.intent.Target.Number)
		payload = struct {
			Body string `json:"body"`
		}{p.objectBody}
	case AddDependency:
		path = fmt.Sprintf("%s/issues/%d/dependencies/blocked_by", g.repoPath(), p.intent.Target.Number)
		payload = struct {
			IssueID int64 `json:"issue_id"`
		}{p.intent.Related.DatabaseID}
	}
	_, _, sendErr = g.request(ctx, http.MethodPost, g.endpoint(path), payload)
	reconcileCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	result, found, err = g.inspect(reconcileCtx, p)
	cancel()
	if err != nil {
		return result, fmt.Errorf("%w: effect reconciliation failed: %w", ErrUncertain, err)
	}
	if !found || result.Intent == nil {
		return result, ErrUncertain
	}
	if sendErr == nil {
		result.State = "applied"
	}
	return result, nil
}
