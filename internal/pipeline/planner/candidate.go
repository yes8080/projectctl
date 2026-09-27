package planner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yes8080/projectctl/internal/pipeline/design"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

const CandidateSchema = "projectctl.plan-candidate/v1"
const maxCandidateBytes = 128 << 10
const maxCandidateItems = 2000

// Candidate is disposable model output, not a second persistent plan ledger.
// Only existing protocol records and GitHub native objects become authority.
type Candidate struct {
	Schema        string   `json:"schema"`
	Contracts     []Slice  `json:"contracts"`
	OpenDecisions []string `json:"open_decisions"`
}

type Slice struct {
	Key                   string               `json:"key"`
	Kind                  string               `json:"kind"`
	Goal                  string               `json:"goal"`
	Scope                 []string             `json:"scope"`
	NonGoals              []string             `json:"non_goals"`
	Requirements          []string             `json:"requirements"`
	Acceptance            []Criterion          `json:"acceptance"`
	Verification          []protocol.Check     `json:"verification"`
	AcceptanceEnvironment protocol.Environment `json:"acceptance_environment"`
	ImpactSurface         []string             `json:"impact_surface"`
	Risks                 []protocol.Risk      `json:"risks"`
	Dependencies          []string             `json:"dependencies"`
	CandidateBinding      string               `json:"candidate_binding"`
}

type Criterion struct {
	ID           string   `json:"id"`
	Statement    string   `json:"statement"`
	Requirements []string `json:"requirements"`
	Checks       []string `json:"checks"`
}

type Summary struct {
	Coverage    []Ownership `json:"coverage"`
	Order       []string    `json:"order"`
	Edges       []Edge      `json:"edges"`
	Integration string      `json:"integration"`
}

type Ownership struct {
	Requirement string   `json:"requirement"`
	Slice       string   `json:"slice"`
	Criteria    []string `json:"criteria"`
}

// Edge points from a prerequisite slice to the slice that depends on it.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

var candidateID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)
var candidateGo = regexp.MustCompile(`^[0-9]+\.[0-9]+(?:\.[0-9]+)?$`)

func candidateText(s string) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && !strings.ContainsRune(s, 0)
}

