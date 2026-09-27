package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type writeFixture struct {
	mu              sync.Mutex
	issues          map[int64]map[string]any
	milestones      map[int64]map[string]any
	comments        map[int64][]map[string]any
	dependencies    []int64
	posts           map[string]int
	events          []string
	duplicateIntent bool
	duplicateEffect bool
	user            protocol.GitHubIdentity
	server          *httptest.Server
}

func newWriteFixture(t *testing.T) *writeFixture {
	t.Helper()
	f := &writeFixture{issues: map[int64]map[string]any{}, milestones: map[int64]map[string]any{}, comments: map[int64][]map[string]any{}, posts: map[string]int{}, user: testServerFixtureAuthor()}
	for _, n := range []int64{2, 4, 5} {
		f.issues[n] = testServerFixtureIssue(n, nil)
		f.comments[n] = []map[string]any{}
	}
	f.milestones[1] = testServerFixtureMilestone(1, nil)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.handle(t, w, r) }))
	t.Cleanup(f.server.Close)
	return f
}

func (f *writeFixture) gateway(t *testing.T, transport http.RoundTripper) *Gateway {
	t.Helper()
	client := f.server.Client()
	if transport != nil {
		client = &http.Client{Transport: transport}
	}
	g, err := New(Config{Project: testServerFixtureProject(), BaseURL: f.server.URL, AllowLoopbackTestServer: true, HTTPClient: client, Publisher: testServerFixtureAuthor(), Token: func(context.Context) (string, error) { return "synthetic-test-bearer", nil }, Authorize: func(_ context.Context, intent Intent, author protocol.GitHubIdentity) error {
		if intent.Project != testServerFixtureProject() || intent.Control != writeResource("issue", 2) || !sameIdentity(author, testServerFixtureAuthor()) || len(intent.RequestSHA256) != 64 {
			return ErrDenied
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func writeResource(kind string, n int64) Resource {
	switch kind {
	case "repository":
		return Resource{Kind: kind, DatabaseID: 99, NodeID: "R_99"}
	case "milestone":
		return Resource{Kind: kind, Number: n, DatabaseID: 2000 + n, NodeID: fmt.Sprintf("M_%d", n)}
	default:
		return Resource{Kind: kind, Number: n, DatabaseID: 1000 + n, NodeID: fmt.Sprintf("I_%d", n)}
	}
}

func writeRequest(action Action) Request {
	r := Request{Project: testServerFixtureProject(), OperationID: "test-" + string(action), Action: action, Control: writeResource("issue", 2), Target: writeResource("repository", 0), Title: "created by gateway", Body: "Navigation only"}
	switch action {
	case CreateIssue:
		m := writeResource("milestone", 1)
		r.Related = &m
	case PublishRecord:
		r.Target = writeResource("issue", 4)
		r.Title = ""
		r.Body = ""
		r.Record = gatewayContract()
		r.Record.(*protocol.Contract).OperationID = r.OperationID
	case AddDependency:
		r.Target = writeResource("issue", 4)
		related := writeResource("issue", 5)
		r.Related = &related
		r.Title = ""
		r.Body = ""
	}
	return r
}

// A fixture Controller emits this event once. This is not a production source
// implementation and must not be replaced by "re-read the latest run".
func (f *writeFixture) freshAllocation(t *testing.T) FreshAllocationSource {
	t.Helper()
	var consumed atomic.Bool
	return func(_ context.Context, intent Intent, author protocol.GitHubIdentity) (protocol.VerifiedRecord, error) {
		if !consumed.CompareAndSwap(false, true) {
			return protocol.VerifiedRecord{}, ErrDenied
		}
		_, input := recordNative(t, gatewayContract(), 31)
		run := &protocol.Run{
			Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindRun, Project: intent.Project, Subject: protocol.Subject{Issue: 2}, OperationID: "allocate-" + intent.OperationID, Version: 1},
			Role:     protocol.RolePublisher, AgentInstance: "fixture-controller", Host: "test", Inputs: []protocol.Reference{input}, InputSHA256: intent.RequestSHA256,
			AssignmentGeneration: 1, Attempt: 1,
			Budget: protocol.Budget{MaxAgentRuns: 10, MaxRunSeconds: 60, MaxWallSeconds: 600, MaxDesignRounds: 2, MaxPlanningRounds: 2, MaxImplementationRounds: 3, Currency: "USD"},
		}
		f.mu.Lock()
		id := int64(900 + len(f.comments[2]))
		native, ref := recordNative(t, run, id)
		f.comments[2] = append(f.comments[2], native)
		f.mu.Unlock()
		return protocol.VerifyGitHubRecord(protocol.GitHubObservation{Project: intent.Project, Kind: ref.Kind, DatabaseID: ref.DatabaseID, NodeID: ref.NodeID, URL: ref.URL, Author: author, Body: native["body"].(string)}, ref)
	}
}

func (f *writeFixture) applyFirst(t *testing.T, g *Gateway, request Request) (Outcome, error) {
	t.Helper()
	admission, err := g.AdmitFirstCreate(context.Background(), request, f.freshAllocation(t))
	if err != nil {
		return Outcome{}, err
	}
	return g.ApplyFirst(context.Background(), request, admission)
}

func (f *writeFixture) handle(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer synthetic-test-bearer" {
		t.Error("missing test credentials")
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/octo/pipeline")
	if r.Method == http.MethodGet {
		switch {
		case r.URL.Path == "/user":
			testServerFixtureJSON(t, w, f.user)
		case path == "":
			testServerFixtureJSON(t, w, map[string]any{"id": 99, "node_id": "R_99", "full_name": "octo/pipeline", "html_url": "https://github.com/octo/pipeline", "default_branch": "main"})
		case path == "/issues":
			values := []any{}
			for _, n := range sortedNativeKeys(f.issues) {
				values = append(values, f.issues[n])
			}
			testServerFixtureJSON(t, w, values)
		case path == "/milestones":
			values := []any{}
			for _, n := range sortedNativeKeys(f.milestones) {
				values = append(values, f.milestones[n])
			}
			testServerFixtureJSON(t, w, values)
		case strings.HasSuffix(path, "/dependencies/blocked_by"):
			values := []any{}
			for _, n := range f.dependencies {
				values = append(values, f.issues[n])
			}
			testServerFixtureJSON(t, w, values)
		case strings.HasPrefix(path, "/issues/comments/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/issues/comments/"), 10, 64)
			for _, comments := range f.comments {
				for _, item := range comments {
					if item["id"] == id {
						testServerFixtureJSON(t, w, item)
						return
					}
				}
			}
			http.NotFound(w, r)
		case strings.HasSuffix(path, "/comments"):
			n, _ := strconv.ParseInt(strings.Split(path, "/")[2], 10, 64)
			items := f.comments[n]
			if items == nil {
				items = []map[string]any{}
			}
			testServerFixtureJSON(t, w, items)
		case strings.HasPrefix(path, "/issues/"):
			n, _ := strconv.ParseInt(strings.TrimPrefix(path, "/issues/"), 10, 64)
			if item, ok := f.issues[n]; ok {
				testServerFixtureJSON(t, w, item)
			} else {
				http.NotFound(w, r)
			}
		case strings.HasPrefix(path, "/milestones/"):
			n, _ := strconv.ParseInt(strings.TrimPrefix(path, "/milestones/"), 10, 64)
			if item, ok := f.milestones[n]; ok {
				testServerFixtureJSON(t, w, item)
			} else {
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != http.MethodPost {
		t.Errorf("unsupported mutation: %s", r.Method)
		http.Error(w, "no", 405)
		return
	}
	f.posts[path]++
	var payload struct {
		Title       string `json:"title"`
		Body        string `json:"body"`
		Description string `json:"description"`
		Milestone   *int64 `json:"milestone"`
		IssueID     int64  `json:"issue_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		t.Error(err)
		http.Error(w, "bad", 400)
		return
	}
	switch {
	case strings.HasSuffix(path, "/comments"):
		n, _ := strconv.ParseInt(strings.Split(path, "/")[2], 10, 64)
		count := 1
		if n == 2 && f.duplicateIntent {
			count = 2
		}
		if n == 4 && f.duplicateEffect {
			count = 2
		}
		var item map[string]any
		for i := 0; i < count; i++ {
			id := int64(300 + len(f.comments[n]))
			item = testServerFixtureComment(n, id, payload.Body)
			f.comments[n] = append(f.comments[n], item)
		}
		w.WriteHeader(201)
		testServerFixtureJSON(t, w, item)
	case path == "/issues":
		count := 1
		if f.duplicateEffect {
			count = 2
		}
		var item map[string]any
		for i := 0; i < count; i++ {
			n := int64(20 + len(f.issues))
			item = testServerFixtureIssue(n, &payload.Body)
			item["title"] = payload.Title
			if payload.Milestone != nil {
				item["milestone"] = f.milestones[*payload.Milestone]
			}
			f.issues[n] = item
		}
		w.WriteHeader(201)
		testServerFixtureJSON(t, w, item)
	case path == "/milestones":
		count := 1
		if f.duplicateEffect {
			count = 2
		}
		var item map[string]any
		for i := 0; i < count; i++ {
			n := int64(20 + len(f.milestones))
			item = testServerFixtureMilestone(n, &payload.Description)
			item["title"] = payload.Title
			f.milestones[n] = item
		}
		w.WriteHeader(201)
		testServerFixtureJSON(t, w, item)
	case path == "/issues/4/dependencies/blocked_by":
		if payload.IssueID != 1005 {
			t.Errorf("dependency used number instead of database ID: %d", payload.IssueID)
		}
		f.dependencies = append(f.dependencies, 5)
		w.WriteHeader(201)
		testServerFixtureJSON(t, w, f.issues[5])
	default:
		t.Errorf("unexpected write target: %s", path)
		http.NotFound(w, r)
	}
}

func sortedNativeKeys(m map[int64]map[string]any) []int64 {
	keys := []int64{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func TestScopedWritesAreRemoteIdempotent(t *testing.T) {
	for _, action := range []Action{CreateIssue, CreateMilestone, PublishRecord, AddDependency} {
		t.Run(string(action), func(t *testing.T) {
			f := newWriteFixture(t)
			g := f.gateway(t, nil)
			request := writeRequest(action)
			result, err := f.applyFirst(t, g, request)
			if err != nil || result.State != "applied" || result.Intent == nil {
				t.Fatalf("apply: %#v %v", result, err)
			}
			f.mu.Lock()
			before := 0
			for _, n := range f.posts {
				before += n
			}
			events := append([]string(nil), f.events...)
			f.mu.Unlock()
			if before != 2 {
				t.Fatalf("want one intent and one effect, got %d", before)
			}
			intentIndex, effectIndex := -1, -1
			for i, event := range events {
				if event == "POST /repos/octo/pipeline/issues/2/comments" {
					intentIndex = i
				}
				if strings.HasPrefix(event, "POST ") && event != "POST /repos/octo/pipeline/issues/2/comments" {
					effectIndex = i
				}
			}
			if intentIndex < 0 || effectIndex <= intentIndex {
				t.Fatal("effect preceded durable intent")
			}
			// Reconstruct with a new client: there is no local cache to retain/delete.
			g = f.gateway(t, nil)
			again, err := g.Apply(context.Background(), request)
			if err != nil || again.State != "reconciled" {
				t.Fatalf("recover: %#v %v", again, err)
			}
			f.mu.Lock()
			after := 0
			for _, n := range f.posts {
				after += n
			}
			f.mu.Unlock()
			if before != after {
				t.Fatal("recovery repeated a write")
			}
		})
	}
}

func TestTimeoutReconciliationNeverBlindlyRetries(t *testing.T) {
	for _, save := range []bool{true, false} {
		t.Run(fmt.Sprint("saved=", save), func(t *testing.T) {
			f := newWriteFixture(t)
			var attempted atomic.Int64
			base := f.server.Client().Transport
			transport := testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost && r.URL.Path == "/repos/octo/pipeline/issues" {
					attempted.Add(1)
					if save {
						resp, err := base.RoundTrip(r)
						if err != nil {
							return nil, err
						}
						resp.Body.Close()
					}
					return nil, context.DeadlineExceeded
				}
				return base.RoundTrip(r)
			})
			g := f.gateway(t, transport)
			request := writeRequest(CreateIssue)
			result, err := f.applyFirst(t, g, request)
			if save {
				if err != nil || result.State != "reconciled" || result.Issue == nil {
					t.Fatalf("saved timeout not recovered: %#v %v", result, err)
				}
			} else {
				if !errors.Is(err, ErrUncertain) || result.Intent == nil {
					t.Fatalf("missing effect not uncertain: %#v %v", result, err)
				}
			}
			g = f.gateway(t, transport)
			_, err = g.Apply(context.Background(), request)
			if !save && !errors.Is(err, ErrUncertain) {
				t.Fatalf("restart lost uncertainty: %v", err)
			}
			if attempted.Load() != 1 {
				t.Fatal("blind effect retry after timeout")
			}
		})
	}
}

func TestDuplicateRemoteObjectsFailClosed(t *testing.T) {
	for _, action := range []Action{CreateIssue, CreateMilestone, PublishRecord} {
		t.Run(string(action), func(t *testing.T) {
			f := newWriteFixture(t)
			f.duplicateEffect = true
			g := f.gateway(t, nil)
			if _, err := f.applyFirst(t, g, writeRequest(action)); !errors.Is(err, ErrConflict) {
				t.Fatalf("duplicate accepted: %v", err)
			}
		})
	}
	t.Run("intent", func(t *testing.T) {
		f := newWriteFixture(t)
		f.duplicateIntent = true
		g := f.gateway(t, nil)
		if _, err := f.applyFirst(t, g, writeRequest(CreateIssue)); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.posts["/issues"] != 0 {
			t.Fatal("effect sent despite duplicate intents")
		}
	})
}

func TestWriteScopeAndNativePublisherBinding(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Gateway, *Request)
	}{
		{"no authorizer", func(g *Gateway, r *Request) { g.authorize = nil }},
		{"denied", func(g *Gateway, r *Request) {
			g.authorize = func(context.Context, Intent, protocol.GitHubIdentity) error {
				return errors.New("secret authorization context")
			}
		}},
		{"wrong publisher", func(g *Gateway, r *Request) { g.publisher.ID++ }},
		{"wrong project", func(g *Gateway, r *Request) { r.Project.Repo = "other" }},
		{"no operation", func(g *Gateway, r *Request) { r.OperationID = "" }},
		{"wrong native ID", func(g *Gateway, r *Request) { r.Control.DatabaseID++ }},
		{"wrong target", func(g *Gateway, r *Request) { r.Target = writeResource("issue", 4) }},
		{"marker injection", func(g *Gateway, r *Request) { r.Body = operationMarker + ` {"operation_id":"other"}` }},
		{"invalid unicode", func(g *Gateway, r *Request) { r.Title = string([]byte{0xff}) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newWriteFixture(t)
			g := f.gateway(t, nil)
			r := writeRequest(CreateIssue)
			test.change(g, &r)
			if _, err := f.applyFirst(t, g, r); err == nil {
				t.Fatal("unscoped write accepted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.posts) != 0 {
				t.Fatal("denied request mutated GitHub")
			}
		})
	}
}

func TestChangedRequestAndDeletedIntentDoNotCreateAgain(t *testing.T) {
	f := newWriteFixture(t)
	g := f.gateway(t, nil)
	request := writeRequest(CreateIssue)
	if _, err := f.applyFirst(t, g, request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Title = "changed goal"
	if _, err := g.Apply(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same op reused for new request: %v", err)
	}
	f.mu.Lock()
	f.comments[2] = nil
	before := f.posts["/issues"]
	f.mu.Unlock()
	if _, err := g.Apply(context.Background(), request); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted intent ignored: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts["/issues"] != before {
		t.Fatal("recreated an orphaned operation")
	}
}

func TestIntentTimeoutStopsBeforeEffectAndSurvivesRestart(t *testing.T) {
	f := newWriteFixture(t)
	base := f.server.Client().Transport
	var attempts atomic.Int64
	transport := testServerFixtureTransport(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/2/comments") {
			attempts.Add(1)
			if resp != nil {
				resp.Body.Close()
			}
			return nil, context.DeadlineExceeded
		}
		return resp, err
	})
	g := f.gateway(t, transport)
	request := writeRequest(CreateIssue)
	result, err := f.applyFirst(t, g, request)
	if !errors.Is(err, ErrUncertain) || result.Intent == nil {
		t.Fatalf("intent timeout not reconciled: %#v %v", result, err)
	}
	g = f.gateway(t, nil)
	if _, err := g.Apply(context.Background(), request); !errors.Is(err, ErrUncertain) {
		t.Fatalf("restart redispatched uncertain intent: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if attempts.Load() != 1 || f.posts["/issues"] != 0 {
		t.Fatal("effect sent after uncertain intent publication")
	}
}

func TestReadOnlyReconciliationNeverCreatesMissingObjects(t *testing.T) {
	f := newWriteFixture(t)
	g := f.gateway(t, nil)
	if _, err := g.Reconcile(context.Background(), writeRequest(CreateIssue)); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.posts) != 0 {
		t.Fatal("read-only reconciliation sent a write")
	}
}

func TestRemoteOperationAuthorAndContentDriftBlock(t *testing.T) {
	for _, mode := range []string{"intent author", "object author", "object body", "milestone membership"} {
		t.Run(mode, func(t *testing.T) {
			f := newWriteFixture(t)
			g := f.gateway(t, nil)
			request := writeRequest(CreateIssue)
			result, err := f.applyFirst(t, g, request)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			other := testServerFixtureAuthor()
			other.ID = 90
			other.NodeID = "U_90"
			switch mode {
			case "intent author":
				for _, comment := range f.comments[2] {
					if strings.Contains(comment["body"].(string), operationMarker) {
						comment["user"] = other
					}
				}
			case "object author":
				f.issues[result.Issue.Number]["user"] = other
			case "object body":
				f.issues[result.Issue.Number]["body"] = "deleted operation marker"
			case "milestone membership":
				f.issues[result.Issue.Number]["milestone"] = nil
			}
			before := f.posts["/issues"]
			f.mu.Unlock()
			_, err = g.Apply(context.Background(), request)
			if err == nil {
				t.Fatal("remote drift accepted")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.posts["/issues"] != before {
				t.Fatal("drift caused duplicate creation")
			}
		})
	}
}

func TestAuthorizerCannotMutatePreparedResourceScope(t *testing.T) {
	f := newWriteFixture(t)
	g := f.gateway(t, nil)
	g.authorize = func(_ context.Context, intent Intent, _ protocol.GitHubIdentity) error {
		intent.Related.Number = 999
		return nil
	}
	result, err := f.applyFirst(t, g, writeRequest(CreateIssue))
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Milestone == nil || result.Issue.Milestone.Number != 1 {
		t.Fatal("authorizer mutated prepared write scope")
	}
}

func TestOneWriteOperationPinsCredentialIdentity(t *testing.T) {
	f := newWriteFixture(t)
	g := f.gateway(t, nil)
	request := writeRequest(CreateIssue)
	admission, err := g.AdmitFirstCreate(context.Background(), request, f.freshAllocation(t))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	g.token = func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "synthetic-test-bearer", nil
		}
		return "different-account-token", nil
	}
	if _, err := g.ApplyFirst(context.Background(), request, admission); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("publisher credential changed after native identity check: %d provider calls", calls)
	}
}
