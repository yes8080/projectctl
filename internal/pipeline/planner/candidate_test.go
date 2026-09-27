package planner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/design"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
	"github.com/yes8080/projectctl/internal/pipeline/startup"
)

func candidateFixture() (Candidate, []design.Requirement, startup.Environment) {
	slice := func(key, requirement string) Slice {
		return Slice{Key: key, Kind: "implementation", Goal: "Implement " + requirement, Scope: []string{"bounded capability"}, NonGoals: []string{"unapproved scope"}, Requirements: []string{requirement}, Acceptance: []Criterion{{ID: "AC-1", Statement: "demonstrate " + requirement, Requirements: []string{requirement}, Checks: []string{"unit"}}}, Verification: []protocol.Check{{ID: "unit", Argv: []string{"go", "test", "./..."}}}, AcceptanceEnvironment: protocol.Environment{OS: []string{"linux", "macos", "windows"}, Go: "1.22", Network: "mocked", Credentials: "synthetic fixtures only", Fixtures: "integration-fixture"}, ImpactSurface: []string{"internal/product"}, Risks: []protocol.Risk{{Risk: "regression", Mitigation: "explicit tests"}}, Dependencies: []string{}, CandidateBinding: "none"}
	}
	a, b, integration := slice("alpha", "REQ-A"), slice("beta", "REQ-B"), slice("integrate", "integration")
	b.Dependencies = []string{"alpha"}
	integration.Kind, integration.CandidateBinding, integration.Dependencies = "integration", "exact_source_artifact_environment", []string{"beta"}
	c := Candidate{Schema: CandidateSchema, Contracts: []Slice{a, b, integration}, OpenDecisions: []string{}}
	requirements := []design.Requirement{{ID: "REQ-A", Statement: "A capability"}, {ID: "REQ-B", Statement: "B capability"}, {ID: "integration", Statement: "Verify the exact integrated source, artifact and environment"}}
	env := startup.Environment{OperatingSystems: []string{"linux", "macos", "windows"}, Runtime: "codex-exec", AcceptanceTarget: "integration-fixture", WorkspaceIsolation: "isolated", CredentialIsolation: "controller_only", ExternalUsageLimit: "bounded"}
	return c, requirements, env
}

func candidateJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCandidateStrictRoundTripAndDerivedSummary(t *testing.T) {
	c, requirements, env := candidateFixture()
	decoded, err := DecodeCandidate(candidateJSON(t, c))
	if err != nil || !reflect.DeepEqual(c, decoded) {
		t.Fatalf("round trip: %v", err)
	}
	before := candidateJSON(t, c)
	summary, err := ValidateCandidate(c, requirements, env)
	want := Summary{Coverage: []Ownership{{Requirement: "REQ-A", Slice: "alpha", Criteria: []string{"AC-1"}}, {Requirement: "REQ-B", Slice: "beta", Criteria: []string{"AC-1"}}, {Requirement: "integration", Slice: "integrate", Criteria: []string{"AC-1"}}}, Order: []string{"alpha", "beta", "integrate"}, Edges: []Edge{{From: "alpha", To: "beta"}, {From: "beta", To: "integrate"}}, Integration: "integrate"}
	if err != nil || !reflect.DeepEqual(summary, want) {
		t.Fatalf("summary %#v err=%v", summary, err)
	}
	if !bytes.Equal(before, candidateJSON(t, c)) {
		t.Fatal("pure validation mutated candidate")
	}
	summary.Coverage[0].Criteria[0] = "mutated"
	again, err := ValidateCandidate(c, requirements, env)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("summary exposed mutable candidate aliases")
	}
}

