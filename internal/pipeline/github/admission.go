package github

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

// FreshAllocationSource is a trusted Controller boundary, NOT an Agent input
// or an idempotent permission check. It must consume a newly allocated GitHub
// publisher-run event for this exact Intent. Recovered/uncertain generations
// must return an error, not replay a saved record or infer novelty from lists.
//
// The returned verified Run must bind InputSHA256 to Intent.RequestSHA256. The
// Controller owns policy authorization and allocation uniqueness across process
// restarts. This Gateway does not create run generations or distributed locks.
type FreshAllocationSource func(context.Context, Intent, protocol.GitHubIdentity) (protocol.VerifiedRecord, error)

// FirstCreateAdmission is ephemeral authority, not an operation fact/cache.
// Copying it shares the same one-shot state; zero values and JSON are unusable.
type FirstCreateAdmission struct{ state *firstCreateState }

type firstCreateState struct {
	used      atomic.Bool
	project   protocol.Project
	intent    string
	publisher protocol.GitHubIdentity
	run       protocol.Reference
}

func (FirstCreateAdmission) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("first-create authority cannot be persisted")
}

func (*FirstCreateAdmission) UnmarshalJSON([]byte) error {
	return fmt.Errorf("first-create authority cannot be restored from JSON")
}

// AdmitFirstCreate is an explicit new-allocation action, not part of Apply,
// Reconcile, New, or cold-start recovery. Merely possessing an old GitHub run
// reference is insufficient: source must deliver a fresh, once-only event.
func (g *Gateway) AdmitFirstCreate(ctx context.Context, request Request, source FreshAllocationSource) (FirstCreateAdmission, error) {
	var zero FirstCreateAdmission
	if source == nil {
		return zero, ErrDenied
	}
	ctx, err := g.bindCredential(ctx)
	if err != nil {
		return zero, err
	}
	p, err := g.prepare(ctx, request)
	if err != nil {
		return zero, err
	}
	input := p.intent
	if input.Related != nil {
		copy := *input.Related
		input.Related = &copy
	}
	allocated, err := source(ctx, input, p.actor)
	if err != nil {
		return zero, ErrDenied
	}
	// Re-read the exact native object rather than trusting serialized claims or
	// retaining a cached authority record. Losing this call burns the allocation.
	current, err := g.FetchRecord(ctx, allocated.Reference())
	if err != nil {
		return zero, err
	}
	record, err := current.Record()
	if err != nil {
		return zero, err
	}
	run, ok := record.(*protocol.Run)
	if !ok || run.Project != g.project || run.Role != protocol.RolePublisher || run.InputSHA256 != p.intent.RequestSHA256 || run.AssignmentGeneration <= 0 || run.Attempt <= 0 || !sameIdentity(current.Reference().Author, p.actor) {
		return zero, fmt.Errorf("%w: allocation must be an exact publisher run generation", ErrDenied)
	}
	return FirstCreateAdmission{state: &firstCreateState{project: g.project, intent: p.intentBody, publisher: p.actor, run: current.Reference()}}, nil
}

func (a FirstCreateAdmission) matches(g *Gateway, p prepared) error {
	if a.state == nil {
		return ErrUncertain
	}
	if a.state.project != g.project || a.state.intent != p.intentBody || !sameIdentity(a.state.publisher, p.actor) {
		return fmt.Errorf("%w: first-create admission scope mismatch", ErrDenied)
	}
	return nil
}

func (a FirstCreateAdmission) claim() bool {
	// Burn on entry, including when inspection finds an old/uncertain intent.
	// Otherwise that still-unused permit could retry once the old intent hides.
	return a.state.used.CompareAndSwap(false, true)
}

func (a FirstCreateAdmission) verify(ctx context.Context, g *Gateway) error {
	if _, err := g.FetchRecord(ctx, a.state.run); err != nil {
		return err
	}
	return ctx.Err()
}
