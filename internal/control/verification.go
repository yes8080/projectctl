package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	gh "github.com/yes8080/projectctl/internal/github"
)

func (s *Store) verifyCommand(args []string) (any, error) {
	id, rest, e := splitID(args)
	if e != nil {
		return nil, e
	}
	f := flags("verify")
	runID := f.String("run", "", "")
	checkID := f.String("check", "", "")
	actor := f.String("actor", "", "")
	execute := f.Bool("execute", false, "")
	timeout := f.Int("timeout", 300, "")
	if e = parse(f, rest); e != nil {
		return nil, e
	}
	if !*execute {
		return nil, errors.New("verify executes task argv locally; inspect it and pass --execute with existing user authorization")
	}
	if *timeout < 1 {
		return nil, errors.New("timeout must be positive")
	}
	if e = need(*actor, "actor"); e != nil {
		return nil, e
	}
	var selected Verification
	var binding Binding
	ev := Evidence{ID: newID("EV-"), Status: "RUNNING", CheckID: *checkID, Actor: *actor, StartedAt: timestamp(), ReturnCode: -1}
	e = s.Locked(func() error {
		t, e := s.Task(id)
		if e != nil {
			return e
		}
		if !contains([]string{"VERIFYING", "REVIEWING"}, t.Status) {
			return errors.New("task is not awaiting verification")
		}
		run, e := s.ActiveRun(&t, *runID, false)
		if e != nil {
			return e
		}
		if *actor == run.Developer {
			return errors.New("QA actor must differ from developer (cooperative check)")
		}
		binding, e = s.Binding(t)
		if e != nil {
			return e
		}
		if binding != run.Binding {
			return errors.New("submitted inputs are stale")
		}
		found := false
		for _, c := range t.Verification {
			if c.ID == *checkID {
				selected = c
				found = true
			}
		}
		if !found {
			return errors.New("unknown verification check")
		}
		if e = CleanCheckout(s.Worktree, run.HeadSHA); e != nil {
			return e
		}
		ev.Binding = binding
		ev.Argv = selected.Argv
		run.Checks[*checkID] = ev
		t.Status = "VERIFYING"
		return s.SaveTask(t, "VerificationStarted", *actor, nil)
	})
	if e != nil {
		return nil, e
	}
	rel := filepath.Join(Directory, "evidence", ev.ID+".log")
	logPath, e := SafePath(s.Root, rel)
	if e != nil {
		return nil, e
	}
	log, e := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, selected.Argv[0], selected.Argv[1:]...)
	cmd.Dir = s.Worktree
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.WaitDelay = 2 * time.Second
	runErr := cmd.Run()
	ev.Status = "PASS"
	ev.ReturnCode = 0
	if runErr != nil {
		ev.Status = "ERROR"
		ev.ReturnCode = -1
		var exit *exec.ExitError
		if errors.As(runErr, &exit) && ctx.Err() == nil {
			ev.Status = "FAIL"
			ev.ReturnCode = exit.ExitCode()
		}
		fmt.Fprintf(log, "\nVerification error: %v\n", runErr)
	}
	if e = CleanCheckout(s.Worktree, binding.HeadSHA); e != nil {
		ev.Status = "ERROR"
		fmt.Fprintf(log, "\nInvalid verification checkout: %v\n", e)
	}
	if e = log.Close(); e != nil {
		return nil, e
	}
	bytes, e := os.ReadFile(logPath)
	if e != nil {
		return nil, e
	}
	ev.Log = filepath.ToSlash(rel)
	ev.LogHash = ByteHash(bytes)
	ev.FinishedAt = timestamp()
	ev.Environment = map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "runtime": runtime.Version()}
	e = s.Locked(func() error {
		t, e := s.Task(id)
		if e != nil {
			return e
		}
		if !contains([]string{"VERIFYING", "REVIEWING"}, t.Status) {
			return errors.New("task changed while verification ran; log retained, result not applied")
		}
		run, e := s.ActiveRun(&t, *runID, false)
		if e != nil {
			return e
		}
		if run.Checks[*checkID].ID != ev.ID {
			return errors.New("a newer verification superseded this result")
		}
		current, e := s.Binding(t)
		if e != nil {
			return e
		}
		if current != binding {
			return errors.New("inputs changed while verification ran; evidence not applied")
		}
		run.Checks[*checkID] = ev
		allPass := true
		for _, check := range t.Verification {
			if run.Checks[check.ID].Status != "PASS" {
				allPass = false
			}
		}
		t.Status = "VERIFYING"
		if allPass {
			t.Status = "REVIEWING"
		}
		return s.SaveTask(t, "VerificationFinished", *actor, nil)
	})
	return ev, e
}
func (s *Store) CheckedEvidence(t Task) (map[string]Evidence, error) {
	binding, e := s.Binding(t)
	if e != nil {
		return nil, e
	}
	if t.Run.Binding != binding {
		return nil, errors.New("submitted inputs changed; resubmit through a new attempt")
	}
	result := map[string]Evidence{}
	for _, check := range t.Verification {
		ev, ok := t.Run.Checks[check.ID]
		if !ok || ev.Status != "PASS" {
			return nil, fmt.Errorf("missing passing check %s", check.ID)
		}
		if ev.Binding != binding {
			return nil, fmt.Errorf("stale check %s", check.ID)
		}
		p, e := SafePath(s.Root, filepath.FromSlash(ev.Log))
		if e != nil {
			return nil, e
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return nil, e
		}
		if ByteHash(b) != ev.LogHash {
			return nil, fmt.Errorf("modified evidence log %s", check.ID)
		}
		result[ev.ID] = ev
	}
	return result, nil
}
func (s *Store) reviewCommand(args []string) (any, error) {
	id, rest, e := splitID(args)
	if e != nil {
		return nil, e
	}
	f := flags("review")
	file := f.String("file", "", "")
	if e = parse(f, rest); e != nil {
		return nil, e
	}
	t, e := s.Task(id)
	if e != nil {
		return nil, e
	}
	if t.Status != "REVIEWING" {
		return nil, errors.New("task is not awaiting independent review")
	}
	evidence, e := s.CheckedEvidence(t)
	if e != nil {
		return nil, e
	}
	var review Review
	if e = readJSON(*file, &review); e != nil {
		return nil, e
	}
	if strings.TrimSpace(review.Actor) == "" || review.Actor == t.Run.Developer {
		return nil, errors.New("reviewer must differ from developer")
	}
	if review.RunID != t.Run.ID || review.HeadSHA != t.Run.HeadSHA {
		return nil, errors.New("review run/commit mismatch")
	}
	p, e := s.Package(t, "reviewer", 0)
	if e != nil {
		return nil, e
	}
	if review.PackageDigest != p["digest"] {
		return nil, errors.New("reviewer context package mismatch")
	}
	if len(review.Criteria) != len(t.Acceptance) {
		return nil, errors.New("review must address exactly all acceptance criteria")
	}
	for _, a := range t.Acceptance {
		r, ok := review.Criteria[a.ID]
		if !ok || !contains([]string{"PASS", "FAIL"}, r.Result) || strings.TrimSpace(r.Reason) == "" {
			return nil, fmt.Errorf("invalid review criterion %s", a.ID)
		}
		if len(r.Evidence) == 0 {
			return nil, fmt.Errorf("criterion %s needs current evidence IDs", a.ID)
		}
		for _, ref := range r.Evidence {
			if _, ok = evidence[ref]; !ok {
				return nil, fmt.Errorf("unknown/stale evidence %s", ref)
			}
		}
		if review.Decision == "PASS" && r.Result != "PASS" {
			return nil, errors.New("PASS conflicts with failed acceptance criterion")
		}
	}
	if !contains([]string{"PASS", "FAIL"}, review.Decision) || strings.TrimSpace(review.Reason) == "" {
		return nil, errors.New("review needs decision PASS|FAIL and reason")
	}
	review.Binding, e = s.Binding(t)
	if e != nil {
		return nil, e
	}
	review.RecordedAt = timestamp()
	t.Run.Review = &review
	t.Status = "REVIEWED"
	if review.Decision == "FAIL" {
		t.Status = "CHANGES_REQUESTED"
		t.Remediation = map[string]any{"id": fmt.Sprintf("FIX-%s-%02d", t.ID, t.Attempts), "reason": review.Reason, "criteria": review.Criteria, "run_id": review.RunID}
	}
	return t, s.SaveTask(t, "ReviewRecorded", review.Actor, nil)
}
func (s *Store) acceptCommand(args []string) (any, error) {
	id, rest, e := splitID(args)
	if e != nil {
		return nil, e
	}
	f := flags("accept")
	actor := f.String("actor", "", "")
	if e = parse(f, rest); e != nil {
		return nil, e
	}
	if e = need(*actor, "actor"); e != nil {
		return nil, e
	}
	t, e := s.Task(id)
	if e != nil {
		return nil, e
	}
	if t.Status != "REVIEWED" {
		return nil, errors.New("only independently REVIEWED tasks can be accepted")
	}
	if _, e = s.CheckedEvidence(t); e != nil {
		return nil, e
	}
	review := t.Run.Review
	binding, e := s.Binding(t)
	if e != nil {
		return nil, e
	}
	if review == nil || review.Decision != "PASS" || review.Binding != binding {
		return nil, errors.New("independent review is missing or stale")
	}
	p, e := s.Package(t, "reviewer", 0)
	if e != nil {
		return nil, e
	}
	if review.PackageDigest != p["digest"] {
		return nil, errors.New("review context is stale")
	}
	if *actor == t.Run.Developer {
		return nil, errors.New("developer cannot accept their own task")
	}
	t.Status = "ACCEPTED_FOR_MERGE"
	return t, s.SaveTask(t, "TaskAcceptedForMerge", *actor, nil)
}
func (s *Store) closeCommand(args []string) (any, error) {
	id, rest, e := splitID(args)
	if e != nil {
		return nil, e
	}
	f := flags("close")
	actor := f.String("actor", "", "")
	sha := f.String("merged-sha", "", "")
	pr := f.Int("pr", 0, "")
	if e = parse(f, rest); e != nil {
		return nil, e
	}
	if e = need(*actor, "actor"); e != nil {
		return nil, e
	}
	t, e := s.Task(id)
	if e != nil {
		return nil, e
	}
	if t.Status != "ACCEPTED_FOR_MERGE" {
		return nil, errors.New("task has not passed acceptance gate")
	}
	if _, e = s.CheckedEvidence(t); e != nil {
		return nil, e
	}
	binding, e := s.Binding(t)
	if e != nil {
		return nil, e
	}
	if t.Run.Review == nil || t.Run.Review.Binding != binding {
		return nil, errors.New("review is stale")
	}
	if *actor == t.Run.Developer {
		return nil, errors.New("developer cannot close their own task")
	}
	merged, e := ResolveCommit(s.Worktree, *sha)
	if e != nil {
		return nil, e
	}
	if e = CleanCheckout(s.Worktree, merged); e != nil {
		return nil, e
	}
	tree, e := ProductTree(s.Worktree, merged)
	if e != nil {
		return nil, e
	}
	if tree != t.Run.ProductTree {
		return nil, errors.New("integrated product tree differs from tested tree; submit integration candidate in a new attempt")
	}
	boundary := "local_verified_tree"
	var facts any
	if *pr > 0 {
		remote, e := gh.InspectPR(s.Config.GitHub.Repository, *pr)
		if e != nil {
			return nil, e
		}
		if remote["state"] != "MERGED" {
			return nil, errors.New("GitHub PR is not merged")
		}
		merge, _ := remote["mergeCommit"].(map[string]any)
		if merge["oid"] != merged || remote["headRefOid"] != t.Run.HeadSHA {
			return nil, errors.New("GitHub merge/head do not match verified objects")
		}
		boundary = "github_merged"
		facts = remote
	}
	t.Status = "DONE"
	t.MergedSHA = merged
	t.CompletionBoundary = boundary
	return t, s.SaveTask(t, "TaskCompleted", *actor, map[string]any{"remote_pr": facts})
}
