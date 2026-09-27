package startup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

// PR14-P2-MALFORMED-CLOSED-CONTROL-SKIPPED: a visible marked object is
// conflicting evidence, not proof that bootstrap has never happened.
func TestMalformedClosedControlCannotBecomeMissing(t *testing.T) {
	f, anchor := fixtureRemote(t)
	body := ControlMarker + " {not-valid-json}"
	f.issues = []gh.Issue{{Resource: gh.Resource{Kind: "issue", Number: 10, DatabaseID: 1010, NodeID: "I_10"}, URL: "https://github.com/octo/pipeline/issues/10", State: "closed", Author: fixtureIdentity(1), Content: gh.Content{Body: &body, BodySHA256: protocol.SHA256([]byte(body))}}}
	e := fixtureEngine(t, f)
	s, err := e.Reconcile(context.Background(), anchor)
	if !errors.Is(err, ErrBlocked) || s.Status != "blocked" || hasReason(s, "control_issue_missing", "repository") {
		t.Fatalf("malformed closed control was skipped: state=%+v err=%v", s, err)
	}
	if _, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t)); !errors.Is(err, ErrBlocked) {
		t.Fatalf("malformed closed control obtained creation authority: %v", err)
	}
	if _, err := e.Initialize(context.Background(), anchor, BootstrapAdmission{}); !errors.Is(err, ErrBlocked) || f.creates != 0 {
		t.Fatalf("malformed closed control permitted bootstrap: err=%v creates=%d", err, f.creates)
	}
}

func historyControl(t *testing.T, f *fakeRemote, number int64, cycle, operation string) (gh.Issue, protocol.SourceReference) {
	t.Helper()
	m := fixtureManifest()
	m.Cycle, m.OperationID = cycle, operation
	m.Policy.Roles.Controller = fixtureIdentity(99) // historical, not current /user
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	ref := fixtureSource("docs/history-"+cycle+".json", data)
	f.addSource(ref, data)
	record := ControlRecord{Schema: ControlSchema, Kind: "project_control", Repository: m.Repository, Cycle: cycle, OperationID: operation, Manifest: ref}
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := protocol.CanonicalJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	body := ControlMarker + " " + string(canonical)
	return gh.Issue{Resource: gh.Resource{Kind: "issue", Number: number, DatabaseID: 1000 + number, NodeID: fmt.Sprintf("I_%d", number)}, URL: fmt.Sprintf("https://github.com/octo/pipeline/issues/%d", number), State: "closed", Author: m.Policy.Roles.Controller, Content: gh.Content{Body: &body, BodySHA256: protocol.SHA256([]byte(body))}}, ref
}

func replaceControlBody(issue *gh.Issue, body string) {
	issue.Content.Body = &body
	issue.Content.BodySHA256 = protocol.SHA256([]byte(body))
}

func editHistoryControl(t *testing.T, issue *gh.Issue, change func(*ControlRecord)) {
	t.Helper()
	var record ControlRecord
	if err := json.Unmarshal([]byte(strings.TrimPrefix(*issue.Content.Body, ControlMarker)), &record); err != nil {
		t.Fatal(err)
	}
	change(&record)
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := protocol.CanonicalJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	replaceControlBody(issue, ControlMarker+" "+string(canonical))
}

