package startup

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type BootstrapRequest struct {
	Repository  Repository
	Cycle       string
	OperationID string
	Title       string
	Body        string
	BodySHA256  string
	Publisher   protocol.GitHubIdentity
}

// BootstrapCreator is a narrow trusted publisher port, not a live adapter
// implemented by this package. It must create only this exact Issue. Responses
// are never facts: Initialize always re-reads native Issues after one attempt.
type BootstrapCreator interface {
	CreateControl(context.Context, BootstrapRequest) error
}

// FreshBootstrapSource must consume a new Controller-authorized bootstrap event.
// It may not replay an old generation after restart/timeout or infer permission
// from an empty Issue list. Production cross-restart uniqueness is a Controller
// obligation, just as FreshAllocationSource is for the normal Gateway.
type FreshBootstrapSource func(context.Context, BootstrapRequest) (protocol.SourceReference, error)

type BootstrapAuthorization struct {
	Schema      string                  `json:"schema"`
	Kind        string                  `json:"kind"`
	Repository  Repository              `json:"repository"`
	OperationID string                  `json:"operation_id"`
	BodySHA256  string                  `json:"body_sha256"`
	Publisher   protocol.GitHubIdentity `json:"publisher"`
	Generation  int64                   `json:"generation"`
	Decision    string                  `json:"decision"`
}

type BootstrapAdmission struct{ state *bootstrapPermit }
type bootstrapPermit struct {
	used          atomic.Bool
	anchor        Anchor
	request       BootstrapRequest
	authorization protocol.SourceReference
}

func (BootstrapAdmission) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("bootstrap admission is ephemeral and cannot be persisted")
}
func (*BootstrapAdmission) UnmarshalJSON([]byte) error {
	return fmt.Errorf("bootstrap authority cannot be restored from JSON")
}

func (e *Engine) AdmitBootstrap(ctx context.Context, anchor Anchor, source FreshBootstrapSource) (BootstrapAdmission, error) {
	var zero BootstrapAdmission
	s, value, err := e.reconcile(ctx, anchor)
	if err != nil {
		return zero, err
	}
	if s.Status != "bootstrap_required" || source == nil || e.creator == nil {
		return zero, ErrBlocked
	}
	ref, err := source(ctx, value.request)
	if err != nil {
		return zero, ErrBlocked
	}
	if err := e.verifyBootstrapAuthorization(ctx, ref, value.request); err != nil {
		return zero, err
	}
	return BootstrapAdmission{state: &bootstrapPermit{anchor: anchor, request: value.request, authorization: ref}}, nil
}

func (e *Engine) verifyBootstrapAuthorization(ctx context.Context, ref protocol.SourceReference, request BootstrapRequest) error {
	if !validSource(ref) {
		return ErrBlocked
	}
	blob, err := e.remote.ReadSource(ctx, ref)
	if err != nil || !matchesBlob(blob, ref) {
		return ErrBlocked
	}
	var authorization BootstrapAuthorization
	if err := strictDecode(blob.Bytes, &authorization); err != nil {
		return ErrBlocked
	}
	if authorization.Schema != "projectctl.bootstrap-authorization/v1" || authorization.Kind != "bootstrap_authorization" || authorization.Repository != request.Repository || authorization.OperationID != request.OperationID || authorization.BodySHA256 != request.BodySHA256 || !sameIdentity(authorization.Publisher, request.Publisher) || authorization.Generation <= 0 || authorization.Decision != "APPROVED" {
		return ErrBlocked
	}
	return nil
}

func sameBootstrapRequest(a, b BootstrapRequest) bool {
	return a.Repository == b.Repository && a.Cycle == b.Cycle && a.OperationID == b.OperationID && a.Title == b.Title && a.Body == b.Body && a.BodySHA256 == b.BodySHA256 && sameIdentity(a.Publisher, b.Publisher)
}

// Initialize performs select-or-create, never "empty means retry". Default and
// cold-start calls are reconcile-only. A fresh explicit admission allows at most
// one bootstrap port call, consumed before the final read/dispatch. The host must
// serialize independent Controller channels; admissions are not distributed locks.
func (e *Engine) Initialize(ctx context.Context, anchor Anchor, admission BootstrapAdmission) (State, error) {
	// Spend any supplied permit on entry, including an existing-Issue result, so
	// later invisibility cannot resurrect unused first-create permission.
	fresh := false
	if admission.state != nil && admission.state.anchor == anchor {
		fresh = admission.state.used.CompareAndSwap(false, true)
	}
	s, value, err := e.reconcile(ctx, anchor)
	if err != nil {
		return s, err
	}
	if s.Status == "ready_for_design" {
		return s, nil
	}
	if e.creator == nil {
		return block(s, "bootstrap_adapter_required", BootstrapIntegration, ErrBlocked)
	}
	if admission.state == nil || !fresh {
		s.Status = "uncertain"
		s.Reasons = append(s.Reasons, Reason{"bootstrap_admission_required", "control_issue"})
		return s, ErrUncertain
	}
	if admission.state.anchor != anchor || !sameBootstrapRequest(admission.state.request, value.request) {
		return block(s, "bootstrap_authorization_scope_mismatch", "control_issue", ErrBlocked)
	}
	if err := e.verifyBootstrapAuthorization(ctx, admission.state.authorization, value.request); err != nil {
		return block(s, "bootstrap_authorization_unavailable_or_drifted", "control_issue", ErrBlocked)
	}
	if ctx.Err() != nil {
		s.Status = "uncertain"
		return s, ErrUncertain
	}
	_ = e.creator.CreateControl(ctx, value.request)
	// Neither success, timeout, nor a returned object can substitute for actual
	// full discovery and identity/content validation from GitHub.
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	s, _, err = e.reconcile(readCtx, anchor)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return s, err
		}
		s.Status = "uncertain"
		s.Reasons = append(s.Reasons, Reason{"bootstrap_outcome_unconfirmed", "control_issue"})
		return s, ErrUncertain
	}
	if s.Status != "ready_for_design" {
		s.Status = "uncertain"
		s.Reasons = append(s.Reasons, Reason{"bootstrap_outcome_unconfirmed", "control_issue"})
		return s, ErrUncertain
	}
	return s, nil
}
