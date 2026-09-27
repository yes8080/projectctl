package startup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func hasReason(s State, code, subject string) bool {
	for _, r := range s.Reasons {
		if r.Code == code && r.Subject == subject {
			return true
		}
	}
	return false
}

func TestReconstructOnlyFromRemoteFacts(t *testing.T) {
	f, anchor, _, before := fixtureInitialized(t)
	if before.Status != "ready_for_design" || before.Phase != "design" || before.Control == nil || len(before.Inputs) != 1 {
		t.Fatalf("unexpected state: %+v", before)
	}
	if before.Inputs[0].Repository != anchor.Repository || before.Inputs[0].Source != fixtureManifest().Inputs[0].Source || before.PolicySHA256 == "" {
		t.Fatal("missing fixed source/policy identity")
	}
	reads := f.reads
	// Discard every Engine and result. There is no local ledger or cache to load.
	after, err := fixtureEngine(t, f).Reconcile(context.Background(), anchor)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("restart differs: before=%+v after=%+v err=%v", before, after, err)
	}
	if f.creates != 1 || f.reads <= reads {
		t.Fatal("restart did not re-read exclusively remote facts")
	}
	// Caller mutations of disposable results cannot authorize or poison a read.
	after.Inputs[0].Source.Commit = "main"
	after.GitHubCapabilities["issues_read"] = gh.Capability{}
	*after.Control = gh.Resource{}
	next, err := fixtureEngine(t, f).Reconcile(context.Background(), anchor)
	if err != nil || !reflect.DeepEqual(before, next) {
		t.Fatal("result is an authority/cache")
	}
}

func TestControlDiscoveryAndRemoteDrift(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(*fakeRemote, Anchor)
	}{
		{"duplicates", "duplicate_active_control_issues", func(f *fakeRemote, _ Anchor) {
			other := f.issues[0]
			other.Number++
			other.DatabaseID++
			other.NodeID += "other"
			f.issues = append(f.issues, other)
		}},
		{"same native object repeated", "control_native_identity_conflict", func(f *fakeRemote, _ Anchor) { f.issues = append(f.issues, f.issues[0]) }},
		{"wrong author", "control_record_drift", func(f *fakeRemote, _ Anchor) { f.issues[0].Author = fixtureIdentity(99) }},
		{"wrong URL", "control_record_drift", func(f *fakeRemote, _ Anchor) { f.issues[0].URL = "https://github.com/elsewhere/repo/issues/10" }},
		{"edited body", "control_record_drift", func(f *fakeRemote, _ Anchor) {
			body := *f.issues[0].Content.Body + " "
			f.issues[0].Content.Body = &body
			f.issues[0].Content.BodySHA256 = protocol.SHA256([]byte(body))
		}},
		{"body digest drift", "control_record_drift", func(f *fakeRemote, _ Anchor) { f.issues[0].Content.BodySHA256 = strings.Repeat("c", 64) }},
		{"malformed marker", "control_record_drift", func(f *fakeRemote, _ Anchor) { body := ControlMarker + " {}"; f.issues[0].Content.Body = &body }},
		{"foreign cycle", "control_record_drift", func(f *fakeRemote, _ Anchor) {
			body := strings.ReplaceAll(*f.issues[0].Content.Body, "delivery-1", "delivery-2")
			f.issues[0].Content.Body = &body
			f.issues[0].Content.BodySHA256 = protocol.SHA256([]byte(body))
		}},
		{"PR masquerade", "control_native_identity_conflict", func(f *fakeRemote, _ Anchor) { f.issues[0].Kind = "pull_request" }},
		{"closed cycle", "cycle_already_closed", func(f *fakeRemote, _ Anchor) { f.issues[0].State = "closed" }},
		{"unknown state", "control_state_unknown", func(f *fakeRemote, _ Anchor) { f.issues[0].State = "unknown" }},
		{"repository recreated", "repository_identity_drift", func(f *fakeRemote, _ Anchor) { f.repo.DatabaseID++ }},
		{"publisher changed", "controller_identity_unverified", func(f *fakeRemote, _ Anchor) { f.user = fixtureIdentity(42) }},
		{"manifest missing", "manifest_unavailable_or_drifted", func(f *fakeRemote, a Anchor) { delete(f.sources, a.Manifest) }},
		{"input missing", "input_unavailable_or_drifted", func(f *fakeRemote, _ Anchor) { delete(f.sources, fixtureManifest().Inputs[0].Source) }},
		{"input changed", "input_unavailable_or_drifted", func(f *fakeRemote, _ Anchor) {
			ref := fixtureManifest().Inputs[0].Source
			v := f.sources[ref]
			v.Bytes = []byte("edited")
			f.sources[ref] = v
		}},
		{"partial listing failure", "control_discovery_unavailable", func(f *fakeRemote, _ Anchor) { f.listError = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, anchor, _, _ := fixtureInitialized(t)
			tc.change(f, anchor)
			s, err := fixtureEngine(t, f).Reconcile(context.Background(), anchor)
			if err == nil || s.Status != "blocked" {
				t.Fatalf("drift admitted: %+v %v", s, err)
			}
			found := false
			for _, r := range s.Reasons {
				found = found || r.Code == tc.reason
			}
			if !found {
				t.Fatalf("missing %s in %+v", tc.reason, s.Reasons)
			}
			if f.creates != 1 {
				t.Fatal("read-only reconcile created an Issue")
			}
		})
	}
}

