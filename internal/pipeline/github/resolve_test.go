package github

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func gatewayContract() *protocol.Contract {
	return &protocol.Contract{
		Envelope:       protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindContract, Project: testServerFixtureProject(), Subject: protocol.Subject{Issue: 4}, OperationID: "contract-v1", Version: 1},
		DesignBaseline: protocol.DesignBaseline{Issue: 1, PullRequest: 3, MergeCommit: strings.Repeat("b", 40), Tree: strings.Repeat("c", 40), Approval: "https://github.com/octo/pipeline/pull/3#issuecomment-9"},
		Key:            "gateway", Goal: "authoritative GitHub reads", Scope: []string{"gateway"}, NonGoals: []string{"scheduler"}, Requirements: []string{"design:3"},
		Acceptance: []protocol.Criterion{{ID: "AC-1", Statement: "exact native records"}}, Verification: []protocol.Check{{ID: "unit", Argv: []string{"go", "test", "./..."}}},
		ImpactSurface: []string{"gateway"}, Risks: []protocol.Risk{{Risk: "drift", Mitigation: "pin references"}},
		AcceptanceEnvironment: protocol.Environment{OS: []string{"linux", "macos", "windows"}, Go: "1.22", Network: "mocked", Credentials: "synthetic", Fixtures: "HTTP fixtures"},
	}
}

func gatewayPolicy() protocol.Policy {
	return protocol.Policy{Schema: protocol.PolicySchema, Version: 1, Project: testServerFixtureProject(), Grants: []protocol.Grant{{Principal: testServerFixtureAuthor(), Roles: []protocol.Role{protocol.RoleController, protocol.RoleDesignReviewer}}}}
}

func recordNative(t *testing.T, record protocol.Record, id int64) (map[string]any, protocol.Reference) {
	t.Helper()
	body, err := protocol.EncodeComment(record)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := protocol.Encode(record)
	if err != nil {
		t.Fatal(err)
	}
	number := int64(2)
	if record.Header().Kind == protocol.KindContract {
		number = record.Header().Subject.Issue
	}
	item := testServerFixtureComment(number, id, body)
	if record.Header().Kind == protocol.KindEvidence || record.Header().Kind == protocol.KindAcceptance {
		number = record.Header().Subject.PullRequest
		item = testServerFixtureComment(number, id, body)
		item["html_url"] = fmt.Sprintf("https://github.com/octo/pipeline/pull/%d#issuecomment-%d", number, id)
	}
	ref := protocol.Reference{Kind: "issue_comment", DatabaseID: id, NodeID: fmt.Sprintf("IC_%d", id), URL: item["html_url"].(string), BodySHA256: protocol.SHA256([]byte(body)), CanonicalSHA256: protocol.SHA256(encoded), Author: testServerFixtureAuthor()}
	return item, ref
}

func gatewayApproval(t *testing.T, candidate protocol.Reference, subject protocol.Subject, op string) *protocol.Approval {
	t.Helper()
	digest, err := gatewayPolicy().Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &protocol.Approval{Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindApproval, Project: testServerFixtureProject(), Subject: subject, OperationID: op, Version: 1}, Candidate: candidate, PolicySHA256: digest, Decision: "APPROVED", Reason: "pinned Controller decision"}
}

func TestResolveApprovalPinsExactCandidateNotLatest(t *testing.T) {
	contract, contractRef := recordNative(t, gatewayContract(), 31)
	approval, approvalRef := recordNative(t, gatewayApproval(t, contractRef, protocol.Subject{Issue: 4}, "approval-v1"), 40)
	newer := gatewayContract()
	newer.OperationID = "contract-v2"
	newer.Version = 2
	newer.Goal = "unapproved change"
	latest, _ := recordNative(t, newer, 32)
	var collectionReads atomic.Int64
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/octo/pipeline/issues/comments/31":
			testServerFixtureJSON(t, w, contract)
		case "/repos/octo/pipeline/issues/comments/40":
			testServerFixtureJSON(t, w, approval)
		case "/repos/octo/pipeline/issues/4/comments":
			collectionReads.Add(1)
			testServerFixtureJSON(t, w, []any{contract, latest})
		default:
			http.NotFound(w, r)
		}
	})
	chain, err := g.ResolveApproved(context.Background(), approvalRef, gatewayPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if chain.Candidate.Reference() != contractRef || len(chain.Records) != 2 || collectionReads.Load() != 0 {
		t.Fatalf("latest comment affected pinned selection: %#v", chain)
	}
	// The returned chain does not become an authoritative local cache.
	contract["body"] = "edited after approval"
	if _, err := g.ResolveApproved(context.Background(), approvalRef, gatewayPolicy()); err == nil {
		t.Fatal("edited remote record replaced by cached selection")
	}
}

