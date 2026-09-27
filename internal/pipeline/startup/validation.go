package startup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sha64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)

func validRepo(r Repository) bool {
	// Reuse only the protocol's pure owner/name validator. No API is configured
	// with this sentinel: bootstrap never invents a control Issue number.
	return r.DatabaseID > 0 && strings.TrimSpace(r.NodeID) != "" && (protocol.Project{Owner: r.Owner, Repo: r.Name, ControlIssue: 1}).Validate() == nil
}

func safePath(p string) bool {
	if p == "" || p == "." || p == ".." || len(p) > 4096 || !utf8.ValidString(p) || len(strings.Split(p, "/")) > 64 || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\%:") {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validSource(s protocol.SourceReference) bool {
	return sha40.MatchString(s.Commit) && sha64.MatchString(s.SHA256) && safePath(s.Path)
}
func sameIdentity(a, b protocol.GitHubIdentity) bool {
	return a.ID == b.ID && a.NodeID == b.NodeID && a.Type == b.Type
}

// strictDecode rejects omitted required zero-valued limits, nulls, aliases,
// duplicate/unknown fields and floats; JSON field presence is not inferred from
// Go zero values. CanonicalJSON supplies the shared protocol digest dialect.
func strictDecode(data []byte, out any) error {
	canonical, err := protocol.CanonicalJSON(data)
	if err != nil {
		return err
	}
	if err := exactFields(canonical, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(canonical))
	d.DisallowUnknownFields()
	return d.Decode(out)
}

