package design

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

func TestStructuredDesignAndReviewAreStrict(t *testing.T) {
	valid := data(t, goodDocument())
	if _, err := DecodeDocument(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Document){func(d *Document) { d.Requirements = nil }, func(d *Document) { d.Sections = d.Sections[1:] }, func(d *Document) { d.Sections[0].Content = " " }, func(d *Document) { d.Sections[0].ID = d.Sections[1].ID }, func(d *Document) { d.Schema = "other" }} {
		d := goodDocument()
		change(&d)
		if _, err := DecodeDocument(data(t, d)); err == nil {
			t.Fatal("incomplete document accepted")
		}
	}
	for _, raw := range []string{strings.Replace(string(valid), `"schema":`, `"unknown":true,"schema":`, 1), strings.Replace(string(valid), `"schema":`, `"schema":"x","schema":`, 1), strings.Replace(string(valid), `"schema":`, `"Schema":`, 1), strings.Replace(string(valid), `"open_questions":[]`, `"open_questions":null`, 1), string(valid) + " {}"} {
		if _, err := DecodeDocument([]byte(raw)); err == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
	hash := protocol.SHA256(valid)
	r := goodReview(hash)
	if _, err := DecodeReview(data(t, r), hash); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Review){func(r *Review) { r.CandidateSHA256 = "other" }, func(r *Review) { r.Assessments = r.Assessments[1:] }, func(r *Review) { r.Assessments[0].Result = "FAIL" }, func(r *Review) { r.Assessments[0].Reason = "" }} {
		r := goodReview(hash)
		change(&r)
		if _, err := DecodeReview(data(t, r), hash); err == nil {
			t.Fatal("invalid review accepted")
		}
	}
	r.Decision = "FAIL"
	r.Assessments[0].Result = "FAIL"
	if _, err := DecodeReview(data(t, r), hash); err != nil {
		t.Fatal("honest failure rejected", err)
	}
	for _, schema := range [][]byte{DesignSchema(), ReviewSchema()} {
		var parsed map[string]any
		if json.Unmarshal(schema, &parsed) != nil || parsed["additionalProperties"] != false {
			t.Fatal("schema not strict")
		}
	}
}

