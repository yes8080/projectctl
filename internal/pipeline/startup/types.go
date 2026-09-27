// Package startup validates a bounded project from immutable GitHub inputs.
// It starts no Agents and keeps no persistent or authoritative local state.
package startup

import (
	"context"
	"errors"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

const (
	ManifestSchema       = "projectctl.startup/v1"
	ControlSchema        = "projectctl.control/v1"
	ControlMarker        = "<!-- projectctl:control:v1 -->"
	BootstrapIntegration = "REQUIRED_NOT_IMPLEMENTED_FOR_LIVE"
)

var (
	ErrBlocked   = errors.New("startup is blocked")
	ErrUncertain = errors.New("bootstrap outcome is uncertain; reconcile only")
	ErrConflict  = errors.New("conflicting startup facts")
)

type Repository struct {
	Owner      string `json:"owner"`
	Name       string `json:"name"`
	DatabaseID int64  `json:"database_id"`
	NodeID     string `json:"node_id"`
}

// Anchor is a trusted Controller-selected GitHub root reference, not a cached
// policy or a record selected from an unapproved Issue body/latest comment.
type Anchor struct {
	Repository Repository               `json:"repository"`
	Manifest   protocol.SourceReference `json:"manifest"`
}

type Input struct {
	Kind   string                   `json:"kind"`
	Source protocol.SourceReference `json:"source"`
}

type Manifest struct {
	Schema      string     `json:"schema"`
	Version     int64      `json:"version"`
	Repository  Repository `json:"repository"`
	Cycle       string     `json:"cycle"`
	OperationID string     `json:"operation_id"`
	Inputs      []Input    `json:"inputs"`
	Policy      Policy     `json:"policy"`
}

type Policy struct {
	Goal                       string          `json:"goal"`
	NonGoals                   []string        `json:"non_goals"`
	StoreInputsAuthorized      bool            `json:"store_inputs_authorized"`
	RemoteWritesAuthorized     bool            `json:"remote_writes_authorized"`
	Roles                      Roles           `json:"roles"`
	Budget                     protocol.Budget `json:"budget"`
	MaxParallelDevelopment     int64           `json:"max_parallel_development"`
	MaxParallelMerge           int64           `json:"max_parallel_merge"`
	Completion                 Completion      `json:"completion"`
	TerminalConditions         []string        `json:"terminal_conditions"`
	Environment                Environment     `json:"environment"`
	RequiredGitHubCapabilities []string        `json:"required_github_capabilities"`
	DesignFiles                []string        `json:"design_files"`
}

type Roles struct {
	Controller     protocol.GitHubIdentity `json:"controller"`
	Designer       protocol.GitHubIdentity `json:"designer"`
	DesignReviewer protocol.GitHubIdentity `json:"design_reviewer"`
	Developer      protocol.GitHubIdentity `json:"developer"`
	Acceptor       protocol.GitHubIdentity `json:"acceptor"`
}

type Completion struct {
	Endpoint             string      `json:"endpoint"` // integrated_code or deployed_and_verified
	Criteria             []string    `json:"criteria"`
	RequireIntegration   bool        `json:"require_integration"`
	RequireBugGate       bool        `json:"require_bug_gate"`
	RequireRemoteCleanup bool        `json:"require_remote_cleanup"`
	RequireLocalCleanup  bool        `json:"require_local_cleanup"`
	Deployment           *Deployment `json:"deployment,omitempty"`
}

type Deployment struct {
	Authorized     bool   `json:"authorized"`
	Target         string `json:"target"`
	RollbackPolicy string `json:"rollback_policy"`
}

type Environment struct {
	OperatingSystems    []string `json:"operating_systems"`
	Runtime             string   `json:"runtime"`
	AcceptanceTarget    string   `json:"acceptance_target"`
	WorkspaceIsolation  string   `json:"workspace_isolation"`
	CredentialIsolation string   `json:"credential_isolation"`
	ExternalUsageLimit  string   `json:"external_usage_limit"`
}

type ControlRecord struct {
	Schema      string                   `json:"schema"`
	Kind        string                   `json:"kind"`
	Repository  Repository               `json:"repository"`
	Cycle       string                   `json:"cycle"`
	OperationID string                   `json:"operation_id"`
	Manifest    protocol.SourceReference `json:"manifest"`
}

// Remote is the authenticated, repository-bound Gateway trust boundary. Its
// read methods must return complete native observations, never Agent payloads.
type Remote interface {
	ReadRepository(context.Context) (gh.Repository, error)
	ReadSource(context.Context, protocol.SourceReference) (gh.SourceBlob, error)
	ListIssues(context.Context) ([]gh.Issue, error)
	CurrentUser(context.Context) (protocol.GitHubIdentity, error)
	ProbeCapabilities(context.Context, int64) gh.Capabilities
}

// Host probes actual managed-host readiness, not policy declarations. Its
// absence/unknown observations block; startup does not implement an Agent runtime.
type Host interface {
	Probe(context.Context, HostRequest) map[string]gh.Capability
}

type HostRequest struct {
	Environment Environment
	Roles       Roles
	Budget      protocol.Budget
}

type Reason struct {
	Code    string
	Subject string
}
type FixedInput struct {
	Kind       string
	Repository Repository
	Source     protocol.SourceReference
	BlobSHA    string
}
type State struct {
	Status             string
	Phase              string
	Control            *gh.Resource
	ManifestSHA256     string
	PolicySHA256       string
	Inputs             []FixedInput
	Reasons            []Reason
	GitHubCapabilities map[string]gh.Capability
	HostCapabilities   map[string]gh.Capability
	BootstrapAdapter   string
}

// Artifact is a proposal only, not dispatch authority. This package never
// permits Developer/Acceptor/product execution, even after design approval.
type Artifact struct {
	Role protocol.Role
	Kind string
	Path string
}

type Engine struct {
	remote  Remote
	host    Host
	creator BootstrapCreator
}

func New(remote Remote, host Host, creator BootstrapCreator) (*Engine, error) {
	if remote == nil {
		return nil, ErrBlocked
	}
	return &Engine{remote: remote, host: host, creator: creator}, nil
}
