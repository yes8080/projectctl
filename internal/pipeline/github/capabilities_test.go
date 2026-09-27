package github

import (
	"context"
	"net/http"
	"testing"
)

func TestCapabilitiesSeparateFalseMissingAndUnknown(t *testing.T) {
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/octo/pipeline" {
			testServerFixtureJSON(t, w, map[string]any{"id": 99, "node_id": "R_99", "full_name": "octo/pipeline", "has_issues": false, "delete_branch_on_merge": true, "permissions": map[string]any{"push": true, "admin": false}})
			return
		}
		testServerFixtureJSON(t, w, []any{})
	})
	c := g.ProbeCapabilities(context.Background(), 4)
	for _, v := range []Capability{c.IssuesRead, c.CommentsRead, c.MilestonesRead, c.DependenciesRead, c.DeleteBranchOnMergeEnabled, c.PushPermission} {
		if !v.IsSupported() {
			t.Fatalf("positive observation missing: %#v", v)
		}
	}
	for _, v := range []Capability{c.IssuesEnabled, c.AdminPermission} {
		if v.State != Unsupported {
			t.Fatalf("explicit false not distinguished: %#v", v)
		}
	}
	for _, v := range []Capability{c.AutoMergeEnabled, c.IssueAutoCloseEnabled, c.ProtectedMergeGate} {
		if v.State != Unknown || v.IsSupported() {
			t.Fatalf("missing capability assumed supported: %#v", v)
		}
	}
	if (Capability{}).IsSupported() {
		t.Fatal("zero capability is supported")
	}
}

func TestCapabilityFailuresNeverBecomeUnsupportedOrSupported(t *testing.T) {
	for _, status := range []int{401, 403, 404, 410, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private error body", status) })
			c := g.ProbeCapabilities(context.Background(), 4)
			for _, v := range []Capability{c.IssuesRead, c.CommentsRead, c.MilestonesRead, c.DependenciesRead, c.IssuesEnabled, c.DeleteBranchOnMergeEnabled, c.AutoMergeEnabled, c.PushPermission, c.AdminPermission} {
				if v.State != Unknown || v.IsSupported() {
					t.Fatalf("HTTP %d became capability claim: %#v", status, v)
				}
			}
		})
	}
}

func TestDependencyCapabilityRequiresBothDirections(t *testing.T) {
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/octo/pipeline/issues/4/dependencies/blocking" {
			http.NotFound(w, r)
			return
		}
		testServerFixtureJSON(t, w, []any{})
	})
	if got := g.ProbeCapabilities(context.Background(), 4).DependenciesRead; got.State != Unknown {
		t.Fatalf("partial dependency access reported supported: %#v", got)
	}
}
