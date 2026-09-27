package startup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func fixtureIdentity(id int64) protocol.GitHubIdentity {
	return protocol.GitHubIdentity{ID: id, NodeID: fmt.Sprintf("U_%d", id), Login: fmt.Sprintf("user-%d", id), Type: "User"}
}
func fixtureRepo() Repository {
	return Repository{Owner: "octo", Name: "pipeline", DatabaseID: 100, NodeID: "R_100"}
}
func fixtureSource(path string, data []byte) protocol.SourceReference {
	return protocol.SourceReference{Commit: strings.Repeat("a", 40), Path: path, SHA256: protocol.SHA256(data)}
}
func fixtureManifest() Manifest {
	return Manifest{Schema: ManifestSchema, Version: 1, Repository: fixtureRepo(), Cycle: "delivery-1", OperationID: "startup-delivery-1", Inputs: []Input{{Kind: "product", Source: fixtureSource("docs/product.md", []byte("product requirements"))}}, Policy: Policy{
		Goal: "deliver bounded product", NonGoals: []string{"scope expansion"}, StoreInputsAuthorized: true, RemoteWritesAuthorized: true,
		Roles:  Roles{Controller: fixtureIdentity(1), Designer: fixtureIdentity(2), DesignReviewer: fixtureIdentity(3), Developer: fixtureIdentity(4), Acceptor: fixtureIdentity(5)},
		Budget: protocol.Budget{MaxAgentRuns: 20, MaxRunSeconds: 60, MaxWallSeconds: 600, MaxDesignRounds: 3, MaxPlanningRounds: 3, MaxImplementationRounds: 3, MaxDefectFixIssues: 0, MaxChangeRequests: 0, MaxCostMinorUnits: 0, Currency: "USD"}, MaxParallelDevelopment: 1, MaxParallelMerge: 1,
		Completion:                 Completion{Endpoint: "integrated_code", Criteria: []string{"integration succeeds at fixed revision"}, RequireIntegration: true, RequireBugGate: true, RequireRemoteCleanup: true, RequireLocalCleanup: true},
		TerminalConditions:         []string{"completed", "blocked", "budget_exceeded", "failed", "paused", "cancelled"},
		Environment:                Environment{OperatingSystems: []string{"linux", "macos", "windows"}, Runtime: "bounded-runtime", AcceptanceTarget: "test-environment", WorkspaceIsolation: "isolated", CredentialIsolation: "controller_only", ExternalUsageLimit: "account-hard-limit"},
		RequiredGitHubCapabilities: []string{"issues_read", "issues_enabled", "push_permission", "comments_read", "milestones_read", "dependencies_read", "delete_branch_on_merge", "issue_auto_close", "protected_merge_gate"},
		DesignFiles:                []string{"docs/design.md", "docs/input.md"},
	}}
}

// This fake is the remote GitHub/Git/host boundary. Reconstructing Engine does
// not restore client state; it must read these independently retained facts.
type fakeRemote struct {
	mu        sync.Mutex
	repo      gh.Repository
	sources   map[protocol.SourceReference]gh.SourceBlob
	issues    []gh.Issue
	user      protocol.GitHubIdentity
	caps      gh.Capabilities
	host      map[string]gh.Capability
	creates   int
	reads     int
	save      bool
	timeout   bool
	hide      bool
	listError bool
}

