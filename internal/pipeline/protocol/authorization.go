package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

type Role string

const (
	RoleController     Role = "controller"
	RoleDesigner       Role = "designer"
	RoleDesignReviewer Role = "design_reviewer"
	RolePlanner        Role = "planner"
	RolePlanReviewer   Role = "plan_reviewer"
	RoleDeveloper      Role = "developer"
	RoleAcceptor       Role = "acceptor"
	RolePublisher      Role = "publisher"
)

func validRole(role Role) bool {
	switch role {
	case RoleController, RoleDesigner, RoleDesignReviewer, RolePlanner, RolePlanReviewer, RoleDeveloper, RoleAcceptor, RolePublisher:
		return true
	default:
		return false
	}
}

type Grant struct {
	Principal GitHubIdentity `json:"principal"`
	Roles     []Role         `json:"roles"`
}

// Policy must itself come from the Controller's trusted, pinned policy source.
// A record does not get to choose its policy merely by supplying its digest.
type Policy struct {
	Schema  string  `json:"schema"`
	Version int64   `json:"version"`
	Project Project `json:"project"`
	Grants  []Grant `json:"grants"`
}

const PolicySchema = "projectctl.policy/v1"

func (p Policy) Validate() error {
	if p.Schema != PolicySchema || p.Version <= 0 || len(p.Grants) == 0 {
		return fmt.Errorf("invalid policy schema, revision or grants")
	}
	if err := p.Project.Validate(); err != nil {
		return err
	}
	ids, nodes := map[int64]bool{}, map[string]bool{}
	for _, grant := range p.Grants {
		if err := grant.Principal.Validate(); err != nil {
			return err
		}
		if ids[grant.Principal.ID] || nodes[grant.Principal.NodeID] || len(grant.Roles) == 0 {
			return fmt.Errorf("duplicate or empty policy grant")
		}
		ids[grant.Principal.ID], nodes[grant.Principal.NodeID] = true, true
		roles := map[Role]bool{}
		for _, role := range grant.Roles {
			if !validRole(role) || roles[role] {
				return fmt.Errorf("unknown or duplicate role")
			}
			roles[role] = true
		}
	}
	return nil
}

func DecodePolicy(data []byte) (Policy, error) {
	var policy Policy
	v, err := parseJSON(data)
	if err != nil {
		return policy, err
	}
	if err := exactFields(v, reflect.TypeOf(policy), "policy"); err != nil {
		return policy, err
	}
	if err := json.Unmarshal(data, &policy); err != nil {
		return policy, err
	}
	return policy, policy.Validate()
}

func (p Policy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if err := validStrings(reflect.ValueOf(p)); err != nil {
		return "", err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return DigestJSON(data)
}

// GitHubObservation is the trusted Gateway boundary, NOT an Agent input type.
// Only the authenticated GitHub adapter may populate it, from a complete native
// API response. This package cannot establish network provenance or credential
// isolation. Never decode an Agent's JSON into this type and call it verified.
type GitHubObservation struct {
	Project    Project
	Kind       string
	DatabaseID int64
	NodeID     string
	URL        string
	Author     GitHubIdentity
	Body       string
	Deleted    bool
}

// VerifiedRecord is an in-memory capability for a checked Gateway observation.
// Its zero value is unusable, and JSON cannot create or persist the capability.
type VerifiedRecord struct {
	reference Reference
	header    Envelope
	canonical []byte
}

func (VerifiedRecord) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("verified observations are ephemeral; persist GitHub references instead")
}
func (*VerifiedRecord) UnmarshalJSON([]byte) error {
	return fmt.Errorf("verified identity cannot be supplied in JSON")
}

func (v VerifiedRecord) Reference() Reference { return v.reference }

// Record returns a newly decoded value, never a mutable alias to verified data.
func (v VerifiedRecord) Record() (Record, error) {
	if len(v.canonical) == 0 {
		return nil, fmt.Errorf("unverified record")
	}
	return Decode(v.canonical)
}