func TestCandidateStrictWireRejections(t *testing.T) {
	c, _, _ := candidateFixture()
	valid := string(candidateJSON(t, c))
	tests := map[string]string{
		"unknown root":         strings.Replace(valid, `"schema":`, `"actor":"planner","schema":`, 1),
		"unknown nested":       strings.Replace(valid, `"goal":`, `"approved":true,"goal":`, 1),
		"duplicate":            strings.Replace(valid, `"goal":`, `"goal":"other","goal":`, 1),
		"escaped duplicate":    strings.Replace(valid, `"goal":`, `"\u0067oal":"other","goal":`, 1),
		"case alias":           strings.Replace(valid, `"scope":`, `"Scope":`, 1),
		"check case alias":     strings.Replace(valid, `"argv":`, `"Argv":`, 1),
		"nested null":          strings.Replace(valid, `"argv":["go","test","./..."]`, `"argv":null`, 1),
		"null array member":    strings.Replace(valid, `"scope":["bounded capability"]`, `"scope":[null]`, 1),
		"null decisions":       strings.Replace(valid, `"open_decisions":[]`, `"open_decisions":null`, 1),
		"omitted check ID":     strings.Replace(valid, `"id":"unit",`, ``, 1),
		"omitted root":         strings.Replace(valid, `"open_decisions":[],`, ``, 1),
		"wrong primitive type": strings.Replace(valid, `"key":"alpha"`, `"key":1`, 1),
		"second document":      valid + `{}`,
		"invalid unicode":      strings.Replace(valid, `"goal":`, `"goal":"\ud800","old_goal":`, 1),
		"root null":            `null`,
		"root array":           `[]`,
		"empty":                ``,
	}
	// open_decisions is the final JSON key in this Go struct; remove it explicitly.
	tests["omitted root"] = strings.Replace(valid, `,"open_decisions":[]`, ``, 1)
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := DecodeCandidate([]byte(raw)); err == nil || !reflect.DeepEqual(got, Candidate{}) {
				t.Fatal("ambiguous candidate accepted")
			}
		})
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(valid), &object); err != nil {
		t.Fatal(err)
	}
	for key := range object {
		t.Run("missing "+key, func(t *testing.T) {
			copy := map[string]json.RawMessage{}
			for k, v := range object {
				if k != key {
					copy[k] = v
				}
			}
			if _, err := DecodeCandidate(candidateJSON(t, copy)); err == nil {
				t.Fatal("required root field omitted")
			}
		})
	}
}

