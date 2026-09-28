package planner

import (
	"context"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/design"
	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

// Inputs is a freshly verified, disposable view; it carries no dispatch or
// publication authority. InspectInputs uses the identical input identity
// algorithm as Admit/Execute so consumers cannot substitute a legitimate old Run.
type Inputs struct {
	Design   design.Document
	Manifest startup.Manifest
	SHA256   string
}

func InspectInputs(ctx context.Context, remote Remote, request Request) (Inputs, error) {
	e := &Engine{remote: remote, now: time.Now}
	p, err := e.prepare(ctx, request)
	if err != nil {
		return Inputs{}, err
	}
	return Inputs{Design: p.document, Manifest: p.manifest, SHA256: p.digest}, nil
}

type historicalRemote struct {
	Remote
	request Request
}

func (r historicalRemote) ReadIssue(ctx context.Context, number int64) (gh.Issue, error) {
	issue, err := r.Remote.ReadIssue(ctx, number)
	if err != nil {
		return issue, err
	}
	// Only the completed planning task is historical. Closing the project
	// control Issue still ends the cycle and must remain a blocking observation.
	if number == r.request.Issue.Number && number != r.request.Project.ControlIssue {
		if issue.State != "open" && issue.State != "closed" {
			return gh.Issue{}, ErrBlocked
		}
		issue.State = "open"
	}
	return issue, nil
}

func (r historicalRemote) ReadBranch(ctx context.Context, name string) (gh.Commit, error) {
	if name != r.request.TargetBranch {
		return gh.Commit{}, ErrBlocked
	}
	commit, err := r.Remote.ReadCommit(ctx, r.request.ExpectedTargetSHA)
	if err != nil || commit.SHA != r.request.ExpectedTargetSHA {
		return gh.Commit{}, ErrBlocked
	}
	return commit, nil
}

// ReplayInputs reconstructs the original Run identity after normal later
// merges. Only the planning branch observation is replaced by its exact Git
// commit object. It does not prove current-head ancestry or authorize execution.
func ReplayInputs(ctx context.Context, remote Remote, request Request) (Inputs, error) {
	if remote == nil {
		return Inputs{}, ErrBlocked
	}
	return InspectInputs(ctx, historicalRemote{Remote: remote, request: request}, request)
}
