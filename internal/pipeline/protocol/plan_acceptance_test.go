package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func fixturePlanAcceptance() *PlanAcceptance {
	ref := reference(testProject.ControlIssue)
	return &PlanAcceptance{Envelope: envelope(KindPlanAcceptance, Subject{Milestone: 1}), Candidate: ref, Run: ref,
		Assessments:        []Assessment{{CriterionID: "coverage", Result: "PASS", Reason: "reviewed exact Plan", Evidence: []Reference{ref}}},
		UnresolvedFindings: []Reference{}, Decision: "PASS", Reason: "all fixed checks satisfied"}
}

func TestPlanAcceptanceStrictRoundTrip(t *testing.T) {
	for _, decision := range []string{"PASS", "FAIL"} {
		t.Run(decision, func(t *testing.T) {
			record := fixturePlanAcceptance()
			record.Decision = decision
			if decision == "FAIL" {
				record.Assessments[0].Result = "FAIL"
				record.UnresolvedFindings = []Reference{record.Candidate}
			}
			data := mustEncode(t, record)
			decoded, err := Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := decoded.(*PlanAcceptance); !ok || !bytes.Equal(data, mustEncode(t, decoded)) {
				t.Fatal("PlanAcceptance lost its exact wire shape")
			}
			body, err := EncodeComment(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeComment(body); err != nil {
				t.Fatal(err)
			}
		})
	}
	valid := string(mustEncode(t, fixturePlanAcceptance()))
	cases := map[string]string{
		"unknown":              strings.Replace(valid, `"candidate":`, `"binding":{},"candidate":`, 1),
		"case alias":           strings.Replace(valid, `"candidate":`, `"Candidate":`, 1),
		"duplicate":            strings.Replace(valid, `"decision":`, `"decision":"FAIL","decision":`, 1),
		"nested duplicate":     strings.Replace(valid, `"criterion_id":`, `"criterion_id":"dag","criterion_id":`, 1),
		"nested unknown":       strings.Replace(valid, `"criterion_id":`, `"actor":"reviewer","criterion_id":`, 1),
		"issue subject":        strings.Replace(valid, `"subject":{"milestone":1}`, `"subject":{"issue":1}`, 1),
		"PR subject":           strings.Replace(valid, `"subject":{"milestone":1}`, `"subject":{"pull_request":1}`, 1),
		"zero issue smuggling": strings.Replace(valid, `"subject":{"milestone":1}`, `"subject":{"milestone":1,"issue":0}`, 1),
		"zero PR smuggling":    strings.Replace(valid, `"subject":{"milestone":1}`, `"subject":{"milestone":1,"pull_request":0}`, 1),
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(valid), &fields); err != nil {
		t.Fatal(err)
	}
	for field, original := range fields {
		delete(fields, field)
		data, _ := json.Marshal(fields)
		cases["missing "+field] = string(data)
		fields[field] = json.RawMessage("null")
		data, _ = json.Marshal(fields)
		cases["null "+field] = string(data)
		fields[field] = original
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) {
			if wire == valid {
				t.Fatal("test did not mutate the wire")
			}
			if _, err := Decode([]byte(wire)); err == nil {
				t.Fatal("invalid PlanAcceptance accepted")
			}
		})
	}
}

func TestPlanAcceptanceDecisionValidation(t *testing.T) {
	cases := map[string]func(*PlanAcceptance){
		"empty assessments":       func(r *PlanAcceptance) { r.Assessments = []Assessment{} },
		"duplicate criterion":     func(r *PlanAcceptance) { r.Assessments = append(r.Assessments, r.Assessments[0]) },
		"blank reason":            func(r *PlanAcceptance) { r.Reason = " " },
		"blank assessment reason": func(r *PlanAcceptance) { r.Assessments[0].Reason = " " },
		"missing evidence":        func(r *PlanAcceptance) { r.Assessments[0].Evidence = []Reference{} },
		"bad criterion":           func(r *PlanAcceptance) { r.Assessments[0].CriterionID = "" },
		"unknown decision":        func(r *PlanAcceptance) { r.Decision = "APPROVED" },
		"PASS with FAIL":          func(r *PlanAcceptance) { r.Assessments[0].Result = "FAIL" },
		"unknown result":          func(r *PlanAcceptance) { r.Assessments[0].Result = "SKIP" },
		"unresolved PASS":         func(r *PlanAcceptance) { r.UnresolvedFindings = []Reference{r.Run} },
		"missing findings":        func(r *PlanAcceptance) { r.UnresolvedFindings = nil },
		"invalid candidate":       func(r *PlanAcceptance) { r.Candidate.BodySHA256 = "" },
		"invalid run":             func(r *PlanAcceptance) { r.Run.NodeID = "" },
		"invalid evidence":        func(r *PlanAcceptance) { r.Assessments[0].Evidence[0].Author.ID = 0 },
		"invalid finding":         func(r *PlanAcceptance) { r.Decision = "FAIL"; r.UnresolvedFindings = []Reference{{}} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := fixturePlanAcceptance()
			change(r)
			if _, err := Encode(r); err == nil {
				t.Fatal("invalid PlanAcceptance encoded")
			}
			data, _ := json.Marshal(r)
			if _, err := Decode(data); err == nil {
				t.Fatal("invalid PlanAcceptance decoded")
			}
		})
	}
}