func TestCandidateSemanticRejections(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Candidate, *[]design.Requirement, *startup.Environment)
	}{
		{"schema", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Schema = "projectctl.plan-candidate/v2"
		}},
		{"empty contracts", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts = []Slice{} }},
		{"open decision", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.OpenDecisions = []string{"choose architecture"}
		}},
		{"nil decisions", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.OpenDecisions = nil }},
		{"duplicate key", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts[1].Key = "alpha" }},
		{"unsafe key", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts[0].Key = "a key" }},
		{"unknown kind", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts[0].Kind = "epic" }},
		{"wrong implementation binding", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].CandidateBinding = "exact_source_artifact_environment"
		}},
		{"wrong integration binding", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[2].CandidateBinding = "none"
		}},
		{"blank goal", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts[0].Goal = " \n" }},
		{"invalid UTF8", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Goal = string([]byte{255})
		}},
		{"blank scope", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Scope = []string{""}
		}},
		{"nil non goals", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts[0].NonGoals = nil }},
		{"duplicate scope", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Scope = []string{"same", "same"}
		}},
		{"duplicate local requirement", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Requirements = []string{"REQ-A", "REQ-A"}
		}},
		{"multiple owners", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[1].Requirements = append(c.Contracts[1].Requirements, "REQ-A")
		}},
		{"invented requirement", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Requirements = []string{"invented"}
		}},
		{"missing owner", func(_ *Candidate, r *[]design.Requirement, _ *startup.Environment) {
			*r = append(*r, design.Requirement{ID: "REQ-C", Statement: "required C"})
		}},
		{"missing integration baseline", func(_ *Candidate, r *[]design.Requirement, _ *startup.Environment) { *r = (*r)[:2] }},
		{"duplicate baseline requirement", func(_ *Candidate, r *[]design.Requirement, _ *startup.Environment) { *r = append(*r, (*r)[0]) }},
		{"blank baseline statement", func(_ *Candidate, r *[]design.Requirement, _ *startup.Environment) { (*r)[0].Statement = "" }},
		{"duplicate AC", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Acceptance = append(c.Contracts[0].Acceptance, c.Contracts[0].Acceptance[0])
		}},
		{"empty AC", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts[0].Acceptance = nil }},
		{"blank AC", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Acceptance[0].Statement = ""
		}},
		{"AC missing requirements", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Acceptance[0].Requirements = []string{}
		}},
		{"AC duplicate requirement", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Acceptance[0].Requirements = []string{"REQ-A", "REQ-A"}
		}},
		{"AC unowned requirement", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Acceptance[0].Requirements = []string{"REQ-B"}
		}},
		{"AC missing checks", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Acceptance[0].Checks = nil
		}},
		{"AC unknown check", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Acceptance[0].Checks = []string{"missing"}
		}},
		{"AC duplicate check", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Acceptance[0].Checks = []string{"unit", "unit"}
		}},
		{"duplicate check", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Verification = append(c.Contracts[0].Verification, c.Contracts[0].Verification[0])
		}},
		{"empty check ID", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Verification[0].ID = ""
		}},
		{"unused check", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Verification = append(c.Contracts[0].Verification, protocol.Check{ID: "unused", Argv: []string{"go", "vet"}})
		}},
		{"empty argv", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Verification[0].Argv = []string{}
		}},
		{"empty arg", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Verification[0].Argv = []string{"go", ""}
		}},
		{"nul arg", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Verification[0].Argv = []string{"go\x00test"}
		}},
		{"nil risks", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts[0].Risks = nil }},
		{"missing mitigation", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Risks[0].Mitigation = ""
		}},
		{"empty impact", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].ImpactSurface = nil
		}},
		{"nil deps", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) { c.Contracts[0].Dependencies = nil }},
		{"duplicate deps", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[1].Dependencies = []string{"alpha", "alpha"}
		}},
		{"dangling dependency", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[1].Dependencies = []string{"missing"}
		}},
		{"self dependency", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Dependencies = []string{"alpha"}
		}},
		{"cycle", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Dependencies = []string{"integrate"}
		}},
		{"missing integration slice", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[2].Kind, c.Contracts[2].CandidateBinding = "implementation", "none"
		}},
		{"excess integration", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].Kind, c.Contracts[0].CandidateBinding = "integration", "exact_source_artifact_environment"
		}},
		{"integration missing dependency", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[2].Dependencies = []string{"alpha"}
		}},
		{"integration unbound", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[2].Dependencies = []string{}
		}},
		{"runtime unresolved", func(_ *Candidate, _ *[]design.Requirement, e *startup.Environment) { e.Runtime = "TBD" }},
		{"target unresolved", func(_ *Candidate, _ *[]design.Requirement, e *startup.Environment) { e.AcceptanceTarget = "unknown" }},
		{"policy OS unknown", func(_ *Candidate, _ *[]design.Requirement, e *startup.Environment) {
			e.OperatingSystems = []string{"android"}
		}},
		{"policy OS duplicate", func(_ *Candidate, _ *[]design.Requirement, e *startup.Environment) {
			e.OperatingSystems = []string{"linux", "linux"}
		}},
		{"slice OS unknown", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].AcceptanceEnvironment.OS = []string{"Android"}
		}},
		{"slice OS narrows", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].AcceptanceEnvironment.OS = []string{"linux"}
		}},
		{"slice OS duplicate", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].AcceptanceEnvironment.OS = []string{"linux", "linux", "windows"}
		}},
		{"Go unresolved", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].AcceptanceEnvironment.Go = "latest"
		}},
		{"Go arbitrary prose", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].AcceptanceEnvironment.Go = "whatever is installed"
		}},
		{"network unresolved", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].AcceptanceEnvironment.Network = "TBD"
		}},
		{"credentials unresolved", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].AcceptanceEnvironment.Credentials = "unknown"
		}},
		{"fixtures unresolved", func(c *Candidate, _ *[]design.Requirement, _ *startup.Environment) {
			c.Contracts[0].AcceptanceEnvironment.Fixtures = "pending"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, requirements, env := candidateFixture()
			test.change(&c, &requirements, &env)
			if got, err := ValidateCandidate(c, requirements, env); err == nil || !reflect.DeepEqual(got, Summary{}) {
				t.Fatal("invalid candidate produced an authoritative-looking summary")
			}
		})
	}
}

