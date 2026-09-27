// Package plan publishes and verifies a candidate through GitHub native objects.
// It never schedules Developers or stores a local authoritative task table.
package plan

import (
	"context"
	"errors"
	"sync/atomic"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/planner"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

var ErrBlocked = errors.New("plan blocked")
var ErrPartial = errors.New("plan publication incomplete; reconcile or explicitly authorize one new operation")

type Gateway interface {
	planner.Remote
	ListIssues(context.Context) ([]gh.Issue, error)
	ListMilestones(context.Context) ([]gh.Milestone, error)
	ReadMilestone(context.Context, int64) (gh.Milestone, error)
	ListDependencies(context.Context, int64, string) ([]gh.Issue, error)
	Reconcile(context.Context, gh.Request) (gh.Outcome, error)
	AdmitFirstCreate(context.Context, gh.Request, gh.FreshAllocationSource) (gh.FirstCreateAdmission, error)
	ApplyFirst(context.Context, gh.Request, gh.FirstCreateAdmission) (gh.Outcome, error)
}

// InputReader is a trusted host test/integration boundary, never Agent JSON.
// New defaults to the production planner InspectInputs implementation.
type InputReader interface {
	Read(context.Context, planner.Request) (planner.Inputs, error)
}
type HistoricalInputReader interface {
	Replay(context.Context, planner.Request) (planner.Inputs, error)
}
type nativeInputs struct{ remote planner.Remote }

func (n nativeInputs) Read(ctx context.Context, r planner.Request) (planner.Inputs, error) {
	return planner.InspectInputs(ctx, n.remote, r)
}
func (n nativeInputs) Replay(ctx context.Context, r planner.Request) (planner.Inputs, error) {
	return planner.ReplayInputs(ctx, n.remote, r)
}

// Request pins an immutable historical proposal and authorization policy. They
// do not override published contracts or native topology after activation.
type Request struct {
	Input                planner.Request
	Candidate            protocol.SourceReference
	PlannerRun           protocol.Reference
	Authorization        protocol.SourceReference
	OperationPrefix      string
	MilestoneTitle       string
	MilestoneDescription string
	// ExistingMilestone is an explicitly selected native fixture/stage. Its exact
	// identity/body/title are pinned; this package never edits existing objects.
	ExistingMilestone *MilestonePin
}
type MilestonePin struct {
	Resource          gh.Resource
	Title, BodySHA256 string
}

type State struct {
	Status        string // partial or candidate; never active without Activate
	Next          *gh.Request
	Plan          *protocol.Plan
	PlanReference *protocol.Reference
	InputSHA256   string
}
type Engine struct {
	remote     Gateway
	inputs     InputReader
	historical bool
}

func New(remote Gateway, inputs InputReader) (*Engine, error) {
	if remote == nil {
		return nil, ErrBlocked
	}
	if inputs == nil {
		inputs = nativeInputs{remote}
	}
	return &Engine{remote: remote, inputs: inputs}, nil
}

type Admission struct{ p *permit }
type permit struct {
	used    atomic.Bool
	request Request
	step    gh.Request
	digest  string
	gateway gh.FirstCreateAdmission
}

func (Admission) MarshalJSON() ([]byte, error) { return nil, ErrBlocked }
func (*Admission) UnmarshalJSON([]byte) error  { return ErrBlocked }

type ReviewRequest struct {
	Publication            Request
	Plan, Review, Approval protocol.Reference
}
type Frozen struct {
	Plan                        protocol.Plan
	Reference, Review, Approval protocol.Reference
	InputSHA256                 string
}

type prepared struct {
	input          planner.Inputs
	candidate      planner.Candidate
	summary        planner.Summary
	policy         protocol.Policy
	plannerRun     protocol.Run
	baseline       protocol.Baseline
	designBaseline protocol.DesignBaseline
	digest         string
	control        gh.Resource
	repository     gh.Resource
}
