package startup

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func policyTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func policyTestObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func policyTestAt(t *testing.T, object any, path string) any {
	t.Helper()
	if path == "" {
		return object
	}
	for _, key := range strings.Split(path, ".") {
		switch value := object.(type) {
		case map[string]any:
			object = value[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(value) {
				t.Fatalf("invalid fixture path %q", path)
			}
			object = value[i]
		default:
			t.Fatalf("invalid fixture traversal %q", path)
		}
	}
	return object
}

func policyTestFieldPaths(object any, prefix string) []string {
	var paths []string
	switch value := object.(type) {
	case map[string]any:
		for key, child := range value {
			p := key
			if prefix != "" {
				p = prefix + "." + key
			}
			paths = append(paths, p)
			paths = append(paths, policyTestFieldPaths(child, p)...)
		}
	case []any:
		for i, child := range value {
			paths = append(paths, policyTestFieldPaths(child, fmt.Sprintf("%s.%d", prefix, i))...)
		}
	}
	sort.Strings(paths)
	return paths
}

func policyTestReject(t *testing.T, m Manifest) {
	t.Helper()
	if err := m.Validate(); err == nil {
		t.Fatal("invalid manifest accepted by Validate")
	}
	if _, err := DecodeManifest(policyTestJSON(t, m)); err == nil {
		t.Fatal("invalid manifest accepted by DecodeManifest")
	}
}

func TestPolicyManifestCompleteAndExplicitZeroLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"integrated code with explicit zero allowances", func(*Manifest) {}},
		{"bounded positive allowances", func(m *Manifest) {
			m.Policy.Budget.MaxDefectFixIssues, m.Policy.Budget.MaxChangeRequests, m.Policy.Budget.MaxCostMinorUnits = 2, 1, 150
		}},
		{"authorized deployment", func(m *Manifest) {
			m.Policy.Completion.Endpoint = "deployed_and_verified"
			m.Policy.Completion.Deployment = &Deployment{Authorized: true, Target: "staging", RollbackPolicy: "restore previous immutable revision"}
		}},
		{"development document only", func(m *Manifest) { m.Inputs[0].Kind = "development" }},
		{"all known capability names", func(m *Manifest) {
			m.Policy.RequiredGitHubCapabilities = []string{"issues_read", "comments_read", "milestones_read", "dependencies_read", "issues_enabled", "delete_branch_on_merge", "auto_merge", "push_permission", "admin_permission", "issue_auto_close", "protected_merge_gate"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := fixtureManifest()
			test.mutate(&m)
			if err := m.Validate(); err != nil {
				t.Fatal(err)
			}
			got, err := DecodeManifest(policyTestJSON(t, m))
			if err != nil || !reflect.DeepEqual(got, m) {
				t.Fatalf("complete explicit policy failed roundtrip: %v", err)
			}
		})
	}
}

func TestPolicyManifestRequiresEveryDeclaredField(t *testing.T) {
	base := policyTestJSON(t, fixtureManifest())
	paths := policyTestFieldPaths(policyTestObject(t, base), "")
	for _, path := range paths {
		for _, mode := range []string{"omitted", "null"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				object := policyTestObject(t, base)
				parentPath, key := "", path
				if i := strings.LastIndex(path, "."); i >= 0 {
					parentPath, key = path[:i], path[i+1:]
				}
				parent := policyTestAt(t, object, parentPath).(map[string]any)
				if mode == "omitted" {
					delete(parent, key)
				} else {
					parent[key] = nil
				}
				if _, err := DecodeManifest(policyTestJSON(t, object)); err == nil {
					t.Fatalf("%s required field %s was silently defaulted", mode, path)
				}
			})
		}
	}
	// Zero cost / bug-fix / change limits are deliberately present in the base
	// manifest. The matrix above rejects their absence, not their explicit zero.
	for _, key := range []string{"authorized", "target", "rollback_policy"} {
		t.Run("deployment/"+key, func(t *testing.T) {
			m := fixtureManifest()
			m.Policy.Completion.Endpoint = "deployed_and_verified"
			m.Policy.Completion.Deployment = &Deployment{Authorized: true, Target: "staging", RollbackPolicy: "rollback"}
			object := policyTestObject(t, policyTestJSON(t, m))
			delete(policyTestAt(t, object, "policy.completion.deployment").(map[string]any), key)
			if _, err := DecodeManifest(policyTestJSON(t, object)); err == nil {
				t.Fatal("deployment missing required field accepted")
			}
		})
	}
}