func TestCandidateCollectionAndByteBounds(t *testing.T) {
	c, requirements, env := candidateFixture()
	c.Contracts[0].Scope = make([]string, maxCandidateItems+1)
	for i := range c.Contracts[0].Scope {
		c.Contracts[0].Scope[i] = fmt.Sprint("scope-", i)
	}
	if _, err := DecodeCandidate(candidateJSON(t, c)); err == nil {
		t.Fatal("oversized decoded collection accepted")
	}
	if _, err := ValidateCandidate(c, requirements, env); err == nil {
		t.Fatal("oversized typed collection accepted")
	}
	c, requirements, env = candidateFixture()
	c.Contracts[0].Goal = strings.Repeat("x", maxCandidateBytes)
	if _, err := DecodeCandidate(candidateJSON(t, c)); err == nil {
		t.Fatal("oversized JSON accepted")
	}
	if _, err := ValidateCandidate(c, requirements, env); err == nil {
		t.Fatal("oversized typed candidate accepted")
	}
	valid, _, _ := candidateFixture()
	padding := append(candidateJSON(t, valid), bytes.Repeat([]byte(" "), maxCandidateBytes)...)
	if _, err := DecodeCandidate(padding); err == nil {
		t.Fatal("whitespace bypassed wire byte bound")
	}
}

func TestCandidateUncoveredOwnedRequirementAndExplicitIntegrationOwner(t *testing.T) {
	c, requirements, env := candidateFixture()
	requirements = append(requirements, design.Requirement{ID: "REQ-C", Statement: "new explicit C"})
	c.Contracts[0].Requirements = append(c.Contracts[0].Requirements, "REQ-C")
	if _, err := ValidateCandidate(c, requirements, env); err == nil {
		t.Fatal("ownership without AC coverage accepted")
	}
	c.Contracts[0].Acceptance[0].Requirements = append(c.Contracts[0].Acceptance[0].Requirements, "REQ-C")
	if _, err := ValidateCandidate(c, requirements, env); err != nil {
		t.Fatal("one AC may cover multiple explicit requirements", err)
	}
	c, requirements, env = candidateFixture()
	c.Contracts[0].Requirements, c.Contracts[2].Requirements = []string{"integration"}, []string{"REQ-A"}
	c.Contracts[0].Acceptance[0].Requirements, c.Contracts[2].Acceptance[0].Requirements = []string{"integration"}, []string{"REQ-A"}
	if _, err := ValidateCandidate(c, requirements, env); err == nil {
		t.Fatal("integration requirement reassigned to implementation")
	}
}