// VerifyGitHubRecord checks native object identity and raw/canonical content
// against an already pinned reference. An edited, moved, deleted or recreated
// object fails, even when a replacement's JSON has the same semantic content.
func VerifyGitHubRecord(observation GitHubObservation, expected Reference) (VerifiedRecord, error) {
	var zero VerifiedRecord
	if observation.Deleted {
		return zero, fmt.Errorf("referenced GitHub object is deleted")
	}
	if err := expected.Validate(observation.Project); err != nil {
		return zero, err
	}
	if err := observation.Author.Validate(); err != nil {
		return zero, err
	}
	if observation.Kind != expected.Kind || observation.DatabaseID != expected.DatabaseID || observation.NodeID != expected.NodeID || observation.URL != expected.URL || !sameIdentity(observation.Author, expected.Author) {
		return zero, fmt.Errorf("native GitHub identity does not match pinned reference")
	}
	if SHA256([]byte(observation.Body)) != expected.BodySHA256 {
		return zero, fmt.Errorf("GitHub body digest drift")
	}
	data, err := commentJSON(observation.Body)
	if err != nil {
		return zero, err
	}
	record, err := Decode(data)
	if err != nil {
		return zero, err
	}
	if record.Header().Project != observation.Project {
		return zero, fmt.Errorf("record belongs to another project")
	}
	if err := recordLocation(record.Header(), observation.URL); err != nil {
		return zero, err
	}
	canonical, err := CanonicalJSON(data)
	if err != nil {
		return zero, err
	}
	if SHA256(canonical) != expected.CanonicalSHA256 {
		return zero, fmt.Errorf("canonical record digest drift")
	}
	return VerifiedRecord{reference: expected, header: record.Header(), canonical: canonical}, nil
}

func recordLocation(header Envelope, raw string) error {
	prefix := fmt.Sprintf("https://github.com/%s/%s/", header.Project.Owner, header.Project.Repo)
	var locations []string
	switch header.Kind {
	case KindContract:
		locations = []string{fmt.Sprintf("%sissues/%d#", prefix, header.Subject.Issue)}
	case KindEvidence, KindAcceptance:
		locations = []string{fmt.Sprintf("%spull/%d#", prefix, header.Subject.PullRequest)}
	case KindRun:
		locations = []string{fmt.Sprintf("%sissues/%d#", prefix, header.Project.ControlIssue), fmt.Sprintf("%sissues/%d#", prefix, header.Subject.Issue), fmt.Sprintf("%spull/%d#", prefix, header.Subject.PullRequest)}
	default:
		locations = []string{fmt.Sprintf("%sissues/%d#", prefix, header.Project.ControlIssue)}
	}
	for _, location := range locations {
		if len(raw) > len(location) && raw[:len(location)] == location {
			return nil
		}
	}
	return fmt.Errorf("record is published on the wrong native subject")
}

func sameIdentity(a, b GitHubIdentity) bool {
	return a.ID == b.ID && a.NodeID == b.NodeID && a.Type == b.Type
}

func sameReference(a, b Reference) bool {
	return a.Kind == b.Kind && a.DatabaseID == b.DatabaseID && a.NodeID == b.NodeID && a.URL == b.URL && a.BodySHA256 == b.BodySHA256 && a.CanonicalSHA256 == b.CanonicalSHA256 && sameIdentity(a.Author, b.Author)
}