func TestMissingControlNeverGuessesOrCreates(t *testing.T) {
	f, anchor := fixtureRemote(t)
	body := "ordinary issue, no control marker"
	f.issues = []gh.Issue{{Content: gh.Content{Body: &body}}}
	s, err := fixtureEngine(t, f).Reconcile(context.Background(), anchor)
	if err != nil || s.Status != "bootstrap_required" || s.Control != nil || f.creates != 0 {
		t.Fatalf("%+v %v", s, err)
	}
	f.hide = true
	s, err = fixtureEngine(t, f).Initialize(context.Background(), anchor, BootstrapAdmission{})
	if !errors.Is(err, ErrUncertain) || s.Status != "uncertain" || f.creates != 0 {
		t.Fatalf("cold start guessed creation: %+v %v", s, err)
	}
}

func TestRequiredCapabilitiesAreTriState(t *testing.T) {
	for _, state := range []gh.CapabilityState{gh.Unknown, gh.Unsupported, ""} {
		t.Run("github_"+string(state), func(t *testing.T) {
			f, anchor := fixtureRemote(t)
			f.caps.DependenciesRead = gh.Capability{State: state, Reason: "secret-token"}
			s, err := fixtureEngine(t, f).Reconcile(context.Background(), anchor)
			if !errors.Is(err, ErrBlocked) || s.Status != "blocked" || f.creates != 0 {
				t.Fatalf("%+v %v", s, err)
			}
			if strings.Contains(s.GitHubCapabilities["dependencies_read"].Reason, "secret") {
				t.Fatal("raw adapter diagnostics escaped")
			}
		})
	}
	for _, name := range hostRequirements {
		for _, state := range []gh.CapabilityState{gh.Unknown, gh.Unsupported} {
			t.Run(name+"_"+string(state), func(t *testing.T) {
				f, anchor := fixtureRemote(t)
				f.host[name] = gh.Capability{State: state}
				s, err := fixtureEngine(t, f).Reconcile(context.Background(), anchor)
				if !errors.Is(err, ErrBlocked) || !hasReason(s, "host_capability_"+string(state), name) {
					t.Fatalf("%+v %v", s, err)
				}
			})
		}
	}
	t.Run("optional unknown is not silently supported", func(t *testing.T) {
		f, anchor := fixtureRemote(t)
		f.caps.AdminPermission = gh.Capability{}
		s, err := fixtureEngine(t, f).Reconcile(context.Background(), anchor)
		if err != nil || s.GitHubCapabilities["admin_permission"].State != gh.Unknown {
			t.Fatalf("%+v %v", s, err)
		}
	})
	t.Run("nil host blocks", func(t *testing.T) {
		f, anchor := fixtureRemote(t)
		e, _ := New(f, nil, f)
		s, err := e.Reconcile(context.Background(), anchor)
		if !errors.Is(err, ErrBlocked) || len(s.Reasons) != len(hostRequirements) {
			t.Fatalf("%+v %v", s, err)
		}
	})
}

func TestOnlyExplicitDesignArtifactsBeforeApproval(t *testing.T) {
	f, anchor, e, _ := fixtureInitialized(t)
	for _, tc := range []struct {
		artifact Artifact
		allowed  bool
	}{
		{Artifact{protocol.RoleDesigner, "design_document", "docs/design.md"}, true},
		{Artifact{protocol.RoleDesignReviewer, "input_document", "docs/input.md"}, true},
		{Artifact{protocol.RoleDesigner, "startup_document", "docs/input.md"}, true},
		{Artifact{protocol.RoleDeveloper, "design_document", "docs/design.md"}, false},
		{Artifact{protocol.RoleAcceptor, "design_document", "docs/design.md"}, false},
		{Artifact{protocol.RolePlanner, "design_document", "docs/design.md"}, false},
		{Artifact{protocol.RoleDesigner, "product_code", "docs/design.md"}, false},
		{Artifact{protocol.RoleDesigner, "design_document", "cmd/main.go"}, false},
		{Artifact{protocol.RoleDesigner, "design_document", "docs/../docs/design.md"}, false},
	} {
		s, err := e.CheckProposal(context.Background(), anchor, tc.artifact)
		if (err == nil) != tc.allowed {
			t.Fatalf("artifact=%+v state=%+v err=%v", tc.artifact, s, err)
		}
	}
	if f.creates != 1 {
		t.Fatal("proposal check mutated remote")
	}
	f.caps.PushPermission = gh.Capability{State: gh.Unknown}
	if _, err := e.CheckProposal(context.Background(), anchor, Artifact{protocol.RoleDesigner, "design_document", "docs/design.md"}); err == nil {
		t.Fatal("proposal used cached permission")
	}
}

func TestInvalidAnchor(t *testing.T) {
	for _, mutate := range []func(*Anchor){func(a *Anchor) { a.Manifest.Commit = "main" }, func(a *Anchor) { a.Manifest.Path = "../secret" }, func(a *Anchor) { a.Repository.DatabaseID = 0 }} {
		f, a := fixtureRemote(t)
		mutate(&a)
		s, err := fixtureEngine(t, f).Reconcile(context.Background(), a)
		if !errors.Is(err, ErrBlocked) || !hasReason(s, "invalid_anchor", "manifest") || f.reads != 0 {
			t.Fatalf("%+v %v", s, err)
		}
	}
	if _, err := New(nil, nil, nil); !errors.Is(err, ErrBlocked) {
		t.Fatal("nil remote accepted")
	}
}
