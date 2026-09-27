package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var developerIdentity = GitHubIdentity{ID: 8, NodeID: "U_8", Login: "developer", Type: "User"}

func fixturePolicy() Policy {
	return Policy{Schema: PolicySchema, Version: 1, Project: testProject, Grants: []Grant{
		{Principal: testAuthor, Roles: []Role{RoleController, RoleAcceptor}},
		{Principal: developerIdentity, Roles: []Role{RoleDeveloper}},
	}}
}

func observed(t *testing.T, record Record, author GitHubIdentity, id int64) (GitHubObservation, Reference) {
	t.Helper()
	body, err := EncodeComment(record)
	if err != nil {
		t.Fatal(err)
	}
	data := mustEncode(t, record)
	header := record.Header()
	location := fmt.Sprintf("issues/%d", header.Project.ControlIssue)
	if header.Kind == KindContract {
		location = fmt.Sprintf("issues/%d", header.Subject.Issue)
	}
	if header.Kind == KindEvidence || header.Kind == KindAcceptance {
		location = fmt.Sprintf("pull/%d", header.Subject.PullRequest)
	}
	ref := Reference{Kind: "issue_comment", DatabaseID: id, NodeID: fmt.Sprintf("IC_%d", id), URL: fmt.Sprintf("https://github.com/%s/%s/%s#issuecomment-%d", header.Project.Owner, header.Project.Repo, location, id), BodySHA256: SHA256([]byte(body)), CanonicalSHA256: SHA256(data), Author: author}
	return GitHubObservation{Project: header.Project, Kind: ref.Kind, DatabaseID: ref.DatabaseID, NodeID: ref.NodeID, URL: ref.URL, Author: author, Body: body}, ref
}

