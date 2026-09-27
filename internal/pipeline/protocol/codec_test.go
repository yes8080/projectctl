package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

var testProject = Project{Owner: "owner", Repo: "repo", ControlIssue: 2}
var testAuthor = GitHubIdentity{ID: 7, NodeID: "U_7", Login: "reviewer", Type: "User"}
var testDigest = strings.Repeat("a", 64)
var testCommit = strings.Repeat("b", 40)

type embeddedRecord struct{ *Contract }

func envelope(kind Kind, subject Subject) Envelope {
	return Envelope{Schema: Schema, Kind: kind, Project: testProject, Subject: subject, OperationID: "test-" + string(kind), Version: 1}
}

func reference(issue int64) Reference {
	return Reference{Kind: "issue_comment", DatabaseID: 30, NodeID: "IC_30", URL: fmt.Sprintf("https://github.com/owner/repo/issues/%d#issuecomment-30", issue), BodySHA256: testDigest, CanonicalSHA256: testDigest, Author: testAuthor}
}

func fixtureBudget() Budget {
	return Budget{MaxAgentRuns: 10, MaxRunSeconds: 60, MaxWallSeconds: 600, MaxDesignRounds: 2, MaxPlanningRounds: 2, MaxImplementationRounds: 3, Currency: "USD"}
}

func fixtureContract() *Contract {
	return &Contract{
		Envelope:       envelope(KindContract, Subject{Issue: 4}),
		DesignBaseline: DesignBaseline{Issue: 1, PullRequest: 3, MergeCommit: testCommit, Tree: testCommit, Approval: "https://github.com/owner/repo/pull/3#issuecomment-9"},
		Key:            "protocol", Goal: "strict records", Scope: []string{"protocol"}, NonGoals: []string{"network"}, Requirements: []string{"design:3"},
		Acceptance: []Criterion{{ID: "AC-1", Statement: "strict decode"}}, Verification: []Check{{ID: "unit", Argv: []string{"go", "test", "./..."}}},
		ImpactSurface: []string{"protocol"}, Risks: []Risk{{Risk: "ambiguity", Mitigation: "goldens"}},
		AcceptanceEnvironment: Environment{OS: []string{"linux", "macos", "windows"}, Go: "1.22", Network: "forbidden", Credentials: "forbidden", Fixtures: "golden vectors"},
	}
}

func fixtureRecords() []Record {
	ref := reference(2)
	bind := Binding{PullRequest: 3, HeadSHA: testCommit, BaseSHA: testCommit, DesignSHA256: testDigest, ContractSHA256: testDigest, PolicySHA256: testDigest}
	source := SourceReference{Commit: testCommit, Path: "docs/design.md", SHA256: testDigest}
	return []Record{
		&Baseline{Envelope: envelope(KindBaseline, Subject{Issue: 1}), Inputs: []SourceReference{source}, Design: source, PolicySHA256: testDigest, Review: ref, Approval: ref},
		&Plan{Envelope: envelope(KindPlan, Subject{Milestone: 1}), Baseline: ref, Members: []Member{{Issue: 4, IssueDatabaseID: 40, IssueNodeID: "I_40", Contract: reference(4)}}, NativeTopologySHA256: testDigest, Budget: fixtureBudget(), Completion: []string{"all ACs satisfied"}},
		fixtureContract(),
		&Run{Envelope: envelope(KindRun, Subject{Issue: 4}), Role: RoleDeveloper, AgentInstance: "dev-1", Host: "host-1", Inputs: []Reference{ref}, InputSHA256: testDigest, AssignmentGeneration: 1, Attempt: 1, Budget: fixtureBudget()},
		&Evidence{Envelope: envelope(KindEvidence, Subject{PullRequest: 3}), Binding: bind, Run: ref, CheckID: "unit", Argv: []string{"go", "test", "./..."}, EnvironmentSHA256: testDigest, Result: "PASS", LogSHA256: testDigest},
		&Acceptance{Envelope: envelope(KindAcceptance, Subject{PullRequest: 3}), Binding: bind, Run: ref, Assessments: []Assessment{{CriterionID: "AC-1", Result: "PASS", Reason: "observed", Evidence: []Reference{ref}}}, UnresolvedFindings: []Reference{}, Decision: "PASS", Reason: "all ACs"},
		&Delivery{Envelope: envelope(KindDelivery, Subject{Milestone: 1}), Plan: ref, SourceSHA: testCommit, EnvironmentSHA256: testDigest, IntegrationEvidence: []Reference{ref}, BugGate: ref, Cleanup: ref},
		&Approval{Envelope: envelope(KindApproval, Subject{Milestone: 1}), Candidate: ref, PolicySHA256: testDigest, Decision: "APPROVED", Reason: "authorized"},
	}
}