func TestPinnedNativeReferenceDriftIsRejected(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any, *protocol.Reference)
		status int
	}{
		{"deleted", nil, 404}, {"inaccessible", nil, 403},
		{"edited", func(n map[string]any, r *protocol.Reference) { n["body"] = n["body"].(string) + "\n" }, 200},
		{"native author", func(n map[string]any, r *protocol.Reference) {
			author := testServerFixtureAuthor()
			author.ID++
			n["user"] = author
		}, 200},
		{"stable node", func(n map[string]any, r *protocol.Reference) { n["node_id"] = "IC_recreated" }, 200},
		{"stable ID", func(n map[string]any, r *protocol.Reference) { n["id"] = 99 }, 200},
		{"wrong canonical digest", func(n map[string]any, r *protocol.Reference) { r.CanonicalSHA256 = strings.Repeat("a", 64) }, 200},
		{"wrong repository", func(n map[string]any, r *protocol.Reference) {
			n["html_url"] = "https://github.com/other/pipeline/issues/4#issuecomment-31"
		}, 200},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			native, ref := recordNative(t, gatewayContract(), 31)
			if test.change != nil {
				test.change(native, &ref)
			}
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				testServerFixtureJSON(t, w, native)
			})
			if _, err := g.FetchRecord(context.Background(), ref); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}

func TestApprovalAuthorizationUsesNativeController(t *testing.T) {
	for _, mode := range []string{"wrong author", "wrong policy", "rejected", "non-approval"} {
		t.Run(mode, func(t *testing.T) {
			contract, contractRef := recordNative(t, gatewayContract(), 31)
			decision := gatewayApproval(t, contractRef, protocol.Subject{Issue: 4}, "decision")
			if mode == "wrong policy" {
				decision.PolicySHA256 = strings.Repeat("a", 64)
			}
			if mode == "rejected" {
				decision.Decision = "REJECTED"
			}
			approval, approvalRef := recordNative(t, decision, 40)
			if mode == "wrong author" {
				other := testServerFixtureAuthor()
				other.ID = 99
				other.NodeID = "U_99"
				approval["user"] = other
				approvalRef.Author = other
			}
			if mode == "non-approval" {
				approval, approvalRef = contract, contractRef
			}
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/31") {
					testServerFixtureJSON(t, w, contract)
				} else {
					testServerFixtureJSON(t, w, approval)
				}
			})
			if _, err := g.ResolveApproved(context.Background(), approvalRef, gatewayPolicy()); err == nil {
				t.Fatal("unapproved chain accepted")
			}
		})
	}
}

