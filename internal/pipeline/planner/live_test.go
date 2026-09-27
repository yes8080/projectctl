package planner

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/design/codexexec"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

// This opt-in test never writes GitHub. Its approved graph is synthetic and the
// single real worker receives only synthetic design/policy. An unsuccessful
// invocation must be reported; the test contains no retry or model fallback.
func TestPlannerCodexIntegration(t *testing.T) {
	if os.Getenv("PROJECTCTL_PLANNER_LIVE") != "1" {
		t.Skip("requires one explicitly authorized live invocation")
	}
	f, r := newPlannerInputFixture(t)
	f.document.Requirements[0].Statement = "Implement Go 1.22 package calc with pure Add(a, b int) int returning a+b, including deterministic positive, negative and zero unit tests. No I/O, external dependencies or overflow policy beyond native Go int arithmetic."
	f.document.Requirements[1].Statement = "Freeze an exact source commit SHA, built artifact SHA256 and environment identity, then verify the complete calc Add behavior on that fixed integration candidate; no mutable branch or artifact tag may be accepted."
	for i := range f.document.Sections {
		f.document.Sections[i].Content = "A tiny Go 1.22 calc package with pure integer Add only. Deterministic go test ./... and go vet ./...; macos is the fixed acceptance OS, no network and no credentials, inline table fixtures. No deployment, migrations, external systems or unresolved major decisions. Integration freezes exact source/artifact/environment identities and depends on completed implementation."
	}
	document := inputFixtureJSON(t, f.document)
	f.baseline.Design.SHA256 = protocol.SHA256(document)
	f.addSource(f.baseline.Design, document)
	head := f.baseline.Design
	head.Commit = f.pr.HeadSHA
	f.addSource(head, document)
	f.evidence.Binding.DesignSHA256 = f.baseline.Design.SHA256
	f.evidence.LogSHA256 = f.baseline.Design.SHA256
	f.repinGraph(t, &r)
	adapter, err := codexexec.New(codexexec.Config{Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("model calls=0; version/environment preflight failed: %v", err)
	}
	e, err := New(f, adapter)
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.Admit(context.Background(), r, f.fresh(t))
	if err != nil {
		t.Fatalf("model calls=0; synthetic approved input gate: %v", err)
	}
	start := time.Now()
	receipt, err := e.Execute(context.Background(), a)
	if err != nil {
		t.Fatalf("model calls=1; model=gpt-5.5; elapsed=%s; result=FAIL; fixed reason=%v", time.Since(start), err)
	}
	t.Logf("model calls=1 model=gpt-5.5 elapsed=%s result=valid-candidate contracts=%d requirements=%d output_sha256=%s input_tokens=%d output_tokens=%d", time.Since(start), len(receipt.Candidate.Contracts), len(receipt.Summary.Coverage), receipt.OutputSHA256, receipt.InputTokens, receipt.OutputTokens)
}