func mustEncode(t *testing.T, r Record) []byte {
	t.Helper()
	data, err := Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRecordRoundTrips(t *testing.T) {
	for _, record := range fixtureRecords() {
		t.Run(string(record.Header().Kind), func(t *testing.T) {
			data := mustEncode(t, record)
			decoded, err := Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, mustEncode(t, decoded)) {
				t.Fatal("non-deterministic round trip")
			}
			body, err := EncodeComment(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeComment(body); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"schema", "kind", "project", "subject", "operation_id", "version"} {
				var object map[string]json.RawMessage
				if err := json.Unmarshal(data, &object); err != nil {
					t.Fatal(err)
				}
				delete(object, field)
				missing, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Decode(missing); err == nil {
					t.Errorf("missing %s accepted", field)
				}
			}
		})
	}
}

func TestStrictDecodeRejections(t *testing.T) {
	valid := string(mustEncode(t, fixtureContract()))
	cases := map[string]string{
		"unknown top-level":        strings.Replace(valid, `"goal":`, `"actor":"owner","goal":`, 1),
		"case alias":               strings.Replace(valid, `"schema":`, `"Schema":`, 1),
		"nested case alias":        strings.Replace(valid, `"owner":`, `"Owner":`, 1),
		"duplicate":                strings.Replace(valid, `"goal":`, `"goal":"other","goal":`, 1),
		"escaped duplicate":        strings.Replace(valid, `"goal":`, `"\u0067oal":"other","goal":`, 1),
		"nested duplicate":         strings.Replace(valid, `"owner":`, `"owner":"other","owner":`, 1),
		"unknown nested":           strings.Replace(valid, `"statement":`, `"verified":true,"statement":`, 1),
		"unknown schema":           strings.Replace(valid, Schema, "projectctl.record/v2", 1),
		"unknown kind":             strings.Replace(valid, `"kind":"contract"`, `"kind":"plan_freeze"`, 1),
		"two documents":            valid + valid,
		"null goal":                strings.Replace(valid, `"goal":"strict records"`, `"goal":null`, 1),
		"fraction revision":        strings.Replace(valid, `"version":1`, `"version":1.0`, 1),
		"zero revision":            strings.Replace(valid, `"version":1`, `"version":0`, 1),
		"empty operation":          strings.Replace(valid, `"operation_id":"test-contract"`, `"operation_id":""`, 1),
		"multiple subject":         strings.Replace(valid, `"subject":{"issue":4}`, `"subject":{"issue":4,"milestone":1}`, 1),
		"zero milestone smuggling": strings.Replace(valid, `"subject":{"issue":4}`, `"subject":{"issue":4,"milestone":0}`, 1),
	}
	for _, field := range []string{"milestone", "dependencies", "progress", "delivery"} {
		cases["native "+field] = strings.Replace(valid, `"goal":`, `"`+field+`":[],"goal":`, 1)
		cases["nested native "+field] = strings.Replace(valid, `"statement":`, `"`+field+`":[],"statement":`, 1)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(data)); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
	for _, body := range []string{valid, "narrative " + Marker + valid, Marker + valid + Marker + valid, Marker + valid + " trailing text"} {
		if _, err := DecodeComment(body); err == nil {
			t.Fatal("ambiguous comment accepted")
		}
	}
}