func Authorize(record VerifiedRecord, policy Policy, role Role) error {
	if len(record.canonical) == 0 {
		return fmt.Errorf("unverified GitHub identity")
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	if !validRole(role) || record.header.Project != policy.Project {
		return fmt.Errorf("role or project is outside policy")
	}
	for _, grant := range policy.Grants {
		if !sameIdentity(grant.Principal, record.reference.Author) {
			continue
		}
		for _, allowed := range grant.Roles {
			if allowed == role {
				return nil
			}
		}
	}
	return fmt.Errorf("native GitHub author is not authorized for %s", role)
}

// RequireIndependent is intentionally stricter than the manual single-login
// bootstrap: separate Agent labels with the same native author do not qualify.
// Agent-instance/run isolation is an additional Runtime/Gateway responsibility.
func RequireIndependent(a, b VerifiedRecord) error {
	if len(a.canonical) == 0 || len(b.canonical) == 0 {
		return fmt.Errorf("unverified participant")
	}
	if a.reference.Author.ID == b.reference.Author.ID || a.reference.Author.NodeID == b.reference.Author.NodeID {
		return fmt.Errorf("independent native GitHub identities required")
	}
	return nil
}

// SelectApproved selects only the exact candidate named by the authoritative
// approval passed by the Controller. It never chooses the newest candidate.
// Resolving approval/revision supersession is the Controller's responsibility;
// this function does not infer authority from timestamps or list order.
// A selected Acceptance or PlanAcceptance is a formal PASS, not proof of
// independent review, exact Run inputs or actual PR head/base/merge/source
// facts; higher-level gates verify those facts.
func SelectApproved(candidates []VerifiedRecord, approval VerifiedRecord, policy Policy) (VerifiedRecord, error) {
	var zero VerifiedRecord
	if err := Authorize(approval, policy, RoleController); err != nil {
		return zero, err
	}
	record, err := approval.Record()
	if err != nil {
		return zero, err
	}
	decision, ok := record.(*Approval)
	if !ok || decision.Decision != "APPROVED" {
		return zero, fmt.Errorf("an explicit APPROVED record is required")
	}
	policyDigest, err := policy.Digest()
	if err != nil {
		return zero, err
	}
	if decision.PolicySHA256 != policyDigest {
		return zero, fmt.Errorf("approval is bound to another policy revision")
	}
	var selected VerifiedRecord
	operations := make(map[string]VerifiedRecord)
	objects := make(map[int64]Reference)
	for _, candidate := range candidates {
		if len(candidate.canonical) == 0 {
			return zero, fmt.Errorf("unverified candidate")
		}
		if candidate.header.Project != approval.header.Project || candidate.header.Subject != approval.header.Subject {
			return zero, fmt.Errorf("candidate and approval have different project/subject")
		}
		if prior, exists := operations[candidate.header.OperationID]; exists && (!bytes.Equal(prior.canonical, candidate.canonical) || !sameReference(prior.reference, candidate.reference)) {
			return zero, fmt.Errorf("conflicting or duplicated operation_id object")
		}
		operations[candidate.header.OperationID] = candidate
		if prior, exists := objects[candidate.reference.DatabaseID]; exists && !sameReference(prior, candidate.reference) {
			return zero, fmt.Errorf("conflicting candidate object")
		}
		objects[candidate.reference.DatabaseID] = candidate.reference
		if !sameReference(candidate.reference, decision.Candidate) {
			continue
		}
		switch candidate.header.Kind {
		case KindBaseline, KindPlan, KindContract:
		case KindAcceptance:
			record, err := candidate.Record()
			if err != nil {
				return zero, err
			}
			acceptance, ok := record.(*Acceptance)
			if !ok || acceptance.Decision != "PASS" || len(acceptance.UnresolvedFindings) != 0 {
				return zero, fmt.Errorf("an approved acceptance candidate requires formal PASS without unresolved findings")
			}
		case KindPlanAcceptance:
			record, err := candidate.Record()
			if err != nil {
				return zero, err
			}
			acceptance, ok := record.(*PlanAcceptance)
			if !ok || acceptance.Decision != "PASS" || len(acceptance.UnresolvedFindings) != 0 {
				return zero, fmt.Errorf("an approved plan acceptance candidate requires formal PASS without unresolved findings")
			}
		default:
			return zero, fmt.Errorf("record kind cannot be an approved candidate")
		}
		selected = candidate
	}
	if len(selected.canonical) == 0 {
		return zero, fmt.Errorf("exact approved candidate is missing or changed")
	}
	return selected, nil
}