func candidateStrings(values []string, empty bool) bool {
	if values == nil || len(values) > maxCandidateItems || (!empty && len(values) == 0) {
		return false
	}
	seen := map[string]bool{}
	for _, s := range values {
		if !candidateText(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}

// Every field is required, even protocol.Check.ID's otherwise optional tag.
func candidateFields(data []byte, typ reflect.Type) error {
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("null candidate field")
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			key := strings.Split(field.Tag.Get("json"), ",")[0]
			value, ok := object[key]
			if !ok {
				return fmt.Errorf("missing candidate field %s", key)
			}
			if err := candidateFields(value, field.Type); err != nil {
				return err
			}
			delete(object, key)
		}
		if len(object) != 0 {
			return fmt.Errorf("unknown candidate field")
		}
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		if len(items) > maxCandidateItems {
			return fmt.Errorf("candidate collection exceeds bound")
		}
		for _, item := range items {
			if err := candidateFields(item, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func DecodeCandidate(data []byte) (Candidate, error) {
	var c Candidate
	if len(data) == 0 || len(data) > maxCandidateBytes {
		return c, fmt.Errorf("candidate outside byte bound")
	}
	canonical, err := protocol.CanonicalJSON(data)
	if err != nil {
		return c, err
	}
	if err := candidateFields(canonical, reflect.TypeOf(c)); err != nil {
		return c, err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return Candidate{}, err
	}
	if err := candidateShape(c); err != nil {
		return Candidate{}, err
	}
	return c, nil
}

func candidateShape(c Candidate) error {
	if c.Schema != CandidateSchema || len(c.Contracts) == 0 || len(c.Contracts) > maxCandidateItems || c.OpenDecisions == nil || len(c.OpenDecisions) != 0 {
		return fmt.Errorf("incomplete or unresolved candidate")
	}
	keys := map[string]bool{}
	for _, s := range c.Contracts {
		if !candidateID.MatchString(s.Key) || keys[s.Key] || !candidateText(s.Goal) {
			return fmt.Errorf("invalid or duplicate slice")
		}
		keys[s.Key] = true
		if (s.Kind != "implementation" && s.Kind != "integration") || (s.Kind == "implementation" && s.CandidateBinding != "none") || (s.Kind == "integration" && s.CandidateBinding != "exact_source_artifact_environment") {
			return fmt.Errorf("invalid slice kind or candidate binding")
		}
		for _, values := range [][]string{s.Scope, s.NonGoals, s.Requirements, s.ImpactSurface} {
			if !candidateStrings(values, false) {
				return fmt.Errorf("invalid required slice collection")
			}
		}
		if !candidateStrings(s.Dependencies, true) || len(s.Acceptance) == 0 || len(s.Acceptance) > maxCandidateItems || len(s.Verification) == 0 || len(s.Verification) > maxCandidateItems || len(s.Risks) == 0 || len(s.Risks) > maxCandidateItems {
			return fmt.Errorf("invalid slice dependencies, criteria, checks or risks")
		}
		acs, checks := map[string]bool{}, map[string]bool{}
		for _, ac := range s.Acceptance {
			if !candidateID.MatchString(ac.ID) || acs[ac.ID] || !candidateText(ac.Statement) || !candidateStrings(ac.Requirements, false) || !candidateStrings(ac.Checks, false) {
				return fmt.Errorf("invalid or duplicate criterion")
			}
			acs[ac.ID] = true
		}
		for _, check := range s.Verification {
			if !candidateID.MatchString(check.ID) || checks[check.ID] || check.Argv == nil || len(check.Argv) == 0 || len(check.Argv) > maxCandidateItems {
				return fmt.Errorf("invalid or duplicate check")
			}
			checks[check.ID] = true
			for _, arg := range check.Argv {
				if !candidateText(arg) {
					return fmt.Errorf("invalid command argument")
				}
			}
		}
		risks := map[string]bool{}
		for _, risk := range s.Risks {
			if !candidateText(risk.Risk) || !candidateText(risk.Mitigation) || risks[risk.Risk] {
				return fmt.Errorf("invalid or duplicate risk")
			}
			risks[risk.Risk] = true
		}
		if err := candidateEnvironment(s.AcceptanceEnvironment); err != nil {
			return err
		}
	}
	return nil
}

func resolvedEnvironmentText(value string) bool {
	if !candidateText(value) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tbd", "todo", "unknown", "unresolved", "to be decided", "to be determined", "pending", "not specified", "n/a":
		return false
	}
	return true
}

func candidateOS(values []string) bool {
	if !candidateStrings(values, false) {
		return false
	}
	for _, value := range values {
		if value != "linux" && value != "macos" && value != "windows" {
			return false
		}
	}
	return true
}

func candidateEnvironment(e protocol.Environment) error {
	if !candidateOS(e.OS) || !candidateGo.MatchString(e.Go) || !resolvedEnvironmentText(e.Network) || !resolvedEnvironmentText(e.Credentials) || !resolvedEnvironmentText(e.Fixtures) {
		return fmt.Errorf("unresolved acceptance environment")
	}
	return nil
}

// ValidateCandidate proves explicit coverage and graph consistency only. It does
// not execute checks, interpret requirement prose, approve scope, or infer a new
// integration requirement. The approved design must already name "integration".
func ValidateCandidate(c Candidate, requirements []design.Requirement, env startup.Environment) (Summary, error) {
	var zero Summary
	if err := candidateShape(c); err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(c)
	if err != nil || len(encoded) > maxCandidateBytes {
		return zero, fmt.Errorf("candidate outside byte bound")
	}
	if len(requirements) == 0 || len(requirements) > maxCandidateItems || !candidateOS(env.OperatingSystems) || !resolvedEnvironmentText(env.Runtime) || !resolvedEnvironmentText(env.AcceptanceTarget) {
		return zero, fmt.Errorf("invalid approved requirements or environment")
	}
	approved := map[string]bool{}
	for _, requirement := range requirements {
		if !candidateText(requirement.ID) || !candidateText(requirement.Statement) || approved[requirement.ID] {
			return zero, fmt.Errorf("invalid or duplicate approved requirement")
		}
		approved[requirement.ID] = true
	}
	if !approved["integration"] {
		return zero, fmt.Errorf("approved design lacks explicit integration requirement")
	}
	allowedOS := map[string]bool{}
	for _, os := range env.OperatingSystems {
		allowedOS[os] = true
	}
	slices := map[string]Slice{}
	owners := map[string]Ownership{}
	result := Summary{Coverage: []Ownership{}, Order: []string{}, Edges: []Edge{}}
	for _, s := range c.Contracts {
		slices[s.Key] = s
		if len(s.AcceptanceEnvironment.OS) != len(allowedOS) {
			return zero, fmt.Errorf("slice OS coverage differs from fixed environment")
		}
		for _, os := range s.AcceptanceEnvironment.OS {
			if !allowedOS[os] {
				return zero, fmt.Errorf("slice OS differs from fixed environment")
			}
		}
		if s.Kind == "integration" {
			if result.Integration != "" {
				return zero, fmt.Errorf("multiple integration slices")
			}
			result.Integration = s.Key
		}
		owned, checks, usedChecks := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, check := range s.Verification {
			checks[check.ID] = true
		}
		for _, requirement := range s.Requirements {
			if !approved[requirement] {
				return zero, fmt.Errorf("slice invents a requirement")
			}
			if _, exists := owners[requirement]; exists {
				return zero, fmt.Errorf("requirement has multiple owners")
			}
			if requirement == "integration" && s.Kind != "integration" {
				return zero, fmt.Errorf("implementation owns integration requirement")
			}
			owned[requirement] = true
			owners[requirement] = Ownership{Requirement: requirement, Slice: s.Key, Criteria: []string{}}
		}
		if s.Kind == "integration" && !owned["integration"] {
			return zero, fmt.Errorf("integration slice lacks integration requirement")
		}
		for _, ac := range s.Acceptance {
			for _, requirement := range ac.Requirements {
				if !owned[requirement] {
					return zero, fmt.Errorf("criterion references an unowned requirement")
				}
				owner := owners[requirement]
				owner.Criteria = append(owner.Criteria, ac.ID)
				owners[requirement] = owner
			}
			for _, check := range ac.Checks {
				if !checks[check] {
					return zero, fmt.Errorf("criterion references an unknown check")
				}
				usedChecks[check] = true
			}
		}
		if len(usedChecks) != len(checks) {
			return zero, fmt.Errorf("check is not traceable to a criterion")
		}
	}
	if result.Integration == "" || len(owners) != len(approved) {
		return zero, fmt.Errorf("missing integration or requirement ownership")
	}
	for _, owner := range owners {
		if len(owner.Criteria) == 0 {
			return zero, fmt.Errorf("requirement lacks criterion coverage")
		}
		sort.Strings(owner.Criteria)
		result.Coverage = append(result.Coverage, owner)
	}
	sort.Slice(result.Coverage, func(i, j int) bool { return result.Coverage[i].Requirement < result.Coverage[j].Requirement })
	indegree, followers := map[string]int{}, map[string][]string{}
	for key, s := range slices {
		indegree[key] = len(s.Dependencies)
		for _, dependency := range s.Dependencies {
			if _, ok := slices[dependency]; !ok || dependency == key {
				return zero, fmt.Errorf("unknown or self dependency")
			}
			followers[dependency] = append(followers[dependency], key)
			result.Edges = append(result.Edges, Edge{From: dependency, To: key})
		}
	}
	ready := []string{}
	for key, n := range indegree {
		if n == 0 {
			ready = append(ready, key)
		}
	}
	for len(ready) != 0 {
		sort.Strings(ready)
		key := ready[0]
		ready = ready[1:]
		result.Order = append(result.Order, key)
		for _, next := range followers[key] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if len(result.Order) != len(slices) {
		return zero, fmt.Errorf("cyclic slice dependencies")
	}
	seen, pending := map[string]bool{}, append([]string(nil), slices[result.Integration].Dependencies...)
	for len(pending) > 0 {
		key := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[key] {
			continue
		}
		seen[key] = true
		pending = append(pending, slices[key].Dependencies...)
	}
	if len(seen) != len(slices)-1 {
		return zero, fmt.Errorf("integration does not depend on every other slice")
	}
	sort.Slice(result.Edges, func(i, j int) bool {
		if result.Edges[i].From != result.Edges[j].From {
			return result.Edges[i].From < result.Edges[j].From
		}
		return result.Edges[i].To < result.Edges[j].To
	})
	return result, nil
}

// Schema follows the same strict structured-output subset as design.Schema:
// objects close additional properties and require every declared property.
func Schema() []byte {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	array := func(item any) map[string]any { return map[string]any{"type": "array", "items": item} }
	object := func(properties map[string]any) map[string]any {
		required := make([]string, 0, len(properties))
		for key := range properties {
			required = append(required, key)
		}
		sort.Strings(required)
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	criterion := object(map[string]any{"id": str(), "statement": str(), "requirements": array(str()), "checks": array(str())})
	check := object(map[string]any{"id": str(), "argv": array(str())})
	environment := object(map[string]any{"os": array(map[string]any{"type": "string", "enum": []string{"linux", "macos", "windows"}}), "go": str(), "network": str(), "credentials": str(), "fixtures": str()})
	risk := object(map[string]any{"risk": str(), "mitigation": str()})
	slice := object(map[string]any{
		"key": str(), "kind": map[string]any{"type": "string", "enum": []string{"implementation", "integration"}}, "goal": str(), "scope": array(str()), "non_goals": array(str()),
		"requirements": array(str()), "acceptance": array(criterion), "verification": array(check), "acceptance_environment": environment, "impact_surface": array(str()), "risks": array(risk), "dependencies": array(str()),
		"candidate_binding": map[string]any{"type": "string", "enum": []string{"none", "exact_source_artifact_environment"}},
	})
	raw, _ := json.Marshal(object(map[string]any{"schema": map[string]any{"type": "string", "enum": []string{CandidateSchema}}, "contracts": array(slice), "open_decisions": array(str())}))
	canonical, _ := protocol.CanonicalJSON(raw)
	return canonical
}
