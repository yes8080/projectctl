package protocol

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var (
	namePattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	operationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)
	digestPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	commitPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func required(value, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	return nil
}

func nonempty(values []string, field string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s must not be empty", field)
	}
	seen := make(map[string]bool)
	for _, v := range values {
		if err := required(v, field); err != nil {
			return err
		}
		if seen[v] {
			return fmt.Errorf("duplicate %s value", field)
		}
		seen[v] = true
	}
	return nil
}

func (p Project) Validate() error {
	if !namePattern.MatchString(p.Owner) || !namePattern.MatchString(p.Repo) || p.ControlIssue <= 0 {
		return fmt.Errorf("invalid project identity")
	}
	return nil
}

func (s Subject) validate() error {
	count := 0
	for _, n := range []int64{s.Issue, s.Milestone, s.PullRequest} {
		if n < 0 {
			return fmt.Errorf("negative subject identity")
		}
		if n > 0 {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("subject must identify exactly one native object")
	}
	return nil
}

func (e Envelope) validateKind(kind Kind) error {
	if e.Schema != Schema || e.Kind != kind || e.Version < 1 {
		return fmt.Errorf("invalid schema, kind or record revision")
	}
	if !operationPattern.MatchString(e.OperationID) {
		return fmt.Errorf("invalid operation_id")
	}
	if err := e.Project.Validate(); err != nil {
		return err
	}
	return e.Subject.validate()
}

func digest(value, field string) error {
	if !digestPattern.MatchString(value) {
		return fmt.Errorf("%s must be a lowercase SHA-256", field)
	}
	return nil
}

func commit(value string) error {
	if !commitPattern.MatchString(value) {
		return fmt.Errorf("commit/tree must be a full lowercase Git SHA-1")
	}
	return nil
}

func (i GitHubIdentity) Validate() error {
	if i.ID <= 0 || strings.TrimSpace(i.NodeID) == "" || strings.TrimSpace(i.Login) == "" || (i.Type != "User" && i.Type != "Bot") {
		return fmt.Errorf("invalid native GitHub identity")
	}
	return nil
}

func (r Reference) Validate(project Project) error {
	if err := project.Validate(); err != nil {
		return err
	}
	if r.DatabaseID <= 0 || strings.TrimSpace(r.NodeID) == "" {
		return fmt.Errorf("reference requires stable database and node IDs")
	}
	if err := recordURL(project, r.Kind, r.DatabaseID, r.URL); err != nil {
		return err
	}
	if err := digest(r.BodySHA256, "reference body_sha256"); err != nil {
		return err
	}
	if err := digest(r.CanonicalSHA256, "reference canonical_sha256"); err != nil {
		return err
	}
	return r.Author.Validate()
}

func recordURL(project Project, kind string, id int64, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || u.RawFragment != "" {
		return fmt.Errorf("invalid GitHub reference URL")
	}
	base := "/" + project.Owner + "/" + project.Repo + "/"
	var expr, fragment string
	switch kind {
	case "issue_comment":
		expr = `(?:issues|pull)/[1-9][0-9]*`
		fragment = fmt.Sprintf("issuecomment-%d", id)
	case "pull_request_review":
		expr = `pull/[1-9][0-9]*`
		fragment = fmt.Sprintf("pullrequestreview-%d", id)
	default:
		return fmt.Errorf("unsupported reference kind %q", kind)
	}
	if !regexp.MustCompile("^"+regexp.QuoteMeta(base)+expr+"$").MatchString(u.Path) || u.Fragment != fragment {
		return fmt.Errorf("reference URL disagrees with repository, kind or ID")
	}
	return nil
}

func (s SourceReference) validate() error {
	if err := commit(s.Commit); err != nil {
		return err
	}
	if s.Path == "" || strings.HasPrefix(s.Path, "/") || strings.Contains(s.Path, "\\") || path.Clean(s.Path) != s.Path || s.Path == "." || s.Path == ".." || strings.HasPrefix(s.Path, "../") {
		return fmt.Errorf("source path must be repository-relative and normalized")
	}
	return digest(s.SHA256, "source sha256")
}

func refs(p Project, values ...Reference) error {
	for _, r := range values {
		if err := r.Validate(p); err != nil {
			return err
		}
	}
	return nil
}

func (r *Baseline) validate() error {
	if err := r.Envelope.validateKind(KindBaseline); err != nil {
		return err
	}
	if len(r.Inputs) == 0 {
		return fmt.Errorf("baseline requires inputs")
	}
	for _, s := range r.Inputs {
		if err := s.validate(); err != nil {
			return err
		}
	}
	if err := r.Design.validate(); err != nil {
		return err
	}
	if err := digest(r.PolicySHA256, "policy_sha256"); err != nil {
		return err
	}
	return refs(r.Project, r.Review, r.Approval)
}

func (b Budget) validate() error {
	if b.MaxAgentRuns <= 0 || b.MaxRunSeconds <= 0 || b.MaxWallSeconds <= 0 || b.MaxDesignRounds <= 0 || b.MaxPlanningRounds <= 0 || b.MaxImplementationRounds <= 0 || b.MaxDefectFixIssues < 0 || b.MaxChangeRequests < 0 || b.MaxCostMinorUnits < 0 || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(b.Currency) {
		return fmt.Errorf("invalid finite budget")
	}
	return nil
}

func (r *Plan) validate() error {
	if err := r.Envelope.validateKind(KindPlan); err != nil {
		return err
	}
	if r.Subject.Milestone <= 0 {
		return fmt.Errorf("plan subject must be milestone")
	}
	if err := refs(r.Project, r.Baseline); err != nil {
		return err
	}
	if r.ParentRevision != nil {
		if err := refs(r.Project, *r.ParentRevision); err != nil {
			return err
		}
	}
	if len(r.Members) == 0 {
		return fmt.Errorf("plan requires members")
	}
	issues, ids, nodes := map[int64]bool{}, map[int64]bool{}, map[string]bool{}
	for _, m := range r.Members {
		if m.Issue <= 0 || m.IssueDatabaseID <= 0 || m.IssueNodeID == "" || issues[m.Issue] || ids[m.IssueDatabaseID] || nodes[m.IssueNodeID] {
			return fmt.Errorf("invalid or duplicate plan member")
		}
		issues[m.Issue], ids[m.IssueDatabaseID], nodes[m.IssueNodeID] = true, true, true
		if err := refs(r.Project, m.Contract); err != nil {
			return err
		}
		want := fmt.Sprintf("https://github.com/%s/%s/issues/%d#", r.Project.Owner, r.Project.Repo, m.Issue)
		if !strings.HasPrefix(m.Contract.URL, want) {
			return fmt.Errorf("contract reference belongs to another issue")
		}
	}
	if err := digest(r.NativeTopologySHA256, "native_topology_sha256"); err != nil {
		return err
	}
	if err := r.Budget.validate(); err != nil {
		return err
	}
	return nonempty(r.Completion, "completion")
}

func (r *Contract) validate() error {
	if err := r.Envelope.validateKind(KindContract); err != nil {
		return err
	}
	if r.Subject.Issue <= 0 {
		return fmt.Errorf("contract subject must be issue")
	}
	b := r.DesignBaseline
	if b.Issue <= 0 || b.PullRequest <= 0 {
		return fmt.Errorf("contract requires design baseline identity")
	}
	if err := commit(b.MergeCommit); err != nil {
		return err
	}
	if err := commit(b.Tree); err != nil {
		return err
	}
	u, err := url.Parse(b.Approval)
	if err != nil || u == nil {
		return fmt.Errorf("invalid baseline approval URL")
	}
	var approvalID int64
	if _, err := fmt.Sscanf(u.Fragment, "issuecomment-%d", &approvalID); err != nil || approvalID <= 0 {
		return fmt.Errorf("invalid baseline approval comment")
	}
	if err := recordURL(r.Project, "issue_comment", approvalID, b.Approval); err != nil {
		return err
	}
	if u.Path != fmt.Sprintf("/%s/%s/pull/%d", r.Project.Owner, r.Project.Repo, b.PullRequest) {
		return fmt.Errorf("baseline approval must reference design PR")
	}
	for field, s := range map[string]string{"key": r.Key, "goal": r.Goal} {
		if err := required(s, field); err != nil {
			return err
		}
	}
	for field, list := range map[string][]string{"scope": r.Scope, "non_goals": r.NonGoals, "requirements": r.Requirements, "impact_surface": r.ImpactSurface} {
		if err := nonempty(list, field); err != nil {
			return err
		}
	}
	if len(r.Acceptance) == 0 || len(r.Verification) == 0 || len(r.Risks) == 0 {
		return fmt.Errorf("contract requires ACs, checks and risks")
	}
	seen := map[string]bool{}
	for _, ac := range r.Acceptance {
		if !operationPattern.MatchString(ac.ID) || strings.TrimSpace(ac.Statement) == "" || seen[ac.ID] {
			return fmt.Errorf("invalid or duplicate criterion")
		}
		seen[ac.ID] = true
	}
	checks := map[string]bool{}
	for _, check := range r.Verification {
		if err := argv(check.Argv); err != nil {
			return err
		}
		if check.ID != "" {
			if !operationPattern.MatchString(check.ID) || checks[check.ID] {
				return fmt.Errorf("invalid or duplicate check ID")
			}
			checks[check.ID] = true
		}
	}
	for _, risk := range r.Risks {
		if strings.TrimSpace(risk.Risk) == "" || strings.TrimSpace(risk.Mitigation) == "" {
			return fmt.Errorf("risk and mitigation required")
		}
	}
	e := r.AcceptanceEnvironment
	if err := nonempty(e.OS, "environment.os"); err != nil {
		return err
	}
	for _, s := range []string{e.Go, e.Network, e.Credentials, e.Fixtures} {
		if err := required(s, "environment"); err != nil {
			return err
		}
	}
	if p := r.ParentRevision; p != nil {
		if p.CommentDatabaseID <= 0 || strings.TrimSpace(p.Disposition) == "" {
			return fmt.Errorf("invalid contract parent")
		}
		if err := recordURL(r.Project, "issue_comment", p.CommentDatabaseID, p.CommentURL); err != nil {
			return err
		}
		want := fmt.Sprintf("https://github.com/%s/%s/issues/%d#", r.Project.Owner, r.Project.Repo, r.Subject.Issue)
		if !strings.HasPrefix(p.CommentURL, want) {
			return fmt.Errorf("parent belongs to another issue")
		}
		if err := digest(p.BodySHA256, "parent body_sha256"); err != nil {
			return err
		}
	}
	return nil
}

func argv(values []string) error {
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		return fmt.Errorf("check requires command argv")
	}
	for _, s := range values {
		if strings.ContainsRune(s, 0) {
			return fmt.Errorf("NUL in argv")
		}
	}
	return nil
}