func TestPlanAcceptanceExactApproval(t *testing.T) {
	policy := fixturePolicy()
	review := fixturePlanAcceptance()
	candidate := verified(t, review, testAuthor, 100)
	decision := approvalFor(t, candidate, policy)
	approval := verified(t, decision, testAuthor, 101)
	newer := fixturePlanAcceptance()
	newer.OperationID, newer.Version, newer.Reason = "plan-review-v2", 2, "not yet approved"
	unapproved := verified(t, newer, testAuthor, 102)
	selected, err := SelectApproved([]VerifiedRecord{unapproved, candidate}, approval, policy)
	if err != nil || selected.Reference() != candidate.Reference() {
		t.Fatalf("exact PASS not selected: %v", err)
	}
	if _, err := SelectApproved([]VerifiedRecord{unapproved}, approval, policy); err == nil {
		t.Fatal("unapproved new revision substituted for missing approved review")
	}
	for _, findings := range [][]Reference{{}, {review.Run}} {
		failed := fixturePlanAcceptance()
		failed.Decision, failed.UnresolvedFindings = "FAIL", findings
		v := verified(t, failed, testAuthor, 110)
		a := verified(t, approvalFor(t, v, policy), testAuthor, 111)
		if _, err := SelectApproved([]VerifiedRecord{v}, a, policy); err == nil {
			t.Fatal("explicit approval authorized a FAIL plan review")
		}
	}
	for _, mutate := range []func(*Approval){
		func(a *Approval) { a.Subject.Milestone++ },
		func(a *Approval) { a.PolicySHA256 = testDigest },
		func(a *Approval) { a.Candidate.BodySHA256 = testDigest },
		func(a *Approval) { a.Candidate.Author = developerIdentity },
	} {
		a := approvalFor(t, candidate, policy)
		mutate(a)
		if _, err := SelectApproved([]VerifiedRecord{candidate}, verified(t, a, testAuthor, 111), policy); err == nil {
			t.Fatal("approval subject, policy or reference drift accepted")
		}
	}
	if _, err := SelectApproved([]VerifiedRecord{candidate}, verified(t, decision, developerIdentity, 111), policy); err == nil {
		t.Fatal("unauthorized native approval author accepted")
	}
}

func TestPlanAcceptancePublishedOnlyOnControlIssue(t *testing.T) {
	native, ref := observed(t, fixturePlanAcceptance(), testAuthor, 100)
	if _, err := VerifyGitHubRecord(native, ref); err != nil {
		t.Fatal(err)
	}
	for _, location := range []string{"issues/4", "pull/3"} {
		n, r := native, ref
		n.URL = strings.Replace(n.URL, "issues/2", location, 1)
		r.URL = n.URL
		if _, err := VerifyGitHubRecord(n, r); err == nil {
			t.Fatal("PlanAcceptance used a non-control native container")
		}
	}
}

func TestContractDesignApprovalLocations(t *testing.T) {
	for _, test := range []struct {
		name  string
		url   string
		valid bool
	}{
		{"legacy design PR", "https://github.com/owner/repo/pull/3#issuecomment-9", true},
		{"formal control Issue approval", "https://github.com/owner/repo/issues/2#issuecomment-9", true},
		{"wrong Issue", "https://github.com/owner/repo/issues/4#issuecomment-9", false},
		{"design Issue is not control", "https://github.com/owner/repo/issues/1#issuecomment-9", false},
		{"wrong PR", "https://github.com/owner/repo/pull/4#issuecomment-9", false},
		{"wrong repository", "https://github.com/owner/other/issues/2#issuecomment-9", false},
		{"wrong host", "https://example.com/owner/repo/issues/2#issuecomment-9", false},
		{"zero comment", "https://github.com/owner/repo/issues/2#issuecomment-0", false},
		{"comment suffix", "https://github.com/owner/repo/issues/2#issuecomment-9junk", false},
		{"URL query", "https://github.com/owner/repo/issues/2?redirect=1#issuecomment-9", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := fixtureContract()
			r.DesignBaseline.Approval = test.url
			_, encodeErr := Encode(r)
			wire, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			_, decodeErr := Decode(wire)
			if (encodeErr == nil) != test.valid || (decodeErr == nil) != test.valid {
				t.Fatalf("approval location valid=%v: encode=%v decode=%v", test.valid, encodeErr, decodeErr)
			}
		})
	}
}