func exactFields(data []byte, t reflect.Type) error {
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("null is not an explicit policy value")
	}
	if t.Kind() == reflect.Pointer {
		return exactFields(data, t.Elem())
	}
	if t.Kind() == reflect.Struct {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			name := tag[0]
			if name == "" {
				name = f.Name
			}
			value, ok := fields[name]
			if !ok {
				if len(tag) > 1 && tag[1] == "omitempty" {
					continue
				}
				return fmt.Errorf("missing required field %s", name)
			}
			if err := exactFields(value, f.Type); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			delete(fields, name)
		}
		if len(fields) > 0 {
			return fmt.Errorf("unknown or non-exact JSON field")
		}
	} else if t.Kind() == reflect.Slice {
		var entries []json.RawMessage
		if err := json.Unmarshal(data, &entries); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := exactFields(entry, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func DecodeManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := strictDecode(data, &m); err != nil {
		return m, err
	}
	return m, m.Validate()
}

func (m Manifest) Validate() error {
	if m.Schema != ManifestSchema || m.Version <= 0 || !validRepo(m.Repository) || !keyPattern.MatchString(m.Cycle) || !keyPattern.MatchString(m.OperationID) || len(m.Inputs) == 0 {
		return fmt.Errorf("invalid startup identity or inputs")
	}
	seen := map[string]bool{}
	hasDocument := false
	for _, in := range m.Inputs {
		if in.Kind != "product" && in.Kind != "development" && in.Kind != "constraints" {
			return fmt.Errorf("unknown input kind")
		}
		if !validSource(in.Source) {
			return fmt.Errorf("input must pin a commit, path and content digest")
		}
		key := in.Source.Commit + ":" + in.Source.Path
		if seen[key] {
			return fmt.Errorf("duplicate input document")
		}
		seen[key] = true
		hasDocument = hasDocument || in.Kind != "constraints"
	}
	if !hasDocument {
		return fmt.Errorf("product or development document required")
	}
	return m.Policy.Validate()
}

func nonempty(xs []string) bool {
	if len(xs) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, x := range xs {
		if strings.TrimSpace(x) == "" || strings.TrimSpace(x) != x || seen[x] {
			return false
		}
		seen[x] = true
	}
	return true
}

func (p Policy) Validate() error {
	if strings.TrimSpace(p.Goal) == "" || !nonempty(p.NonGoals) || !p.StoreInputsAuthorized || !p.RemoteWritesAuthorized {
		return fmt.Errorf("goal, non-goals and explicit input/write authorization required")
	}
	for _, id := range []protocol.GitHubIdentity{p.Roles.Controller, p.Roles.Designer, p.Roles.DesignReviewer, p.Roles.Developer, p.Roles.Acceptor} {
		if err := id.Validate(); err != nil {
			return err
		}
	}
	if sameIdentity(p.Roles.Designer, p.Roles.DesignReviewer) || sameIdentity(p.Roles.Developer, p.Roles.Acceptor) || sameIdentity(p.Roles.Controller, p.Roles.Developer) || sameIdentity(p.Roles.Controller, p.Roles.Acceptor) {
		return fmt.Errorf("independent role identities required")
	}
	b := p.Budget
	if b.MaxAgentRuns <= 0 || b.MaxRunSeconds <= 0 || b.MaxWallSeconds < b.MaxRunSeconds || b.MaxDesignRounds <= 0 || b.MaxPlanningRounds <= 0 || b.MaxImplementationRounds <= 0 || b.MaxDefectFixIssues < 0 || b.MaxChangeRequests < 0 || b.MaxCostMinorUnits < 0 || !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(b.Currency) || p.MaxParallelDevelopment <= 0 || p.MaxParallelDevelopment > b.MaxAgentRuns || p.MaxParallelMerge != 1 {
		return fmt.Errorf("all finite budgets and serial merge policy required")
	}
	c := p.Completion
	if !nonempty(c.Criteria) || !c.RequireIntegration || !c.RequireBugGate || !c.RequireRemoteCleanup || !c.RequireLocalCleanup {
		return fmt.Errorf("complete delivery and cleanup policy required")
	}
	if c.Endpoint == "integrated_code" {
		if c.Deployment != nil {
			return fmt.Errorf("deployment outside completion scope")
		}
	} else if c.Endpoint == "deployed_and_verified" {
		if c.Deployment == nil || !c.Deployment.Authorized || strings.TrimSpace(c.Deployment.Target) == "" || strings.TrimSpace(c.Deployment.RollbackPolicy) == "" {
			return fmt.Errorf("explicit deployment target, authorization and rollback required")
		}
	} else {
		return fmt.Errorf("unknown delivery endpoint")
	}
	required := map[string]bool{"completed": false, "blocked": false, "budget_exceeded": false, "failed": false, "paused": false, "cancelled": false}
	for _, v := range p.TerminalConditions {
		if used, ok := required[v]; !ok || used {
			return fmt.Errorf("unknown or duplicate terminal condition")
		}
		required[v] = true
	}
	for _, set := range required {
		if !set {
			return fmt.Errorf("missing terminal condition")
		}
	}
	e := p.Environment
	if !nonempty(e.OperatingSystems) || strings.TrimSpace(e.Runtime) == "" || strings.TrimSpace(e.AcceptanceTarget) == "" || e.WorkspaceIsolation != "isolated" || e.CredentialIsolation != "controller_only" || strings.TrimSpace(e.ExternalUsageLimit) == "" {
		return fmt.Errorf("required isolated runtime and acceptance environment missing")
	}
	for _, os := range e.OperatingSystems {
		if os != "linux" && os != "macos" && os != "windows" {
			return fmt.Errorf("unsupported operating system")
		}
	}
	if !nonempty(p.RequiredGitHubCapabilities) {
		return fmt.Errorf("required GitHub capabilities missing")
	}
	hasIssues, hasEnabled, hasPush, hasAutoMerge, hasGate := false, false, false, false, false
	for _, name := range p.RequiredGitHubCapabilities {
		if !knownCapability(name) {
			return fmt.Errorf("unknown required GitHub capability")
		}
		hasIssues = hasIssues || name == "issues_read"
		hasEnabled = hasEnabled || name == "issues_enabled"
		hasPush = hasPush || name == "push_permission"
		hasAutoMerge = hasAutoMerge || name == "auto_merge"
		hasGate = hasGate || name == "protected_merge_gate"
	}
	if !hasIssues || !hasEnabled || !hasPush {
		return fmt.Errorf("issue access and publication permission must be preflighted")
	}
	if hasAutoMerge && !hasGate {
		return fmt.Errorf("auto-merge requires a proven protected gate")
	}
	if !nonempty(p.DesignFiles) {
		return fmt.Errorf("explicit design artifact paths required")
	}
	for _, file := range p.DesignFiles {
		if !safePath(file) || (path.Ext(file) != ".md" && path.Ext(file) != ".txt") {
			return fmt.Errorf("design artifact must be a fixed document path")
		}
	}
	return nil
}