func (r *Run) validate() error {
	if err := r.Envelope.validateKind(KindRun); err != nil {
		return err
	}
	if !validRole(r.Role) || r.AgentInstance == "" || r.Host == "" || r.AssignmentGeneration <= 0 || r.Attempt <= 0 || len(r.Inputs) == 0 {
		return fmt.Errorf("invalid run identity, assignment or inputs")
	}
	if err := refs(r.Project, r.Inputs...); err != nil {
		return err
	}
	if err := refs(r.Project, r.ResultReferences...); err != nil {
		return err
	}
	if err := digest(r.InputSHA256, "input_sha256"); err != nil {
		return err
	}
	return r.Budget.validate()
}

func (b Binding) validate(s Subject) error {
	if b.PullRequest <= 0 || s.PullRequest != b.PullRequest {
		return fmt.Errorf("binding must match PR subject")
	}
	for _, sha := range []string{b.HeadSHA, b.BaseSHA} {
		if err := commit(sha); err != nil {
			return err
		}
	}
	for _, sha := range []string{b.DesignSHA256, b.ContractSHA256, b.PolicySHA256} {
		if err := digest(sha, "binding digest"); err != nil {
			return err
		}
	}
	return nil
}

func (r *Evidence) validate() error {
	if err := r.Envelope.validateKind(KindEvidence); err != nil {
		return err
	}
	if err := r.Binding.validate(r.Subject); err != nil {
		return err
	}
	if err := refs(r.Project, r.Run); err != nil {
		return err
	}
	if !operationPattern.MatchString(r.CheckID) || (r.Result != "PASS" && r.Result != "FAIL" && r.Result != "ERROR") || (r.Result == "PASS" && r.ExitCode != 0) {
		return fmt.Errorf("invalid evidence check or result")
	}
	if err := argv(r.Argv); err != nil {
		return err
	}
	if err := digest(r.EnvironmentSHA256, "environment_sha256"); err != nil {
		return err
	}
	return digest(r.LogSHA256, "log_sha256")
}

