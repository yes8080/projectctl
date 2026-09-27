package protocol

const (
	Schema = "projectctl.record/v1"
	Marker = "<!-- projectctl:record:v1 -->"
)

type Kind string

const (
	KindBaseline       Kind = "baseline"
	KindPlan           Kind = "plan"
	KindContract       Kind = "contract"
	KindRun            Kind = "run"
	KindEvidence       Kind = "evidence"
	KindAcceptance     Kind = "acceptance"
	KindPlanAcceptance Kind = "plan_acceptance"
	KindDelivery       Kind = "delivery"
	KindApproval       Kind = "approval"
)

// Record is a closed union. Each kind has exact top-level fields; there is no
// arbitrary payload map, actor field, or unknown-field extension bucket.
type Record interface {
	Header() Envelope
	validate() error
}

type Project struct {
	Owner        string `json:"owner"`
	Repo         string `json:"repo"`
	ControlIssue int64  `json:"control_issue"`
}

// Subject identifies exactly one GitHub-native object in Project.
type Subject struct {
	Issue       int64 `json:"issue,omitempty"`
	Milestone   int64 `json:"milestone,omitempty"`
	PullRequest int64 `json:"pull_request,omitempty"`
}

type Envelope struct {
	Schema      string  `json:"schema"`
	Kind        Kind    `json:"kind"`
	Project     Project `json:"project"`
	Subject     Subject `json:"subject"`
	OperationID string  `json:"operation_id"`
	Version     int64   `json:"version"`
}

func (e Envelope) Header() Envelope { return e }

// GitHubIdentity contains native identity facts, not an assertion of authority.
// Login is display metadata; policy grants match stable ID, node ID and type.
type GitHubIdentity struct {
	ID     int64  `json:"id"`
	NodeID string `json:"node_id"`
	Login  string `json:"login"`
	Type   string `json:"type"`
}

// Reference pins one GitHub record object, its author and both raw body and
// canonical record digests. The URL must agree with the stable database ID.
type Reference struct {
	Kind            string         `json:"kind"`
	DatabaseID      int64          `json:"database_id"`
	NodeID          string         `json:"node_id"`
	URL             string         `json:"url"`
	BodySHA256      string         `json:"body_sha256"`
	CanonicalSHA256 string         `json:"canonical_sha256"`
	Author          GitHubIdentity `json:"author"`
}

type SourceReference struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Baseline struct {
	Envelope
	Inputs       []SourceReference `json:"inputs"`
	Design       SourceReference   `json:"design"`
	PolicySHA256 string            `json:"policy_sha256"`
	Review       Reference         `json:"review"`
	Approval     Reference         `json:"approval"`
}

type Member struct {
	Issue           int64     `json:"issue"`
	IssueDatabaseID int64     `json:"issue_database_id"`
	IssueNodeID     string    `json:"issue_node_id"`
	Contract        Reference `json:"contract"`
}

type Budget struct {
	MaxAgentRuns            int64  `json:"max_agent_runs"`
	MaxRunSeconds           int64  `json:"max_run_seconds"`
	MaxWallSeconds          int64  `json:"max_wall_seconds"`
	MaxDesignRounds         int64  `json:"max_design_rounds"`
	MaxPlanningRounds       int64  `json:"max_planning_rounds"`
	MaxImplementationRounds int64  `json:"max_implementation_rounds"`
	MaxDefectFixIssues      int64  `json:"max_defect_fix_issues"`
	MaxChangeRequests       int64  `json:"max_change_requests"`
	MaxCostMinorUnits       int64  `json:"max_cost_minor_units"`
	Currency                string `json:"currency"`
}

type Plan struct {
	Envelope
	ParentRevision       *Reference `json:"parent_revision,omitempty"`
	Baseline             Reference  `json:"baseline"`
	Members              []Member   `json:"members"`
	NativeTopologySHA256 string     `json:"native_topology_sha256"`
	Budget               Budget     `json:"budget"`
	Completion           []string   `json:"completion"`
}

// Contract deliberately has no milestone/dependency/progress/delivery fields.
// DesignBaseline and ParentRevision preserve the already-published v1 wire
// shape of the selected bootstrap contract; they are not authorization proofs.
type Contract struct {
	Envelope
	DesignBaseline        DesignBaseline    `json:"design_baseline"`
	Key                   string            `json:"key"`
	Goal                  string            `json:"goal"`
	Scope                 []string          `json:"scope"`
	NonGoals              []string          `json:"non_goals"`
	Requirements          []string          `json:"requirements"`
	Acceptance            []Criterion       `json:"acceptance"`
	Verification          []Check           `json:"verification"`
	ParentRevision        *ContractRevision `json:"parent_revision,omitempty"`
	ImpactSurface         []string          `json:"impact_surface"`
	Risks                 []Risk            `json:"risks"`
	AcceptanceEnvironment Environment       `json:"acceptance_environment"`
}

