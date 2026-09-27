package startup

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func TestBootstrapLostResponseReconcilesWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		save, hide bool
	}{
		{"accepted visible", true, false}, {"accepted temporarily invisible", true, true}, {"unconfirmed absent", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, anchor := fixtureRemote(t)
			f.save, f.hide, f.timeout = tc.save, tc.hide, true
			e := fixtureEngine(t, f)
			admission, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t))
			if err != nil {
				t.Fatal(err)
			}
			s, err := e.Initialize(context.Background(), anchor, admission)
			confirmed := tc.save && !tc.hide
			if confirmed {
				if err != nil || s.Status != "ready_for_design" {
					t.Fatalf("%+v %v", s, err)
				}
			} else if !errors.Is(err, ErrUncertain) || s.Status != "uncertain" {
				t.Fatalf("%+v %v", s, err)
			}
			// A completely new engine has no new first-create authority. Neither a
			// zero admission nor a copied, spent admission may repeat the POST.
			for _, permit := range []BootstrapAdmission{{}, admission} {
				_, err = fixtureEngine(t, f).Initialize(context.Background(), anchor, permit)
				if !confirmed && !errors.Is(err, ErrUncertain) {
					t.Fatal(err)
				}
			}
			if f.creates != 1 {
				t.Fatalf("intent/effect repeated: creates=%d", f.creates)
			}
			if tc.save {
				f.hide = false
				recovered, err := fixtureEngine(t, f).Reconcile(context.Background(), anchor)
				if err != nil || recovered.Status != "ready_for_design" || f.creates != 1 {
					t.Fatalf("%+v %v", recovered, err)
				}
			}
		})
	}
}

func TestBootstrapRequiresLiveAdapterAndExplicitFreshAuthority(t *testing.T) {
	f, anchor := fixtureRemote(t)
	e, _ := New(f, f, nil)
	s, err := e.Initialize(context.Background(), anchor, BootstrapAdmission{})
	if !errors.Is(err, ErrBlocked) || s.BootstrapAdapter != BootstrapIntegration || !hasReason(s, "bootstrap_adapter_required", BootstrapIntegration) || f.creates != 0 {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err = e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t)); !errors.Is(err, ErrBlocked) {
		t.Fatal("absent adapter admitted")
	}
	e = fixtureEngine(t, f)
	if _, err = e.AdmitBootstrap(context.Background(), anchor, nil); !errors.Is(err, ErrBlocked) {
		t.Fatal("nil authority admitted")
	}
	source := f.freshSource(t)
	permit, err := e.AdmitBootstrap(context.Background(), anchor, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixtureEngine(t, f).AdmitBootstrap(context.Background(), anchor, source); !errors.Is(err, ErrBlocked) {
		t.Fatal("fresh source replay admitted")
	}
	if _, err = json.Marshal(permit); err == nil {
		t.Fatal("admission serialized")
	}
	if err = json.Unmarshal([]byte(`{}`), &permit); err == nil {
		t.Fatal("admission deserialized")
	}
	if f.creates != 0 {
		t.Fatal("admission is not read-only")
	}
}

func TestBootstrapAdmissionCopiesShareOneDispatch(t *testing.T) {
	for i := 0; i < 100; i++ {
		f, anchor := fixtureRemote(t)
		f.hide, f.timeout = true, true
		e := fixtureEngine(t, f)
		permit, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func(copy BootstrapAdmission) {
				defer wg.Done()
				s, err := fixtureEngine(t, f).Initialize(context.Background(), anchor, copy)
				if !errors.Is(err, ErrUncertain) || s.Status != "uncertain" {
					t.Errorf("%+v %v", s, err)
				}
			}(permit)
		}
		wg.Wait()
		if f.creates != 1 {
			t.Fatalf("iteration %d: %d writes", i, f.creates)
		}
	}
}

func TestBootstrapExistingIssueConsumesAdmission(t *testing.T) {
	f, anchor := fixtureRemote(t)
	e := fixtureEngine(t, f)
	permit, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t))
	if err != nil {
		t.Fatal(err)
	}
	// The exact operation becomes visible before this holder dispatches.
	if err = f.CreateControl(context.Background(), permit.state.request); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Initialize(context.Background(), anchor, permit); err != nil {
		t.Fatal(err)
	}
	f.hide = true
	if _, err = fixtureEngine(t, f).Initialize(context.Background(), anchor, permit); !errors.Is(err, ErrUncertain) {
		t.Fatal(err)
	}
	if f.creates != 1 {
		t.Fatal("existing result left reusable write authority")
	}
}