func (r *Acceptance) validate() error {
	if err := r.Envelope.validateKind(KindAcceptance); err != nil {
		return err
	}
	if err := r.Binding.validate(r.Subject); err != nil {
		return err
	}
	if err := refs(r.Project, r.Run); err != nil {
		return err
	}
	if len(r.Assessments) == 0 || (r.Decision != "PASS" && r.Decision != "FAIL") || strings.TrimSpace(r.Reason) == "" || r.UnresolvedFindings == nil {
		return fmt.Errorf("invalid acceptance decision")
	}
	if r.Decision == "PASS" && len(r.UnresolvedFindings) != 0 {
		return fmt.Errorf("PASS has unresolved findings")
	}
	seen := map[string]bool{}
	for _, ac := range r.Assessments {
		if !operationPattern.MatchString(ac.CriterionID) || seen[ac.CriterionID] || strings.TrimSpace(ac.Reason) == "" || len(ac.Evidence) == 0 || (ac.Result != "PASS" && ac.Result != "FAIL") || (r.Decision == "PASS" && ac.Result != "PASS") {
			return fmt.Errorf("invalid criterion assessment")
		}
		seen[ac.CriterionID] = true
		if err := refs(r.Project, ac.Evidence...); err != nil {
			return err
		}
	}
	return refs(r.Project, r.UnresolvedFindings...)
}

func (r *Delivery) validate() error {
	if err := r.Envelope.validateKind(KindDelivery); err != nil {
		return err
	}
	if len(r.IntegrationEvidence) == 0 {
		return fmt.Errorf("delivery requires integration evidence")
	}
	if err := refs(r.Project, r.Plan, r.BugGate, r.Cleanup); err != nil {
		return err
	}
	if err := refs(r.Project, r.IntegrationEvidence...); err != nil {
		return err
	}
	if err := commit(r.SourceSHA); err != nil {
		return err
	}
	if r.ArtifactSHA256 != "" {
		if err := digest(r.ArtifactSHA256, "artifact_sha256"); err != nil {
			return err
		}
	}
	return digest(r.EnvironmentSHA256, "environment_sha256")
}

func (r *Approval) validate() error {
	if err := r.Envelope.validateKind(KindApproval); err != nil {
		return err
	}
	if r.Decision != "APPROVED" && r.Decision != "REJECTED" {
		return fmt.Errorf("invalid approval decision")
	}
	if err := required(r.Reason, "approval reason"); err != nil {
		return err
	}
	if err := digest(r.PolicySHA256, "policy_sha256"); err != nil {
		return err
	}
	return refs(r.Project, r.Candidate)
}