type DesignBaseline struct {
	Issue       int64  `json:"issue"`
	PullRequest int64  `json:"pull_request"`
	MergeCommit string `json:"merge_commit"`
	Tree        string `json:"tree"`
	Approval    string `json:"approval"`
}

type ContractRevision struct {
	CommentDatabaseID int64  `json:"comment_database_id"`
	CommentNodeID     string `json:"comment_node_id,omitempty"`
	CommentURL        string `json:"comment_url"`
	BodySHA256        string `json:"body_sha256"`
	Disposition       string `json:"disposition"`
}

type Criterion struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
}
type Check struct {
	ID   string   `json:"id,omitempty"`
	Argv []string `json:"argv"`
}
type Risk struct {
	Risk       string `json:"risk"`
	Mitigation string `json:"mitigation"`
}
type Environment struct {
	OS          []string `json:"os"`
	Go          string   `json:"go"`
	Network     string   `json:"network"`
	Credentials string   `json:"credentials"`
	Fixtures    string   `json:"fixtures"`
}

type Run struct {
	Envelope
	Role                 Role        `json:"role"`
	AgentInstance        string      `json:"agent_instance"`
	Host                 string      `json:"host"`
	Inputs               []Reference `json:"inputs"`
	InputSHA256          string      `json:"input_sha256"`
	AssignmentGeneration int64       `json:"assignment_generation"`
	Attempt              int64       `json:"attempt"`
	Budget               Budget      `json:"budget"`
	ResultReferences     []Reference `json:"result_references,omitempty"`
}

type Binding struct {
	PullRequest    int64  `json:"pull_request"`
	HeadSHA        string `json:"head_sha"`
	BaseSHA        string `json:"base_sha"`
	DesignSHA256   string `json:"design_sha256"`
	ContractSHA256 string `json:"contract_sha256"`
	PolicySHA256   string `json:"policy_sha256"`
}

type Evidence struct {
	Envelope
	Binding           Binding   `json:"binding"`
	Run               Reference `json:"run"`
	CheckID           string    `json:"check_id"`
	Argv              []string  `json:"argv"`
	EnvironmentSHA256 string    `json:"environment_sha256"`
	Result            string    `json:"result"`
	ExitCode          int64     `json:"exit_code"`
	LogSHA256         string    `json:"log_sha256"`
}

type Assessment struct {
	CriterionID string      `json:"criterion_id"`
	Result      string      `json:"result"`
	Reason      string      `json:"reason"`
	Evidence    []Reference `json:"evidence"`
}

type Acceptance struct {
	Envelope
	Binding            Binding      `json:"binding"`
	Run                Reference    `json:"run"`
	Assessments        []Assessment `json:"assessments"`
	UnresolvedFindings []Reference  `json:"unresolved_findings"`
	Decision           string       `json:"decision"`
	Reason             string       `json:"reason"`
}

// PlanAcceptance reviews an exact milestone Plan, without borrowing a PR
// binding. Assessment evidence identifies the facts reviewed; the activation
// gate owns the required criteria, independent identities and Run inputs.
type PlanAcceptance struct {
	Envelope
	Candidate          Reference    `json:"candidate"`
	Run                Reference    `json:"run"`
	Assessments        []Assessment `json:"assessments"`
	UnresolvedFindings []Reference  `json:"unresolved_findings"`
	Decision           string       `json:"decision"`
	Reason             string       `json:"reason"`
}

type Delivery struct {
	Envelope
	Plan                Reference   `json:"plan"`
	SourceSHA           string      `json:"source_sha"`
	ArtifactSHA256      string      `json:"artifact_sha256,omitempty"`
	EnvironmentSHA256   string      `json:"environment_sha256"`
	IntegrationEvidence []Reference `json:"integration_evidence"`
	BugGate             Reference   `json:"bug_gate"`
	Cleanup             Reference   `json:"cleanup"`
}

type Approval struct {
	Envelope
	Candidate    Reference `json:"candidate"`
	PolicySHA256 string    `json:"policy_sha256"`
	Decision     string    `json:"decision"`
	Reason       string    `json:"reason"`
}