func TestBootstrapGrantIsExactAndRevalidated(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*BootstrapAuthorization)
	}{
		{"repository", func(v *BootstrapAuthorization) { v.Repository.DatabaseID++ }},
		{"operation", func(v *BootstrapAuthorization) { v.OperationID += "-other" }},
		{"body digest", func(v *BootstrapAuthorization) { v.BodySHA256 = "wrong" }},
		{"publisher", func(v *BootstrapAuthorization) { v.Publisher = fixtureIdentity(99) }},
		{"generation", func(v *BootstrapAuthorization) { v.Generation = 0 }},
		{"decision", func(v *BootstrapAuthorization) { v.Decision = "REJECTED" }},
		{"schema", func(v *BootstrapAuthorization) { v.Schema = "projectctl.record/v1" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			f, anchor := fixtureRemote(t)
			e := fixtureEngine(t, f)
			source := func(_ context.Context, r BootstrapRequest) (protocol.SourceReference, error) {
				v := BootstrapAuthorization{Schema: "projectctl.bootstrap-authorization/v1", Kind: "bootstrap_authorization", Repository: r.Repository, OperationID: r.OperationID, BodySHA256: r.BodySHA256, Publisher: r.Publisher, Generation: 1, Decision: "APPROVED"}
				tc.change(&v)
				data, _ := json.Marshal(v)
				ref := fixtureSource("docs/grant.json", data)
				f.addSource(ref, data)
				return ref, nil
			}
			if _, err := e.AdmitBootstrap(context.Background(), anchor, source); !errors.Is(err, ErrBlocked) || f.creates != 0 {
				t.Fatalf("grant admitted: %v writes=%d", err, f.creates)
			}
		})
	}
	t.Run("authorization removed before dispatch", func(t *testing.T) {
		f, anchor := fixtureRemote(t)
		e := fixtureEngine(t, f)
		permit, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t))
		if err != nil {
			t.Fatal(err)
		}
		delete(f.sources, permit.state.authorization)
		s, err := e.Initialize(context.Background(), anchor, permit)
		if !errors.Is(err, ErrBlocked) || !hasReason(s, "bootstrap_authorization_unavailable_or_drifted", "control_issue") || f.creates != 0 {
			t.Fatalf("%+v %v", s, err)
		}
		if _, err = e.Initialize(context.Background(), anchor, permit); !errors.Is(err, ErrUncertain) {
			t.Fatal("failed attempt refunded authority")
		}
	})
	t.Run("canceled dispatch is not retried", func(t *testing.T) {
		f, anchor := fixtureRemote(t)
		e := fixtureEngine(t, f)
		permit, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err = e.Initialize(ctx, anchor, permit); !errors.Is(err, ErrUncertain) || f.creates != 0 {
			t.Fatalf("%v writes=%d", err, f.creates)
		}
		if _, err = e.Initialize(context.Background(), anchor, permit); !errors.Is(err, ErrUncertain) || f.creates != 0 {
			t.Fatal("cancellation refunded authority")
		}
	})
}

type bootstrapCreatorFunc func(context.Context, BootstrapRequest) error

func (f bootstrapCreatorFunc) CreateControl(ctx context.Context, r BootstrapRequest) error {
	return f(ctx, r)
}

func TestBootstrapConflictingResultsFailClosed(t *testing.T) {
	f, anchor := fixtureRemote(t)
	creator := bootstrapCreatorFunc(func(ctx context.Context, r BootstrapRequest) error {
		_ = f.CreateControl(ctx, r)
		_ = f.CreateControl(ctx, r)
		return nil
	})
	e, _ := New(f, f, creator)
	permit, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.Initialize(context.Background(), anchor, permit)
	if !errors.Is(err, ErrConflict) || !hasReason(s, "duplicate_active_control_issues", "repository") {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestBootstrapReadFailureAfterDispatchIsUncertain(t *testing.T) {
	f, anchor := fixtureRemote(t)
	creator := bootstrapCreatorFunc(func(ctx context.Context, r BootstrapRequest) error {
		_ = f.CreateControl(ctx, r)
		f.mu.Lock()
		f.listError = true
		f.mu.Unlock()
		return context.DeadlineExceeded
	})
	e, _ := New(f, f, creator)
	permit, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t))
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.Initialize(context.Background(), anchor, permit)
	if !errors.Is(err, ErrUncertain) || s.Status != "uncertain" || f.creates != 1 {
		t.Fatalf("%+v %v creates=%d", s, err, f.creates)
	}
	f.listError = false
	s, err = fixtureEngine(t, f).Initialize(context.Background(), anchor, BootstrapAdmission{})
	if err != nil || s.Status != "ready_for_design" || f.creates != 1 {
		t.Fatalf("%+v %v", s, err)
	}
}