func verified(t *testing.T, record Record, author GitHubIdentity, id int64) VerifiedRecord {
	t.Helper()
	native, ref := observed(t, record, author, id)
	v, err := VerifyGitHubRecord(native, ref)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func approvalFor(t *testing.T, candidate VerifiedRecord, policy Policy) *Approval {
	t.Helper()
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &Approval{Envelope: envelope(KindApproval, candidate.header.Subject), Candidate: candidate.Reference(), PolicySHA256: digest, Decision: "APPROVED", Reason: "explicit authorized decision"}
}

func TestVerifiedGatewayBoundary(t *testing.T) {
	native, ref := observed(t, fixtureContract(), developerIdentity, 100)
	if _, err := VerifyGitHubRecord(native, ref); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*GitHubObservation, *Reference)
	}{
		{"deleted", func(n *GitHubObservation, r *Reference) { n.Deleted = true }},
		{"missing", func(n *GitHubObservation, r *Reference) { n.Body = "" }},
		{"raw whitespace drift", func(n *GitHubObservation, r *Reference) { n.Body += "\n" }},
		{"body drift", func(n *GitHubObservation, r *Reference) {
			n.Body = strings.Replace(n.Body, "strict records", "changed records", 1)
		}},
		{"canonical mismatch", func(n *GitHubObservation, r *Reference) { r.CanonicalSHA256 = testDigest }},
		{"replacement same content", func(n *GitHubObservation, r *Reference) { n.DatabaseID++ }},
		{"node drift", func(n *GitHubObservation, r *Reference) { n.NodeID = "IC_recreated" }},
		{"native author mismatch", func(n *GitHubObservation, r *Reference) { n.Author = testAuthor }},
		{"same login different ID", func(n *GitHubObservation, r *Reference) { n.Author.ID = 9 }},
		{"same ID different node", func(n *GitHubObservation, r *Reference) { n.Author.NodeID = "U_9" }},
		{"record moved", func(n *GitHubObservation, r *Reference) { n.URL = strings.Replace(n.URL, "issues/4", "issues/5", 1) }},
		{"record and reference moved", func(n *GitHubObservation, r *Reference) {
			n.URL = strings.Replace(n.URL, "issues/4", "issues/5", 1)
			r.URL = n.URL
		}},
		{"project drift", func(n *GitHubObservation, r *Reference) { n.Project.ControlIssue = 9 }},
		{"kind masquerade", func(n *GitHubObservation, r *Reference) { n.Kind = "pull_request_review" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			n, r := native, ref
			test.change(&n, &r)
			if _, err := VerifyGitHubRecord(n, r); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
	// Login renames do not transfer policy authority to a different stable user.
	native.Author.Login = "renamed-developer"
	if _, err := VerifyGitHubRecord(native, ref); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizationUsesNativeIdentityAndPolicy(t *testing.T) {
	policy := fixturePolicy()
	v := verified(t, fixtureContract(), developerIdentity, 100)
	if err := Authorize(v, policy, RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	if err := Authorize(v, policy, RoleController); err == nil {
		t.Fatal("developer obtained controller authority")
	}
	if err := Authorize(VerifiedRecord{}, policy, RoleController); err == nil {
		t.Fatal("zero identity authorized")
	}
	if err := Authorize(v, policy, "unknown"); err == nil {
		t.Fatal("unknown role authorized")
	}
	policy.Project.Repo = "other"
	if err := Authorize(v, policy, RoleDeveloper); err == nil {
		t.Fatal("cross-project authority")
	}
	for _, forgery := range []string{`{"actor":"reviewer"}`, `{"verified":true,"author":{"id":7}}`, `{}`} {
		var decoded VerifiedRecord
		if err := json.Unmarshal([]byte(forgery), &decoded); err == nil {
			t.Fatal("JSON created verified capability")
		}
		if err := Authorize(decoded, fixturePolicy(), RoleController); err == nil {
			t.Fatal("forged capability authorized")
		}
	}
	if _, err := json.Marshal(v); err == nil {
		t.Fatal("verified capability persisted")
	}
	// The getter cannot mutate the content that authorization or selection uses.
	r, err := v.Record()
	if err != nil {
		t.Fatal(err)
	}
	r.(*Contract).Goal = "tampered"
	again, err := v.Record()
	if err != nil || again.(*Contract).Goal != "strict records" {
		t.Fatal("mutable alias to verified data")
	}
	rf := v.Reference()
	rf.Author = testAuthor
	if err := Authorize(v, fixturePolicy(), RoleController); err == nil {
		t.Fatal("mutable author alias")
	}
}

func TestIndependentNativeIdentities(t *testing.T) {
	dev := verified(t, fixtureContract(), developerIdentity, 100)
	reviewer := verified(t, fixtureContract(), testAuthor, 101)
	if err := RequireIndependent(dev, reviewer); err != nil {
		t.Fatal(err)
	}
	if err := RequireIndependent(dev, dev); err == nil {
		t.Fatal("single-login bootstrap claimed independence")
	}
	if err := RequireIndependent(VerifiedRecord{}, reviewer); err == nil {
		t.Fatal("unverified reviewer")
	}
}

func TestExactApprovalDoesNotSelectNewerCandidate(t *testing.T) {
	policy := fixturePolicy()
	old := verified(t, fixtureContract(), developerIdentity, 100)
	newContract := fixtureContract()
	newContract.Version = 2
	newContract.OperationID = "contract-v2"
	newContract.Goal = "new unapproved goal"
	newer := verified(t, newContract, developerIdentity, 101)
	approval := verified(t, approvalFor(t, old, policy), testAuthor, 200)
	for _, candidates := range [][]VerifiedRecord{{old, newer}, {newer, old}, {old, old, newer}} {
		selected, err := SelectApproved(candidates, approval, policy)
		if err != nil {
			t.Fatal(err)
		}
		if selected.Reference() != old.Reference() {
			t.Fatal("new unapproved candidate superseded exact approval")
		}
	}
}

func TestApprovalRejectsForgedStaleOrConflictingFacts(t *testing.T) {
	policy := fixturePolicy()
	candidate := verified(t, fixtureContract(), developerIdentity, 100)
	type scenario struct {
		candidates []VerifiedRecord
		approval   *Approval
		signer     GitHubIdentity
		policy     Policy
	}
	base := func() scenario {
		freshPolicy := fixturePolicy()
		return scenario{[]VerifiedRecord{candidate}, approvalFor(t, candidate, freshPolicy), testAuthor, freshPolicy}
	}
	tests := []struct {
		name   string
		change func(*scenario)
	}{
		{"missing exact candidate", func(s *scenario) { s.candidates = nil }},
		{"unverified candidate", func(s *scenario) { s.candidates = []VerifiedRecord{{}} }},
		{"wrong digest", func(s *scenario) { s.approval.Candidate.CanonicalSHA256 = testDigest }},
		{"wrong raw digest", func(s *scenario) { s.approval.Candidate.BodySHA256 = testDigest }},
		{"same content other ID", func(s *scenario) {
			s.candidates = []VerifiedRecord{verified(t, fixtureContract(), developerIdentity, 101)}
		}},
		{"other subject", func(s *scenario) { s.approval.Subject = Subject{Issue: 5} }},
		{"duplicate creation same operation", func(s *scenario) {
			s.candidates = append(s.candidates, verified(t, fixtureContract(), developerIdentity, 101))
		}},
		{"rejected", func(s *scenario) { s.approval.Decision = "REJECTED" }},
		{"unauthorized native author", func(s *scenario) { s.signer = developerIdentity }},
		{"stolen display login", func(s *scenario) {
			s.signer = GitHubIdentity{ID: 99, NodeID: "U_99", Login: testAuthor.Login, Type: "User"}
		}},
		{"policy changed", func(s *scenario) { s.policy.Version++ }},
		{"role revoked", func(s *scenario) { s.policy.Grants[0].Roles = []Role{RoleAcceptor} }},
		{"conflicting operation", func(s *scenario) {
			other := fixtureContract()
			other.Goal = "different"
			s.candidates = append(s.candidates, verified(t, other, developerIdentity, 101))
		}},
		{"object edit with new op", func(s *scenario) {
			other := fixtureContract()
			other.OperationID = "changed"
			other.Goal = "different"
			s.candidates = append(s.candidates, verified(t, other, developerIdentity, 100))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := base()
			test.change(&s)
			approval := verified(t, s.approval, s.signer, 200)
			if _, err := SelectApproved(s.candidates, approval, s.policy); err == nil {
				t.Fatal("invalid approval accepted")
			}
		})
	}
	if _, err := SelectApproved([]VerifiedRecord{candidate}, VerifiedRecord{}, policy); err == nil {
		t.Fatal("zero approval accepted")
	}
	if _, err := SelectApproved([]VerifiedRecord{candidate}, verified(t, fixtureContract(), testAuthor, 200), policy); err == nil {
		t.Fatal("non-approval accepted")
	}
}

func TestPolicyStrictness(t *testing.T) {
	policy := fixturePolicy()
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePolicy(data); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		strings.Replace(string(data), `"principal":`, `"actor":"owner","principal":`, 1),
		strings.Replace(string(data), `"id":7`, `"id":7,"id":8`, 1),
		strings.Replace(string(data), `"login":`, `"Login":`, 1),
		strings.Replace(string(data), PolicySchema, "projectctl.policy/v2", 1),
	} {
		if _, err := DecodePolicy([]byte(input)); err == nil {
			t.Fatal("ambiguous policy accepted")
		}
	}
	policy.Grants = append(policy.Grants, policy.Grants[0])
	if err := policy.Validate(); err == nil {
		t.Fatal("duplicate grant accepted")
	}
}