func TestPolicyManifestStrictWireSchema(t *testing.T) {
	base := policyTestJSON(t, fixtureManifest())
	for _, path := range []string{"", "repository", "inputs.0", "inputs.0.source", "policy", "policy.roles", "policy.roles.controller", "policy.budget", "policy.completion", "policy.environment"} {
		t.Run("unknown at "+path, func(t *testing.T) {
			object := policyTestObject(t, base)
			policyTestAt(t, object, path).(map[string]any)["unknown"] = true
			if _, err := DecodeManifest(policyTestJSON(t, object)); err == nil {
				t.Fatal("unknown nested field accepted")
			}
		})
	}
	for _, test := range []struct{ name, old, replacement string }{
		{"duplicate root", `"version":1`, `"version":1,"version":1`},
		{"duplicate nested zero", `"max_cost_minor_units":0`, `"max_cost_minor_units":0,"max_cost_minor_units":0`},
		{"escaped duplicate", `"goal":`, `"\u0067oal":"injected","goal":`},
		{"case alias", `"goal":`, `"Goal":"injected","goal":`},
		{"alias replacement", `"max_cost_minor_units":0`, `"Max_Cost_Minor_Units":0`},
		{"fractional budget", `"max_agent_runs":20`, `"max_agent_runs":20.0`},
		{"exponent budget", `"max_agent_runs":20`, `"max_agent_runs":2e1`},
		{"overflow budget", `"max_agent_runs":20`, `"max_agent_runs":9223372036854775808`},
		{"negative zero", `"max_cost_minor_units":0`, `"max_cost_minor_units":-0`},
		{"string budget", `"max_agent_runs":20`, `"max_agent_runs":"20"`},
		{"lone surrogate", `"goal":"deliver bounded product"`, `"goal":"\ud800"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(string(base), test.old) {
				t.Fatal("fixture token missing")
			}
			changed := strings.Replace(string(base), test.old, test.replacement, 1)
			if _, err := DecodeManifest([]byte(changed)); err == nil {
				t.Fatal("ambiguous or unsupported JSON accepted")
			}
		})
	}
	for _, body := range [][]byte{nil, []byte("null"), []byte("[]"), append(append([]byte(nil), base...), []byte(" {}")...), append([]byte{0xff}, base...)} {
		if _, err := DecodeManifest(body); err == nil {
			t.Fatal("invalid document or trailing JSON accepted")
		}
	}
}

func TestPolicyManifestRejectsInvalidBudgets(t *testing.T) {
	for _, field := range []struct {
		name      string
		value     func(*protocol.Budget) *int64
		allowZero bool
	}{
		{"agent runs", func(b *protocol.Budget) *int64 { return &b.MaxAgentRuns }, false},
		{"run seconds", func(b *protocol.Budget) *int64 { return &b.MaxRunSeconds }, false},
		{"wall seconds", func(b *protocol.Budget) *int64 { return &b.MaxWallSeconds }, false},
		{"design rounds", func(b *protocol.Budget) *int64 { return &b.MaxDesignRounds }, false},
		{"planning rounds", func(b *protocol.Budget) *int64 { return &b.MaxPlanningRounds }, false},
		{"implementation rounds", func(b *protocol.Budget) *int64 { return &b.MaxImplementationRounds }, false},
		{"defect fix issues", func(b *protocol.Budget) *int64 { return &b.MaxDefectFixIssues }, true},
		{"change requests", func(b *protocol.Budget) *int64 { return &b.MaxChangeRequests }, true},
		{"cost minor units", func(b *protocol.Budget) *int64 { return &b.MaxCostMinorUnits }, true},
	} {
		for _, value := range []int64{-1, 0} {
			t.Run(fmt.Sprintf("%s/%d", field.name, value), func(t *testing.T) {
				m := fixtureManifest()
				*field.value(&m.Policy.Budget) = value
				if value == 0 && field.allowZero {
					if _, err := DecodeManifest(policyTestJSON(t, m)); err != nil {
						t.Fatalf("explicit zero allowance rejected: %v", err)
					}
				} else {
					policyTestReject(t, m)
				}
			})
		}
	}
}

func TestPolicyManifestSemanticFailureMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"wrong schema", func(m *Manifest) { m.Schema = "projectctl.policy/v1" }},
		{"zero version", func(m *Manifest) { m.Version = 0 }},
		{"missing repository stable id", func(m *Manifest) { m.Repository.DatabaseID = 0 }},
		{"missing repository node", func(m *Manifest) { m.Repository.NodeID = " " }},
		{"repository path escape", func(m *Manifest) { m.Repository.Name = "../other" }},
		{"empty cycle", func(m *Manifest) { m.Cycle = "" }},
		{"empty operation", func(m *Manifest) { m.OperationID = "" }},
		{"missing inputs", func(m *Manifest) { m.Inputs = nil }},
		{"unknown input kind", func(m *Manifest) { m.Inputs[0].Kind = "external_link" }},
		{"constraints only", func(m *Manifest) { m.Inputs[0].Kind = "constraints" }},
		{"duplicate input identity", func(m *Manifest) { m.Inputs = append(m.Inputs, m.Inputs[0]) }},
		{"duplicate input different digest", func(m *Manifest) {
			in := m.Inputs[0]
			in.Kind = "development"
			in.Source.SHA256 = strings.Repeat("b", 64)
			m.Inputs = append(m.Inputs, in)
		}},
		{"empty goal", func(m *Manifest) { m.Policy.Goal = " \n" }},
		{"empty non-goals", func(m *Manifest) { m.Policy.NonGoals = nil }},
		{"duplicate non-goals", func(m *Manifest) { m.Policy.NonGoals = []string{"same", "same"} }},
		{"blank non-goal", func(m *Manifest) { m.Policy.NonGoals = []string{" "} }},
		{"store authorization absent", func(m *Manifest) { m.Policy.StoreInputsAuthorized = false }},
		{"write authorization absent", func(m *Manifest) { m.Policy.RemoteWritesAuthorized = false }},
		{"role identity missing", func(m *Manifest) { m.Policy.Roles.Acceptor.ID = 0 }},
		{"role native type invalid", func(m *Manifest) { m.Policy.Roles.Acceptor.Type = "actor" }},
		{"wall below run", func(m *Manifest) { m.Policy.Budget.MaxWallSeconds = m.Policy.Budget.MaxRunSeconds - 1 }},
		{"currency missing", func(m *Manifest) { m.Policy.Budget.Currency = "" }},
		{"currency not canonical", func(m *Manifest) { m.Policy.Budget.Currency = "usd" }},
		{"development parallelism missing", func(m *Manifest) { m.Policy.MaxParallelDevelopment = 0 }},
		{"development parallelism unbounded", func(m *Manifest) { m.Policy.MaxParallelDevelopment = m.Policy.Budget.MaxAgentRuns + 1 }},
		{"parallel merges", func(m *Manifest) { m.Policy.MaxParallelMerge = 2 }},
		{"merge budget missing", func(m *Manifest) { m.Policy.MaxParallelMerge = 0 }},
		{"completion missing", func(m *Manifest) { m.Policy.Completion = Completion{} }},
		{"completion only merged", func(m *Manifest) { m.Policy.Completion.Endpoint = "merged" }},
		{"completion criteria empty", func(m *Manifest) { m.Policy.Completion.Criteria = nil }},
		{"completion duplicate criteria", func(m *Manifest) { m.Policy.Completion.Criteria = []string{"same", "same"} }},
		{"integration disabled", func(m *Manifest) { m.Policy.Completion.RequireIntegration = false }},
		{"bug gate disabled", func(m *Manifest) { m.Policy.Completion.RequireBugGate = false }},
		{"remote cleanup disabled", func(m *Manifest) { m.Policy.Completion.RequireRemoteCleanup = false }},
		{"local cleanup disabled", func(m *Manifest) { m.Policy.Completion.RequireLocalCleanup = false }},
		{"implicit deployment", func(m *Manifest) {
			m.Policy.Completion.Deployment = &Deployment{Authorized: true, Target: "production", RollbackPolicy: "rollback"}
		}},
		{"deployment authorization missing", func(m *Manifest) {
			m.Policy.Completion.Endpoint = "deployed_and_verified"
			m.Policy.Completion.Deployment = &Deployment{Target: "production", RollbackPolicy: "rollback"}
		}},
		{"deployment target missing", func(m *Manifest) {
			m.Policy.Completion.Endpoint = "deployed_and_verified"
			m.Policy.Completion.Deployment = &Deployment{Authorized: true, RollbackPolicy: "rollback"}
		}},
		{"deployment rollback missing", func(m *Manifest) {
			m.Policy.Completion.Endpoint = "deployed_and_verified"
			m.Policy.Completion.Deployment = &Deployment{Authorized: true, Target: "production"}
		}},
		{"deployment scope absent", func(m *Manifest) { m.Policy.Completion.Endpoint = "deployed_and_verified" }},
		{"terminal conditions missing", func(m *Manifest) { m.Policy.TerminalConditions = nil }},
		{"unknown terminal condition", func(m *Manifest) { m.Policy.TerminalConditions = append(m.Policy.TerminalConditions, "merged") }},
		{"duplicate terminal condition", func(m *Manifest) { m.Policy.TerminalConditions = append(m.Policy.TerminalConditions, "completed") }},
		{"environment missing", func(m *Manifest) { m.Policy.Environment = Environment{} }},
		{"OS missing", func(m *Manifest) { m.Policy.Environment.OperatingSystems = nil }},
		{"unknown OS", func(m *Manifest) { m.Policy.Environment.OperatingSystems = []string{"unknown"} }},
		{"duplicate OS", func(m *Manifest) { m.Policy.Environment.OperatingSystems = []string{"linux", "linux"} }},
		{"runtime missing", func(m *Manifest) { m.Policy.Environment.Runtime = " " }},
		{"acceptance target missing", func(m *Manifest) { m.Policy.Environment.AcceptanceTarget = " " }},
		{"workspace not isolated", func(m *Manifest) { m.Policy.Environment.WorkspaceIsolation = "shared" }},
		{"credentials not isolated", func(m *Manifest) { m.Policy.Environment.CredentialIsolation = "agent" }},
		{"external usage bound missing", func(m *Manifest) { m.Policy.Environment.ExternalUsageLimit = " " }},
		{"capabilities missing", func(m *Manifest) { m.Policy.RequiredGitHubCapabilities = nil }},
		{"unknown capability", func(m *Manifest) {
			m.Policy.RequiredGitHubCapabilities = append(m.Policy.RequiredGitHubCapabilities, "repository_supported")
		}},
		{"duplicate capability", func(m *Manifest) {
			m.Policy.RequiredGitHubCapabilities = append(m.Policy.RequiredGitHubCapabilities, "issues_read")
		}},
		{"auto merge missing trusted gate", func(m *Manifest) {
			m.Policy.RequiredGitHubCapabilities = []string{"issues_read", "issues_enabled", "push_permission", "auto_merge"}
		}},
		{"design files missing", func(m *Manifest) { m.Policy.DesignFiles = nil }},
		{"design file duplicate", func(m *Manifest) { m.Policy.DesignFiles = []string{"docs/design.md", "docs/design.md"} }},
		{"product implementation in design files", func(m *Manifest) { m.Policy.DesignFiles = []string{"cmd/product/main.go"} }},
		{"design file traversal", func(m *Manifest) { m.Policy.DesignFiles = []string{"../design.md"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := fixtureManifest()
			test.mutate(&m)
			policyTestReject(t, m)
		})
	}
}

func TestPolicyManifestRequiresEachTerminalAndCoreCapability(t *testing.T) {
	base := fixtureManifest()
	for index, name := range base.Policy.TerminalConditions {
		t.Run("terminal "+name, func(t *testing.T) {
			m := fixtureManifest()
			m.Policy.TerminalConditions = append(m.Policy.TerminalConditions[:index], m.Policy.TerminalConditions[index+1:]...)
			policyTestReject(t, m)
		})
	}
	for _, name := range []string{"issues_read", "issues_enabled", "push_permission"} {
		t.Run("capability "+name, func(t *testing.T) {
			m := fixtureManifest()
			var retained []string
			for _, value := range m.Policy.RequiredGitHubCapabilities {
				if value != name {
					retained = append(retained, value)
				}
			}
			m.Policy.RequiredGitHubCapabilities = retained
			policyTestReject(t, m)
		})
	}
}

func TestPolicyManifestRoleIndependenceUsesNativeIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Roles)
	}{
		{"design author reviewer", func(r *Roles) { r.DesignReviewer = r.Designer; r.DesignReviewer.Login = "renamed-reviewer" }},
		{"developer acceptor", func(r *Roles) { r.Acceptor = r.Developer; r.Acceptor.Login = "renamed-acceptor" }},
		{"controller developer", func(r *Roles) { r.Developer = r.Controller; r.Developer.Login = "renamed-developer" }},
		{"controller acceptor", func(r *Roles) { r.Acceptor = r.Controller; r.Acceptor.Login = "renamed-acceptor" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := fixtureManifest()
			test.mutate(&m.Policy.Roles)
			policyTestReject(t, m)
		})
	}
}

func TestPolicyManifestRequiresImmutableSafeSources(t *testing.T) {
	for _, value := range []string{"", "main", "refs/heads/main", "v1", "abcd123", strings.Repeat("A", 40), strings.Repeat("g", 40)} {
		t.Run("commit "+value, func(t *testing.T) {
			m := fixtureManifest()
			m.Inputs[0].Source.Commit = value
			policyTestReject(t, m)
		})
	}
	for _, value := range []string{"", ".", "..", "../input.md", "/docs/input.md", "docs/../input.md", "docs//input.md", "docs/./input.md", "docs\\input.md", "docs/%2finput.md", "docs/%252finput.md", "C:/input.md", "docs/\x00input.md", "docs/\tinput.md", "docs/\x01input.md", "docs/\x7finput.md", strings.Repeat("a", 4097), strings.Repeat("a/", 64) + "input.md"} {
		t.Run(fmt.Sprintf("path %q", value), func(t *testing.T) {
			m := fixtureManifest()
			m.Inputs[0].Source.Path = value
			policyTestReject(t, m)
		})
	}
	for _, value := range []string{"", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("z", 64)} {
		t.Run("digest "+value, func(t *testing.T) {
			m := fixtureManifest()
			m.Inputs[0].Source.SHA256 = value
			policyTestReject(t, m)
		})
	}
	// json.Marshal replaces invalid Go UTF-8, so test the in-memory validation
	// boundary directly; strict wire UTF-8 has its own malformed-JSON matrix.
	m := fixtureManifest()
	m.Inputs[0].Source.Path = string([]byte{'d', '/', 0xff})
	if err := m.Validate(); err == nil {
		t.Fatal("invalid UTF-8 source path accepted")
	}
}