func TestFreshDesignAndIndependentReviewContext(t *testing.T) {
	f, e, r := newFixture(t)
	a, err := e.Admit(context.Background(), r, f.fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := e.Execute(context.Background(), a)
	if err != nil || receipt.ThreadID == "" || receipt.OutputSHA256 != protocol.SHA256(f.output) {
		t.Fatalf("%+v %v", receipt, err)
	}
	candidate := sourceRef("docs/design.json.md", receipt.Output)
	candidate.Commit = sha("e")
	f.addSource(candidate, receipt.Output)
	reviewRequest := r
	reviewRequest.Role = protocol.RoleDesignReviewer
	reviewRequest.Candidate = &candidate
	reviewRequest.AuthorRun = &receipt.Run
	f.output = data(t, goodReview(candidate.SHA256))
	a, err = e.Admit(context.Background(), reviewRequest, f.fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	review, err := e.Execute(context.Background(), a)
	if err != nil || review.ThreadID == receipt.ThreadID || review.AgentInstance == receipt.AgentInstance {
		t.Fatalf("%+v %v", review, err)
	}
	if len(f.requests) != 2 || !strings.Contains(f.requests[1].Prompt, "original_inputs") || !strings.Contains(f.requests[1].Prompt, candidate.SHA256) || strings.Contains(f.requests[1].Prompt, receipt.ThreadID) || strings.Contains(f.requests[1].Prompt, receipt.AgentInstance) {
		t.Fatal("review missing exact originals or inherited author runtime context")
	}
	if _, err = e.Execute(context.Background(), a); err == nil || f.calls != 2 {
		t.Fatal("permit reused")
	}
	if _, err = json.Marshal(a); err == nil {
		t.Fatal("persistent permit")
	}
}

func TestExecutionStopsAtPolicyAndDrift(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fixture, *Engine, *Request)
	}{
		{"startup blocked", func(f *fixture, _ *Engine, _ *Request) { f.startup.Status = "blocked" }},
		{"branch drift", func(f *fixture, _ *Engine, _ *Request) { f.branch.SHA = sha("d") }},
		{"repository drift", func(f *fixture, _ *Engine, _ *Request) { f.repo.DatabaseID++ }},
		{"missing remote input", func(f *fixture, _ *Engine, _ *Request) { delete(f.sources, f.manifest.Inputs[0].Source) }},
		{"wall expired", func(f *fixture, _ *Engine, _ *Request) { f.clock = f.clock.Add(time.Hour) }},
		{"creation time unknown", func(f *fixture, _ *Engine, _ *Request) { v := f.issues[2]; v.CreatedAt = ""; f.issues[2] = v }},
		{"forbidden role", func(_ *fixture, _ *Engine, r *Request) { r.Role = protocol.RoleDeveloper }},
		{"missing review author", func(_ *fixture, _ *Engine, r *Request) { r.Role = protocol.RoleDesignReviewer }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e, r := newFixture(t)
			tc.change(f, e, &r)
			freshCalls := 0
			_, err := e.Admit(context.Background(), r, func(context.Context, Request, string) (protocol.Reference, error) {
				freshCalls++
				return protocol.Reference{}, nil
			})
			if err == nil || freshCalls != 0 || f.calls != 0 {
				t.Fatal("invalid state dispatched")
			}
		})
	}
	t.Run("round budget survives engine restart", func(t *testing.T) {
		f, e, r := newFixture(t)
		for i := 0; i < 3; i++ {
			if _, err := e.Admit(context.Background(), r, f.fresh(t)); err != nil {
				t.Fatal(err)
			}
		}
		restarted, _ := New(f, f, f)
		restarted.now = e.now
		if _, err := restarted.Admit(context.Background(), r, f.fresh(t)); !errors.Is(err, ErrBudget) {
			t.Fatal(err)
		}
		if f.calls != 0 {
			t.Fatal("reserved calls should not run")
		}
	})
	t.Run("drift after admission burns permit", func(t *testing.T) {
		f, e, r := newFixture(t)
		a, err := e.Admit(context.Background(), r, f.fresh(t))
		if err != nil {
			t.Fatal(err)
		}
		f.branch.SHA = sha("d")
		if _, err = e.Execute(context.Background(), a); err == nil {
			t.Fatal("drift accepted")
		}
		f.branch.SHA = r.ExpectedTargetSHA
		if _, err = e.Execute(context.Background(), a); err == nil || f.calls != 0 {
			t.Fatal("authority refunded")
		}
	})
	t.Run("runtime deadline and late result", func(t *testing.T) {
		f, e, r := newFixture(t)
		a, err := e.Admit(context.Background(), r, f.fresh(t))
		if err != nil {
			t.Fatal(err)
		}
		f.hook = func(ctx context.Context) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("no hard deadline")
			}
			f.clock = f.clock.Add(time.Hour)
		}
		if _, err = e.Execute(context.Background(), a); err == nil {
			t.Fatal("late result accepted")
		}
	})
	t.Run("mutation during runtime", func(t *testing.T) {
		f, e, r := newFixture(t)
		a, err := e.Admit(context.Background(), r, f.fresh(t))
		if err != nil {
			t.Fatal(err)
		}
		f.hook = func(context.Context) { delete(f.observations, a.p.run.DatabaseID) }
		if _, err = e.Execute(context.Background(), a); err == nil {
			t.Fatal("deleted reservation accepted")
		}
	})
}

func TestSharedAdmissionIsOneProcess(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		f, e, r := newFixture(t)
		a, err := e.Admit(context.Background(), r, f.fresh(t))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = e.Execute(context.Background(), a) }()
		}
		wg.Wait()
		if f.calls != 1 {
			t.Fatalf("runtime calls=%d", f.calls)
		}
	}
}

func TestSchemaOutputIsNotAuthority(t *testing.T) {
	f, e, r := newFixture(t)
	a, err := e.Admit(context.Background(), r, f.fresh(t))
	if err != nil {
		t.Fatal(err)
	}
	f.output = []byte(`{"approved":true,"actor":"controller"}`)
	if result, err := e.Execute(context.Background(), a); err == nil || !reflect.DeepEqual(result, Receipt{}) {
		t.Fatal("model acquired authority")
	}
}