func fixtureRemote(t *testing.T) (*fakeRemote, Anchor) {
	t.Helper()
	m := fixtureManifest()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	a := Anchor{Repository: m.Repository, Manifest: fixtureSource("docs/startup.json", data)}
	yes := gh.Capability{State: gh.Supported}
	f := &fakeRemote{repo: gh.Repository{Resource: gh.Resource{Kind: "repository", DatabaseID: m.Repository.DatabaseID, NodeID: m.Repository.NodeID}, URL: "https://github.com/octo/pipeline", DefaultBranch: "main"}, sources: map[protocol.SourceReference]gh.SourceBlob{}, issues: []gh.Issue{}, user: m.Policy.Roles.Controller, save: true, host: map[string]gh.Capability{}}
	f.caps = gh.Capabilities{IssuesRead: yes, CommentsRead: yes, MilestonesRead: yes, DependenciesRead: yes, IssuesEnabled: yes, DeleteBranchOnMergeEnabled: yes, AutoMergeEnabled: yes, PushPermission: yes, AdminPermission: yes, IssueAutoCloseEnabled: yes, ProtectedMergeGate: yes}
	f.addSource(a.Manifest, data)
	f.addSource(m.Inputs[0].Source, []byte("product requirements"))
	for _, name := range hostRequirements {
		f.host[name] = yes
	}
	return f, a
}
func (f *fakeRemote) addSource(ref protocol.SourceReference, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sources[ref] = gh.SourceBlob{Reference: ref, BlobSHA: strings.Repeat("b", 40), Bytes: append([]byte(nil), data...)}
}
func (f *fakeRemote) ReadRepository(context.Context) (gh.Repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.repo, nil
}
func (f *fakeRemote) ReadSource(_ context.Context, ref protocol.SourceReference) (gh.SourceBlob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	v, ok := f.sources[ref]
	if !ok {
		return gh.SourceBlob{}, errors.New("missing remote blob")
	}
	v.Bytes = append([]byte(nil), v.Bytes...)
	return v, nil
}
func (f *fakeRemote) ListIssues(context.Context) ([]gh.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listError {
		return nil, errors.New("remote unavailable")
	}
	if f.hide {
		return []gh.Issue{}, nil
	}
	return append([]gh.Issue(nil), f.issues...), nil
}
func (f *fakeRemote) CurrentUser(context.Context) (protocol.GitHubIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.user, nil
}
func (f *fakeRemote) ProbeCapabilities(context.Context, int64) gh.Capabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caps
}
func (f *fakeRemote) Probe(context.Context, HostRequest) map[string]gh.Capability {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]gh.Capability{}
	for k, v := range f.host {
		out[k] = v
	}
	return out
}
func (f *fakeRemote) CreateControl(_ context.Context, r BootstrapRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.save {
		n := int64(10 + len(f.issues))
		body := r.Body
		f.issues = append(f.issues, gh.Issue{Resource: gh.Resource{Kind: "issue", Number: n, DatabaseID: 1000 + n, NodeID: fmt.Sprintf("I_%d", n)}, URL: fmt.Sprintf("https://github.com/%s/%s/issues/%d", r.Repository.Owner, r.Repository.Name, n), Title: r.Title, State: "open", Author: r.Publisher, Content: gh.Content{Body: &body, BodySHA256: protocol.SHA256([]byte(body))}})
	}
	if f.timeout {
		return context.DeadlineExceeded
	}
	return nil
}
func (f *fakeRemote) freshSource(t *testing.T) FreshBootstrapSource {
	t.Helper()
	var used atomic.Bool
	return func(_ context.Context, r BootstrapRequest) (protocol.SourceReference, error) {
		if !used.CompareAndSwap(false, true) {
			return protocol.SourceReference{}, ErrBlocked
		}
		authorization := BootstrapAuthorization{Schema: "projectctl.bootstrap-authorization/v1", Kind: "bootstrap_authorization", Repository: r.Repository, OperationID: r.OperationID, BodySHA256: r.BodySHA256, Publisher: r.Publisher, Generation: 1, Decision: "APPROVED"}
		body, err := json.Marshal(authorization)
		if err != nil {
			t.Fatal(err)
		}
		ref := fixtureSource("docs/bootstrap-authorization.json", body)
		f.addSource(ref, body)
		return ref, nil
	}
}
func fixtureEngine(t *testing.T, f *fakeRemote) *Engine {
	t.Helper()
	e, err := New(f, f, f)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func fixtureInitialized(t *testing.T) (*fakeRemote, Anchor, *Engine, State) {
	t.Helper()
	f, a := fixtureRemote(t)
	e := fixtureEngine(t, f)
	admission, err := e.AdmitBootstrap(context.Background(), a, f.freshSource(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.Initialize(context.Background(), a, admission)
	if err != nil {
		t.Fatal(err)
	}
	return f, a, e, s
}