func TestSemanticValidation(t *testing.T) {
	cases := []struct {
		name   string
		record Record
	}{
		{"bad contract subject", func() Record { c := fixtureContract(); c.Subject = Subject{Milestone: 1}; return c }()},
		{"duplicate AC", func() Record { c := fixtureContract(); c.Acceptance = append(c.Acceptance, c.Acceptance[0]); return c }()},
		{"missing check", func() Record { c := fixtureContract(); c.Verification = nil; return c }()},
		{"wrong baseline URL", func() Record {
			c := fixtureContract()
			c.DesignBaseline.Approval = "https://github.com/owner/repo/pull/99#issuecomment-9"
			return c
		}()},
		{"wrong parent issue", func() Record {
			c := fixtureContract()
			c.ParentRevision = &ContractRevision{CommentDatabaseID: 30, CommentURL: reference(9).URL, BodySHA256: testDigest, Disposition: "superseded"}
			return c
		}()},
		{"unbounded budget", func() Record { r := fixtureRecords()[1].(*Plan); r.Budget.MaxAgentRuns = 0; return r }()},
		{"duplicate member", func() Record { r := fixtureRecords()[1].(*Plan); r.Members = append(r.Members, r.Members[0]); return r }()},
		{"wrong contract issue", func() Record { r := fixtureRecords()[1].(*Plan); r.Members[0].Contract = reference(9); return r }()},
		{"unknown role", func() Record { r := fixtureRecords()[3].(*Run); r.Role = "actor"; return r }()},
		{"PASS nonzero exit", func() Record { r := fixtureRecords()[4].(*Evidence); r.ExitCode = 1; return r }()},
		{"mismatched PR", func() Record { r := fixtureRecords()[4].(*Evidence); r.Binding.PullRequest = 4; return r }()},
		{"PASS failed AC", func() Record { r := fixtureRecords()[5].(*Acceptance); r.Assessments[0].Result = "FAIL"; return r }()},
		{"PASS finding", func() Record {
			r := fixtureRecords()[5].(*Acceptance)
			r.UnresolvedFindings = []Reference{reference(2)}
			return r
		}()},
		{"missing integration", func() Record { r := fixtureRecords()[6].(*Delivery); r.IntegrationEvidence = nil; return r }()},
		{"informal approval", func() Record { r := fixtureRecords()[7].(*Approval); r.Decision = "LGTM"; return r }()},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Encode(test.record); err == nil {
				t.Fatal("invalid record encoded")
			}
		})
	}
	if _, err := Encode((*Contract)(nil)); err == nil {
		t.Fatal("nil accepted")
	}
	if _, err := Encode(embeddedRecord{fixtureContract()}); err == nil {
		t.Fatal("record union extended through embedding")
	}
	invalidUTF8 := fixtureContract()
	invalidUTF8.Goal = string([]byte{0xff})
	if _, err := Encode(invalidUTF8); err == nil {
		t.Fatal("encoding silently replaced invalid UTF-8")
	}
}

func TestReferenceValidation(t *testing.T) {
	valid := reference(2)
	if err := valid.Validate(testProject); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*Reference)
	}{
		{"other repo", func(r *Reference) { r.URL = "https://github.com/owner/other/issues/2#issuecomment-30" }},
		{"other ID", func(r *Reference) { r.DatabaseID = 31 }},
		{"missing node", func(r *Reference) { r.NodeID = "" }},
		{"missing author", func(r *Reference) { r.Author = GitHubIdentity{} }},
		{"wrong kind", func(r *Reference) { r.Kind = "pull_request_review" }},
		{"http", func(r *Reference) { r.URL = strings.Replace(r.URL, "https:", "http:", 1) }},
		{"query", func(r *Reference) { r.URL = strings.Replace(r.URL, "#", "?x=1#", 1) }},
		{"escaped path", func(r *Reference) { r.URL = strings.Replace(r.URL, "owner", "%6fwner", 1) }},
		{"hash uppercase", func(r *Reference) { r.BodySHA256 = strings.ToUpper(testDigest) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			r := valid
			test.change(&r)
			if err := r.Validate(testProject); err == nil {
				t.Fatal("invalid ref accepted")
			}
		})
	}
}

func TestFrozenIssue4Contract(t *testing.T) {
	data, err := os.ReadFile("testdata/issue-4-contract-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.TrimSuffix(data, []byte("\n")) // fixture file newline is not comment content
	r, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	c := r.(*Contract)
	if c.Subject.Issue != 4 || c.Version != 2 || c.DesignBaseline.MergeCommit != "1da6dfa438d4deaa13d42475e33fe84492c3d40b" {
		t.Fatal("wrong frozen contract")
	}
	d, err := DigestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if d != "7992cb33e1db6df9c78819ad984610d960a7b60a88d647c25d112d654c0ddcdd" {
		t.Fatalf("frozen canonical digest mismatch: %s", d)
	}
	if got := SHA256([]byte(Marker + " " + string(data))); got != "0d514a4ddcd5d54c8ff5b2eb3a10c6a7b4779013f9bea5c5d9115ccb40808395" {
		t.Fatalf("frozen body digest mismatch: %s", got)
	}
}