func TestCandidateSummaryDeterministicUnderPermutations(t *testing.T) {
	c, requirements, env := candidateFixture()
	// Diamond permits multiple ready slices; lexicographic order is canonical.
	c.Contracts[1].Dependencies = []string{}
	c.Contracts[2].Dependencies = []string{"beta", "alpha"}
	c.Contracts[0].Acceptance = append(c.Contracts[0].Acceptance, Criterion{ID: "AC-2", Statement: "second proof", Requirements: []string{"REQ-A"}, Checks: []string{"unit"}})
	want, err := ValidateCandidate(c, requirements, env)
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(8))
	for i := 0; i < 100; i++ {
		random.Shuffle(len(c.Contracts), func(i, j int) { c.Contracts[i], c.Contracts[j] = c.Contracts[j], c.Contracts[i] })
		random.Shuffle(len(requirements), func(i, j int) { requirements[i], requirements[j] = requirements[j], requirements[i] })
		for j := range c.Contracts {
			s := &c.Contracts[j]
			random.Shuffle(len(s.Dependencies), func(i, j int) { s.Dependencies[i], s.Dependencies[j] = s.Dependencies[j], s.Dependencies[i] })
			random.Shuffle(len(s.Acceptance), func(i, j int) { s.Acceptance[i], s.Acceptance[j] = s.Acceptance[j], s.Acceptance[i] })
			random.Shuffle(len(s.AcceptanceEnvironment.OS), func(i, j int) {
				s.AcceptanceEnvironment.OS[i], s.AcceptanceEnvironment.OS[j] = s.AcceptanceEnvironment.OS[j], s.AcceptanceEnvironment.OS[i]
			})
		}
		got, err := ValidateCandidate(c, requirements, env)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("permutation %d: %#v %v", i, got, err)
		}
	}
}

func TestCandidateGeneratedDAGOrderAndIntegrationReachability(t *testing.T) {
	for n := 1; n <= 24; n++ {
		c, _, env := candidateFixture()
		prototype := c.Contracts[0]
		c.Contracts = []Slice{}
		requirements := []design.Requirement{}
		for i := 0; i < n; i++ {
			s := prototype
			s.Key = fmt.Sprintf("slice-%02d", i)
			req := fmt.Sprint("REQ-", i)
			s.Requirements = []string{req}
			s.Acceptance = []Criterion{{ID: "AC", Statement: "explicit result", Requirements: []string{req}, Checks: []string{"unit"}}}
			s.Dependencies = []string{}
			if i > 0 {
				s.Dependencies = []string{fmt.Sprintf("slice-%02d", i-1)}
			}
			if i == n-1 {
				req = "integration"
				s.Kind, s.CandidateBinding = "integration", "exact_source_artifact_environment"
				s.Requirements, s.Acceptance[0].Requirements = []string{req}, []string{req}
			}
			requirements = append(requirements, design.Requirement{ID: req, Statement: "approved statement"})
			c.Contracts = append(c.Contracts, s)
		}
		summary, err := ValidateCandidate(c, requirements, env)
		if err != nil || len(summary.Order) != n || len(summary.Coverage) != n || len(summary.Edges) != n-1 {
			t.Fatalf("DAG size %d: %v", n, err)
		}
		position := map[string]int{}
		for i, key := range summary.Order {
			position[key] = i
		}
		for _, edge := range summary.Edges {
			if position[edge.From] >= position[edge.To] {
				t.Fatal("dependency ordered after its dependent")
			}
		}
		if n > 1 {
			c.Contracts[0].Dependencies = []string{c.Contracts[n-1].Key}
			if _, err := ValidateCandidate(c, requirements, env); err == nil {
				t.Fatal("generated cycle accepted")
			}
		}
	}
}

func TestCandidateSchemaClosesEveryObjectAndRequiresEveryProperty(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(Schema(), &root); err != nil {
		t.Fatal(err)
	}
	var check func(map[string]any)
	check = func(node map[string]any) {
		switch node["type"] {
		case "object":
			properties := node["properties"].(map[string]any)
			required := node["required"].([]any)
			if node["additionalProperties"] != false || len(properties) != len(required) {
				t.Fatal("open or optional structured-output object")
			}
			for _, key := range required {
				if _, ok := properties[key.(string)]; !ok {
					t.Fatal("required property absent")
				}
			}
			for _, value := range properties {
				check(value.(map[string]any))
			}
		case "array":
			check(node["items"].(map[string]any))
		}
	}
	check(root)
	first := Schema()
	for i := 0; i < 20; i++ {
		if !bytes.Equal(first, Schema()) {
			t.Fatal("schema is nondeterministic")
		}
	}
	first[0] = 'x'
	if !json.Valid(Schema()) {
		t.Fatal("schema exposes shared mutable bytes")
	}
}