func TestMarkedControlValidationPrecedesLifecycleFiltering(t *testing.T) {
	type mutation func(*testing.T, *fakeRemote, Anchor, *gh.Issue, protocol.SourceReference)
	cases := []struct {
		name   string
		change mutation
	}{
		{"invalid JSON", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			replaceControlBody(i, ControlMarker+" {")
		}},
		{"missing fields", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			replaceControlBody(i, ControlMarker+" {}")
		}},
		{"duplicate field", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			replaceControlBody(i, strings.Replace(*i.Content.Body, `"kind":"project_control"`, `"kind":"project_control","kind":"project_control"`, 1))
		}},
		{"unknown field", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			replaceControlBody(i, strings.TrimSuffix(*i.Content.Body, "}")+`,"unexpected":true}`)
		}},
		{"marker prefix", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			replaceControlBody(i, "explanation "+*i.Content.Body)
		}},
		{"repeated marker", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			replaceControlBody(i, *i.Content.Body+ControlMarker)
		}},
		{"noncanonical edit", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			replaceControlBody(i, *i.Content.Body+" ")
		}},
		{"digest drift", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			i.Content.BodySHA256 = strings.Repeat("f", 64)
		}},
		{"wrong URL", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			i.URL = "https://github.com/foreign/repo/issues/90"
		}},
		{"invalid author", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) { i.Author.ID = 0 }},
		{"wrong historical publisher", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			i.Author = fixtureIdentity(98)
		}},
		{"invalid native ID", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) { i.DatabaseID = 0 }},
		{"invalid native node", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) { i.NodeID = " " }},
		{"invalid native number", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) { i.Number = 0 }},
		{"PR masquerade", func(_ *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			i.Kind = "pull_request"
		}},
		{"historical source deleted", func(_ *testing.T, f *fakeRemote, _ Anchor, _ *gh.Issue, r protocol.SourceReference) {
			delete(f.sources, r)
		}},
		{"historical source edited", func(_ *testing.T, f *fakeRemote, _ Anchor, _ *gh.Issue, r protocol.SourceReference) {
			v := f.sources[r]
			v.Bytes = []byte("edited")
			f.sources[r] = v
		}},
		{"schema", func(t *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			editHistoryControl(t, i, func(r *ControlRecord) { r.Schema = "projectctl.control/v2" })
		}},
		{"kind", func(t *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			editHistoryControl(t, i, func(r *ControlRecord) { r.Kind = "unrelated" })
		}},
		{"repository", func(t *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			editHistoryControl(t, i, func(r *ControlRecord) { r.Repository.DatabaseID++ })
		}},
		{"cycle", func(t *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			editHistoryControl(t, i, func(r *ControlRecord) { r.Cycle = "" })
		}},
		{"operation", func(t *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			editHistoryControl(t, i, func(r *ControlRecord) { r.OperationID = "" })
		}},
		{"mutable source", func(t *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			editHistoryControl(t, i, func(r *ControlRecord) { r.Manifest.Commit = "main" })
		}},
		{"unsafe source path", func(t *testing.T, _ *fakeRemote, _ Anchor, i *gh.Issue, _ protocol.SourceReference) {
			editHistoryControl(t, i, func(r *ControlRecord) { r.Manifest.Path = "../secret" })
		}},
		{"source bound to current cycle", func(t *testing.T, _ *fakeRemote, a Anchor, i *gh.Issue, _ protocol.SourceReference) {
			editHistoryControl(t, i, func(r *ControlRecord) { r.Manifest = a.Manifest })
		}},
		{"historical manifest identity mismatch", func(t *testing.T, f *fakeRemote, _ Anchor, i *gh.Issue, r protocol.SourceReference) {
			var m Manifest
			if err := json.Unmarshal(f.sources[r].Bytes, &m); err != nil {
				t.Fatal(err)
			}
			m.OperationID = "other-operation"
			data, _ := json.Marshal(m)
			ref := fixtureSource(r.Path, data)
			f.addSource(ref, data)
			editHistoryControl(t, i, func(r *ControlRecord) { r.Manifest = ref })
		}},
	}
	for _, tc := range cases {
		for _, state := range []string{"open", "closed"} {
			for _, order := range []string{"alone", "before current", "after current"} {
				t.Run(tc.name+"/"+state+"/"+order, func(t *testing.T) {
					f, anchor := fixtureRemote(t)
					e := fixtureEngine(t, f)
					permit, err := e.AdmitBootstrap(context.Background(), anchor, f.freshSource(t))
					if err != nil {
						t.Fatal(err)
					}
					if order != "alone" {
						if err := f.CreateControl(context.Background(), permit.state.request); err != nil {
							t.Fatal(err)
						}
					}
					bad, ref := historyControl(t, f, 90, "old-cycle", "old-operation")
					bad.State = state
					tc.change(t, f, anchor, &bad, ref)
					if order == "after current" {
						f.issues = append(f.issues, bad)
					} else {
						f.issues = append([]gh.Issue{bad}, f.issues...)
					}
					before := f.creates
					s, err := e.Reconcile(context.Background(), anchor)
					if !errors.Is(err, ErrBlocked) || s.Status != "blocked" || hasReason(s, "control_issue_missing", "repository") {
						t.Fatalf("marked record skipped: %+v %v", s, err)
					}
					calls := 0
					_, err = e.AdmitBootstrap(context.Background(), anchor, func(context.Context, BootstrapRequest) (protocol.SourceReference, error) {
						calls++
						return protocol.SourceReference{}, nil
					})
					if !errors.Is(err, ErrBlocked) || calls != 0 {
						t.Fatalf("invalid history requested new authority: err=%v calls=%d", err, calls)
					}
					if _, err = e.Initialize(context.Background(), anchor, permit); !errors.Is(err, ErrBlocked) || f.creates != before {
						t.Fatalf("invalid history wrote: %v", err)
					}
					// Once this attempt observed a contradiction it cannot later turn
					// an edited-away/deleted record into reusable first-create authority.
					f.issues = nil
					if _, err = fixtureEngine(t, f).Initialize(context.Background(), anchor, permit); !errors.Is(err, ErrUncertain) || f.creates != before {
						t.Fatalf("old permit resurrected: %v", err)
					}
				})
			}
		}
	}
}

func TestValidatedHistoricalControlClassification(t *testing.T) {
	for _, tc := range []struct {
		name, cycle, operation, want string
		current                      bool
	}{
		{"historical only", "old-cycle", "old-operation", "bootstrap_required", false},
		{"historical plus current", "old-cycle", "old-operation", "ready_for_design", true},
		{"closed current cycle", "delivery-1", "startup-delivery-1", "blocked", false},
		{"same cycle different operation", "delivery-1", "other-operation", "blocked", false},
		{"same operation different cycle", "old-cycle", "startup-delivery-1", "blocked", false},
		{"closed current plus open current", "delivery-1", "startup-delivery-1", "blocked", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, anchor := fixtureRemote(t)
			e := fixtureEngine(t, f)
			if tc.current {
				_, v, err := e.load(context.Background(), anchor)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.CreateControl(context.Background(), v.request); err != nil {
					t.Fatal(err)
				}
			}
			history, _ := historyControl(t, f, 90, tc.cycle, tc.operation)
			f.issues = append(f.issues, history)
			s, err := e.Reconcile(context.Background(), anchor)
			if s.Status != tc.want || (tc.want == "blocked" && !errors.Is(err, ErrBlocked)) || (tc.want != "blocked" && err != nil) {
				t.Fatalf("%+v %v", s, err)
			}
			if tc.want == "blocked" && !hasReason(s, "cycle_already_closed", "control_issue") {
				t.Fatal(s.Reasons)
			}
		})
	}
}