func TestReferenceChainFollowsAllPreciseInputsAndDetectsConflicts(t *testing.T) {
	// A run is an input to an evidence record, itself referenced by a baseline.
	// These references test full traversal, not high-level design acceptance.
	contract, contractRef := recordNative(t, gatewayContract(), 31)
	budget := protocol.Budget{MaxAgentRuns: 10, MaxRunSeconds: 60, MaxWallSeconds: 600, MaxDesignRounds: 2, MaxPlanningRounds: 2, MaxImplementationRounds: 3, Currency: "USD"}
	header := func(kind protocol.Kind, op string) protocol.Envelope {
		return protocol.Envelope{Schema: protocol.Schema, Kind: kind, Project: testServerFixtureProject(), Subject: protocol.Subject{PullRequest: 3}, OperationID: op, Version: 1}
	}
	run := &protocol.Run{Envelope: header(protocol.KindRun, "run"), Role: protocol.RoleDesignReviewer, AgentInstance: "reviewer", Host: "test", Inputs: []protocol.Reference{contractRef}, InputSHA256: strings.Repeat("a", 64), AssignmentGeneration: 1, Attempt: 1, Budget: budget}
	runNative, runRef := recordNative(t, run, 33)
	bind := protocol.Binding{PullRequest: 3, HeadSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("c", 40), DesignSHA256: strings.Repeat("a", 64), ContractSHA256: contractRef.CanonicalSHA256, PolicySHA256: strings.Repeat("a", 64)}
	evidence := &protocol.Evidence{Envelope: header(protocol.KindEvidence, "evidence"), Binding: bind, Run: runRef, CheckID: "unit", Argv: []string{"go", "test"}, EnvironmentSHA256: strings.Repeat("a", 64), Result: "PASS", LogSHA256: strings.Repeat("a", 64)}
	evidenceNative, evidenceRef := recordNative(t, evidence, 34)
	acceptance := &protocol.Acceptance{Envelope: header(protocol.KindAcceptance, "design-review"), Binding: bind, Run: runRef, Assessments: []protocol.Assessment{{CriterionID: "AC-1", Result: "PASS", Reason: "fixture observation", Evidence: []protocol.Reference{evidenceRef}}}, UnresolvedFindings: []protocol.Reference{}, Decision: "PASS", Reason: "fixture review"}
	acceptanceNative, acceptanceRef := recordNative(t, acceptance, 37)
	decisionNative, decisionRef := recordNative(t, gatewayApproval(t, contractRef, protocol.Subject{Issue: 4}, "design-approval"), 35)
	source := protocol.SourceReference{Commit: strings.Repeat("b", 40), Path: "docs/design.md", SHA256: strings.Repeat("a", 64)}
	baseline := &protocol.Baseline{Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindBaseline, Project: testServerFixtureProject(), Subject: protocol.Subject{Issue: 1}, OperationID: "baseline", Version: 1}, Inputs: []protocol.SourceReference{source}, Design: source, PolicySHA256: strings.Repeat("a", 64), Review: acceptanceRef, Approval: decisionRef}
	baselineNative, baselineRef := recordNative(t, baseline, 36)
	plan := &protocol.Plan{Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindPlan, Project: testServerFixtureProject(), Subject: protocol.Subject{Milestone: 1}, OperationID: "plan", Version: 1}, Baseline: baselineRef, Members: []protocol.Member{{Issue: 4, IssueDatabaseID: 1004, IssueNodeID: "I_4", Contract: contractRef}}, NativeTopologySHA256: strings.Repeat("a", 64), Budget: budget, Completion: []string{"all required ACs"}}
	planNative, planRef := recordNative(t, plan, 38)
	rootNative, rootRef := recordNative(t, gatewayApproval(t, planRef, protocol.Subject{Milestone: 1}, "plan-approval"), 40)
	objects := map[int64]map[string]any{31: contract, 33: runNative, 34: evidenceNative, 35: decisionNative, 36: baselineNative, 37: acceptanceNative, 38: planNative, 40: rootNative}
	memberNative := testServerFixtureIssue(4, nil)
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/octo/pipeline/issues/4" {
			testServerFixtureJSON(t, w, memberNative)
			return
		}
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/octo/pipeline/issues/comments/"), 10, 64)
		if item, ok := objects[id]; ok {
			testServerFixtureJSON(t, w, item)
		} else {
			http.NotFound(w, r)
		}
	})
	chain, err := g.ResolveApproved(context.Background(), rootRef, gatewayPolicy())
	if err != nil || len(chain.Records) != 8 {
		t.Fatalf("incomplete chain: %d %v", len(chain.Records), err)
	}
	memberNative["id"] = 9999
	if _, err := g.ResolveApproved(context.Background(), rootRef, gatewayPolicy()); err == nil {
		t.Fatal("plan member identity drift ignored")
	}
	memberNative["id"] = 1004
	delete(objects, 33)
	if _, err := g.ResolveApproved(context.Background(), rootRef, gatewayPolicy()); err == nil {
		t.Fatal("deleted transitive run ignored")
	}
}

func TestRecordChainRejectsWrongReferenceKinds(t *testing.T) {
	contract, contractRef := recordNative(t, gatewayContract(), 31)
	source := protocol.SourceReference{Commit: strings.Repeat("b", 40), Path: "docs/design.md", SHA256: strings.Repeat("a", 64)}
	baseline := &protocol.Baseline{Envelope: protocol.Envelope{Schema: protocol.Schema, Kind: protocol.KindBaseline, Project: testServerFixtureProject(), Subject: protocol.Subject{Issue: 1}, OperationID: "baseline", Version: 1}, Inputs: []protocol.SourceReference{source}, Design: source, PolicySHA256: strings.Repeat("a", 64), Review: contractRef, Approval: contractRef}
	baselineNative, baselineRef := recordNative(t, baseline, 36)
	rootNative, rootRef := recordNative(t, gatewayApproval(t, baselineRef, protocol.Subject{Issue: 1}, "approval"), 40)
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/31"):
			testServerFixtureJSON(t, w, contract)
		case strings.HasSuffix(r.URL.Path, "/36"):
			testServerFixtureJSON(t, w, baselineNative)
		default:
			testServerFixtureJSON(t, w, rootNative)
		}
	})
	if _, err := g.ResolveApproved(context.Background(), rootRef, gatewayPolicy()); err == nil {
		t.Fatal("contract was accepted as design review and approval")
	}
}

func TestFetchNativeReviewIdentity(t *testing.T) {
	native, ref := recordNative(t, gatewayContract(), 31)
	// A contract cannot be relocated to a PR review even when native identity is
	// otherwise valid: protocol publication location remains authoritative.
	native["html_url"] = "https://github.com/octo/pipeline/pull/3#pullrequestreview-31"
	native["pull_request_url"] = "https://api.github.com/repos/octo/pipeline/pulls/3"
	ref.Kind = "pull_request_review"
	ref.URL = native["html_url"].(string)
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/octo/pipeline/pulls/3/reviews/31" {
			t.Errorf("wrong review endpoint %s", r.URL.Path)
		}
		testServerFixtureJSON(t, w, native)
	})
	if _, err := g.FetchRecord(context.Background(), ref); err == nil {
		t.Fatal("contract accepted on wrong native location")
	}
}
