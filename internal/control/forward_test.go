package control

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests use the public command dispatcher, reopening the store on every
// command. Fixtures use real local commits and a real verification subprocess;
// no GitHub requests, Python interpreter, or running agent are involved.
type forwardFixture struct {
	t        *testing.T
	root     string
	contract Contract
}

func newForwardFixture(t *testing.T) *forwardFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("forward tests require Git")
	}
	root := filepath.Join(t.TempDir(), "project with spaces")
	if err := os.MkdirAll(filepath.Join(root, Directory, "forward-inputs"), 0755); err != nil {
		t.Fatal(err)
	}
	f := &forwardFixture{t: t, root: root}
	config := Config{SchemaVersion: 1, ProjectName: "Forward fixture", MaxAttempts: 3, ContextMaxChars: 60000}
	config.GitHub.Repository = "example/project-control"
	f.json(filepath.Join(Directory, "config.json"), config)
	f.write("docs/requirement.md", "REQ-001: The committed implementation must contain the expected marker.\n")
	f.write("docs/constraint.md", "CON-001: Preserve regional access boundaries.\n")
	f.write("implementation.txt", "baseline\n")
	f.git("init", "--quiet")
	f.git("config", "user.name", "Project Control Forward Test")
	f.git("config", "user.email", "forward-test@example.invalid")
	f.git("config", "commit.gpgsign", "false")
	f.git("config", "core.autocrlf", "false")
	f.git("config", "core.hooksPath", filepath.Join(root, ".git", "empty-hooks"))
	f.commit("initial fixture")
	f.call("source", "add", "--id", "REQ-001", "--kind", "requirement", "--path", "docs/requirement.md",
		"--title", "Fixture requirement", "--approve", "--actor", "fixture-approved-source")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f.contract = Contract{
		ID: "TASK-001", Title: "Record an implementation", Goal: "Commit the expected implementation marker",
		Requirements: []string{"REQ-001"}, Scope: []string{"region"},
		Acceptance: []Acceptance{{ID: "AC-001", Description: "The implementation marker is correct"}, {ID: "AC-002", Description: "The inspected version is a real Git commit"}},
		Verification: []Verification{{ID: "CHECK-MARKER", Argv: []string{executable, "-test.run=^TestForwardVerificationHelper$", "--", "--projectctl-forward-helper", "assert-marker"}},
			{ID: "CHECK-COMMIT", Argv: []string{"git", "rev-parse", "--verify", "HEAD"}}},
		Files: []string{"implementation.txt"}, Priority: 10,
	}
	return f
}

func (f *forwardFixture) write(relative, contents string) string {
	f.t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *forwardFixture) json(relative string, value any) string {
	f.t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		f.t.Fatal(err)
	}
	return f.write(relative, string(data)+"\n")
}

func (f *forwardFixture) git(args ...string) string {
	f.t.Helper()
	command := exec.Command("git", append([]string{"-C", f.root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := command.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %s: %v", args, output, err)
	}
	return strings.TrimSpace(string(output))
}

func (f *forwardFixture) commit(message string) string {
	f.t.Helper()
	f.git("add", "--all")
	f.git("commit", "--quiet", "-m", message)
	return f.git("rev-parse", "HEAD")
}

func (f *forwardFixture) call(args ...string) any {
	f.t.Helper()
	result, err := Execute(f.root, args)
	if err != nil {
		f.t.Fatalf("Execute(%q): %v", args, err)
	}
	return result
}

func (f *forwardFixture) reject(expected string, args ...string) {
	f.t.Helper()
	_, err := Execute(f.root, args)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(expected)) {
		f.t.Fatalf("Execute(%q): expected error containing %q, got %v", args, expected, err)
	}
}

func (f *forwardFixture) task() Task {
	f.t.Helper()
	return f.call("task", "show", f.contract.ID).(Task)
}

func (f *forwardFixture) create() {
	f.t.Helper()
	path := f.json(filepath.Join(Directory, "forward-inputs", f.contract.ID+".json"), f.contract)
	f.call("task", "create", "--file", path)
}

func (f *forwardFixture) ready() {
	f.t.Helper()
	f.create()
	f.call("task", "ready", f.contract.ID)
}

func (f *forwardFixture) submit() Task {
	f.t.Helper()
	f.ready()
	claimed := f.call("task", "claim", f.contract.ID, "--actor", "developer-a").(Task)
	f.write("implementation.txt", "implemented\n")
	sha := f.commit("implement fixture")
	return f.call("task", "submit", f.contract.ID, "--run", claimed.Run.ID, "--head", sha).(Task)
}

func (f *forwardFixture) verify() Task {
	f.t.Helper()
	submitted := f.submit()
	for _, check := range f.contract.Verification {
		ev := f.call("verify", f.contract.ID, "--run", submitted.Run.ID, "--check", check.ID, "--actor", "qa-a", "--execute").(Evidence)
		if ev.Status != "PASS" || ev.ReturnCode != 0 {
			f.t.Fatalf("verification failed: %+v", ev)
		}
	}
	return f.task()
}

func (f *forwardFixture) reviewRecord(task Task) Review {
	f.t.Helper()
	p := f.call("context", f.contract.ID, "--role", "reviewer").(map[string]any)
	criteria := map[string]Criterion{}
	refs := make([]string, 0, len(task.Run.Checks))
	for _, check := range f.contract.Verification {
		refs = append(refs, task.Run.Checks[check.ID].ID)
	}
	for _, criterion := range f.contract.Acceptance {
		criteria[criterion.ID] = Criterion{Result: "PASS", Reason: "Fixture process and Git command executed against the recorded commit", Evidence: refs}
	}
	return Review{Actor: "reviewer-a", RunID: task.Run.ID, HeadSHA: task.Run.HeadSHA, PackageDigest: p["digest"].(string),
		Criteria: criteria, Decision: "PASS", Reason: "All fixture acceptance criteria checked"}
}

func (f *forwardFixture) recordReview(review Review) string {
	f.t.Helper()
	return f.json(filepath.Join(Directory, "forward-inputs", "review.json"), review)
}

func (f *forwardFixture) reviewed() Task {
	f.t.Helper()
	verified := f.verify()
	path := f.recordReview(f.reviewRecord(verified))
	return f.call("review", f.contract.ID, "--file", path).(Task)
}

func (f *forwardFixture) editTaskForBoundary(fn func(*Task)) {
	f.t.Helper()
	store, err := Open(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	err = store.Locked(func() error {
		task, err := store.Task(f.contract.ID)
		if err != nil {
			return err
		}
		fn(&task)
		return store.SaveTask(task, "TestBoundaryFixture", "fixture", nil)
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// The compiled Go test binary is also the real argv verification program. The
// special argument is only supplied by these fixtures; ordinary go test runs
// return without subprocess behavior.
func TestForwardVerificationHelper(t *testing.T) {
	for index, arg := range os.Args {
		if arg != "--projectctl-forward-helper" || index+1 >= len(os.Args) {
			continue
		}
		switch os.Args[index+1] {
		case "assert-marker":
			data, err := os.ReadFile("implementation.txt")
			if err != nil || string(data) != "implemented\n" {
				fmt.Fprintf(os.Stderr, "unexpected implementation: %q: %v\n", data, err)
				os.Exit(3)
			}
			fmt.Println("implementation marker verified")
		case "mutate-policy":
			file, err := os.OpenFile(filepath.Join(Directory, "config.json"), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				os.Exit(4)
			}
			_, err = file.WriteString(" \n")
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				os.Exit(5)
			}
			fmt.Println("fixture changed policy while verification was running")
		case "mutate-checkout":
			if err := os.WriteFile("implementation.txt", []byte("uncommitted mutation\n"), 0644); err != nil {
				os.Exit(6)
			}
		case "fail":
			fmt.Println("intentional fixture failure")
			os.Exit(7)
		default:
			os.Exit(8)
		}
		os.Exit(0)
	}
}

func TestForwardLifecycleAndReplayFromEvents(t *testing.T) {
	f := newForwardFixture(t)
	f.ready()
	if next := f.call("next").(map[string]any)["next"]; next != f.contract.ID {
		t.Fatalf("wrong ready task: %v", next)
	}
	devPackage := f.call("context", f.contract.ID, "--role", "developer").(map[string]any)
	contents, err := os.ReadFile(devPackage["markdown"].(string))
	if err != nil || !strings.Contains(string(contents), "REQ-001: The committed implementation") {
		t.Fatalf("original source missing from cold-start package: %s, %v", contents, err)
	}
	claimed := f.call("task", "claim", f.contract.ID, "--actor", "developer-a").(Task)
	f.write("implementation.txt", "implemented\n")
	sha := f.commit("implement fixture")
	f.call("task", "submit", f.contract.ID, "--run", claimed.Run.ID, "--head", sha)
	for index, check := range f.contract.Verification {
		evidence := f.call("verify", f.contract.ID, "--run", claimed.Run.ID, "--check", check.ID, "--actor", "qa-a", "--execute").(Evidence)
		if evidence.Status != "PASS" || evidence.Binding.HeadSHA != sha || evidence.LogHash == "" {
			t.Fatalf("invalid evidence: %+v", evidence)
		}
		if index == 0 && f.task().Status != "VERIFYING" {
			t.Fatal("task reached review before every required check passed")
		}
	}
	verified := f.task()
	if verified.Status != "REVIEWING" {
		t.Fatalf("unexpected verified status: %s", verified.Status)
	}
	f.call("review", f.contract.ID, "--file", f.recordReview(f.reviewRecord(verified)))
	accepted := f.call("accept", f.contract.ID, "--actor", "controller-a").(Task)
	if accepted.Status != "ACCEPTED_FOR_MERGE" {
		t.Fatal("accept collapsed merge and acceptance boundaries")
	}
	// A ledger-only commit has a different SHA but the same product tree.
	integrationSHA := f.commit("persist evidence without changing product")
	if integrationSHA == sha {
		t.Fatal("fixture did not create a separate integration commit")
	}
	completed := f.call("close", f.contract.ID, "--merged-sha", integrationSHA, "--actor", "controller-a").(Task)
	if completed.Status != "DONE" || completed.CompletionBoundary != "local_verified_tree" || completed.MergedSHA != integrationSHA {
		t.Fatalf("unexpected completion: %+v", completed)
	}
	// A corrupt cached projection must not affect a fresh process/store.
	f.write(filepath.Join(Directory, "state.json"), "not a usable snapshot")
	reopened, err := Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Locked(func() error {
		if reopened.State.Tasks[f.contract.ID].Status != "DONE" {
			return fmt.Errorf("fresh store did not replay task completion")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.call("rebuild")
	var projected State
	if err := readJSON(filepath.Join(f.root, Directory, "state.json"), &projected); err != nil || projected.Tasks[f.contract.ID].Status != "DONE" {
		t.Fatalf("projection not rebuilt from events: %+v, %v", projected, err)
	}
	if findings := f.call("audit").(map[string]any)["findings"].([]map[string]string); len(findings) != 0 {
		t.Fatalf("completed fixture has audit findings: %v", findings)
	}
}

func TestForwardSourceChangeInvalidatesReviewAndAcceptance(t *testing.T) {
	f := newForwardFixture(t)
	f.reviewed()
	f.write("docs/requirement.md", "REQ-001: Changed acceptance requirements.\n")
	f.reject("changed", "accept", f.contract.ID, "--actor", "controller-a")
	f.call("source", "approve", "REQ-001", "--actor", "fixture-approver", "--reason", "Approve changed fixture baseline")
	f.reject("stale", "accept", f.contract.ID, "--actor", "controller-a")
	if f.task().Status != "REVIEWED" {
		t.Fatal("failed acceptance advanced task state")
	}
}

func TestForwardNewApplicableConstraintInvalidatesExistingGate(t *testing.T) {
	for _, scope := range []string{"", "region"} {
		t.Run("scope="+scope, func(t *testing.T) {
			f := newForwardFixture(t)
			f.reviewed()
			args := []string{"source", "add", "--id", "CON-001", "--kind", "constraint", "--path", "docs/constraint.md", "--title", "New invariant", "--approve", "--actor", "fixture-approver"}
			if scope != "" {
				args = append(args, "--scope", scope)
			}
			f.call(args...)
			f.reject("stale", "accept", f.contract.ID, "--actor", "controller-a")
		})
	}
}

func TestForwardUnrelatedConstraintDoesNotInvalidateTask(t *testing.T) {
	f := newForwardFixture(t)
	f.reviewed()
	f.call("source", "add", "--id", "CON-001", "--kind", "constraint", "--path", "docs/constraint.md", "--title", "Unrelated invariant", "--scope", "unrelated", "--approve", "--actor", "fixture-approver")
	f.call("accept", f.contract.ID, "--actor", "controller-a")
}

func TestForwardSameActorAndIncompleteReviewAreRejected(t *testing.T) {
	f := newForwardFixture(t)
	verified := f.verify()
	good := f.reviewRecord(verified)
	bad := good
	bad.Actor = "developer-a"
	f.reject("reviewer must differ", "review", f.contract.ID, "--file", f.recordReview(bad))
	bad = good
	bad.Criteria = map[string]Criterion{"AC-001": good.Criteria["AC-001"]}
	f.reject("exactly all", "review", f.contract.ID, "--file", f.recordReview(bad))
	bad = good
	bad.HeadSHA = strings.Repeat("a", 40)
	f.reject("run/commit mismatch", "review", f.contract.ID, "--file", f.recordReview(bad))
	bad = good
	bad.PackageDigest = "obsolete-package"
	f.reject("package mismatch", "review", f.contract.ID, "--file", f.recordReview(bad))
	f.call("review", f.contract.ID, "--file", f.recordReview(good))
	f.reject("developer cannot accept", "accept", f.contract.ID, "--actor", "developer-a")
	f.call("accept", f.contract.ID, "--actor", "controller-a")
	f.reject("developer cannot close", "close", f.contract.ID, "--merged-sha", verified.Run.HeadSHA, "--actor", "developer-a")
}

func TestForwardVerificationRequiresExecuteAndDifferentActor(t *testing.T) {
	f := newForwardFixture(t)
	submitted := f.submit()
	f.reject("--execute", "verify", f.contract.ID, "--run", submitted.Run.ID, "--check", "CHECK-COMMIT", "--actor", "qa-a")
	f.reject("QA actor must differ", "verify", f.contract.ID, "--run", submitted.Run.ID, "--check", "CHECK-COMMIT", "--actor", "developer-a", "--execute")
	if len(f.task().Run.Checks) != 0 {
		t.Fatal("rejected verification wrote evidence")
	}
}

func TestForwardDuplicateClaimLeaseExpiryAndOldRunRejection(t *testing.T) {
	f := newForwardFixture(t)
	f.ready()
	claimed := f.call("task", "claim", f.contract.ID, "--actor", "developer-a", "--lease-seconds", "3600").(Task)
	f.reject("only READY", "task", "claim", f.contract.ID, "--actor", "developer-b")
	f.reject("active lease", "task", "retry", f.contract.ID, "--reason", "Premature retry")
	// Construct expiry explicitly so slower runners and second boundaries cannot race.
	f.editTaskForBoundary(func(task *Task) { task.Run.LeaseUntil = time.Now().Unix() - 1 })
	f.reject("lease expired", "task", "heartbeat", f.contract.ID, "--run", claimed.Run.ID)
	f.call("task", "retry", f.contract.ID, "--reason", "Original worker lease expired")
	f.call("task", "ready", f.contract.ID)
	newAttempt := f.call("task", "claim", f.contract.ID, "--actor", "developer-b").(Task)
	if newAttempt.Run.ID == claimed.Run.ID || len(newAttempt.History) != 1 || newAttempt.Attempts != 2 {
		t.Fatal("retry failed to retain history and create a new run")
	}
	f.reject("stale or unknown", "task", "heartbeat", f.contract.ID, "--run", claimed.Run.ID)
	f.reject("stale or unknown", "task", "submit", f.contract.ID, "--run", claimed.Run.ID, "--head", newAttempt.Run.BaseSHA)
}

func TestForwardModifiedEventAndEvidenceAreRejected(t *testing.T) {
	t.Run("event", func(t *testing.T) {
		f := newForwardFixture(t)
		paths, err := filepath.Glob(filepath.Join(f.root, Directory, "events", "*.json"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("missing fixture event: %v", err)
		}
		var event Event
		if err := readJSON(paths[0], &event); err != nil {
			t.Fatal(err)
		}
		event.Actor = "changed-without-updating-hash"
		if err := WriteJSON(paths[0], event); err != nil {
			t.Fatal(err)
		}
		f.reject("hash/sequence mismatch", "status")
	})
	t.Run("evidence", func(t *testing.T) {
		f := newForwardFixture(t)
		reviewed := f.reviewed()
		f.write(reviewed.Run.Checks["CHECK-MARKER"].Log, "forged passing output\n")
		f.reject("modified evidence", "accept", f.contract.ID, "--actor", "controller-a")
	})
}

func TestForwardDependencyCycleAndMissingDependencyBlock(t *testing.T) {
	f := newForwardFixture(t)
	f.contract.Dependencies = []string{"TASK-002"}
	f.create()
	f.reject("not DONE", "task", "ready", f.contract.ID)
	second := f.contract
	second.ID, second.Dependencies = "TASK-002", []string{f.contract.ID}
	path := f.json(filepath.Join(Directory, "forward-inputs", "second.json"), second)
	f.reject("dependency cycle", "task", "create", "--file", path)
	f.reject("unknown task", "task", "show", "TASK-002")
}

func TestForwardContextBudgetRefusesTruncation(t *testing.T) {
	f := newForwardFixture(t)
	f.ready()
	before, err := filepath.Glob(filepath.Join(f.root, Directory, "packages", f.contract.ID, "*"))
	if err != nil {
		t.Fatal(err)
	}
	f.reject("instead of truncating", "context", f.contract.ID, "--role", "reviewer", "--max-chars", "100")
	after, err := filepath.Glob(filepath.Join(f.root, Directory, "packages", f.contract.ID, "*"))
	if err != nil || len(after) != len(before) {
		t.Fatalf("over-budget request persisted a partial context: %v", err)
	}
	packageInfo := f.call("context", f.contract.ID, "--role", "reviewer").(map[string]any)
	raw, err := os.ReadFile(packageInfo["json"].(string))
	if err != nil || !strings.Contains(string(raw), "AC-001") || !strings.Contains(string(raw), "AC-002") || !strings.Contains(string(raw), "REQ-001: The committed implementation") {
		t.Fatalf("full context omitted mandatory content: %s, %v", raw, err)
	}
}

func TestForwardPolicyChangeDuringVerificationCannotBecomePass(t *testing.T) {
	f := newForwardFixture(t)
	f.contract.Verification[0].Argv[len(f.contract.Verification[0].Argv)-1] = "mutate-policy"
	f.contract.Verification = f.contract.Verification[:1]
	submitted := f.submit()
	f.reject("inputs changed while verification", "verify", f.contract.ID, "--run", submitted.Run.ID, "--check", "CHECK-MARKER", "--actor", "qa-a", "--execute")
	current := f.task()
	if current.Status != "VERIFYING" || current.Run.Checks["CHECK-MARKER"].Status != "RUNNING" {
		t.Fatal("stale verification was applied as a passing result")
	}
	logs, err := filepath.Glob(filepath.Join(f.root, Directory, "evidence", "*.log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("interrupted verification evidence was not retained: %v", err)
	}
	f.call("task", "retry", f.contract.ID, "--reason", "Policy changed during verification")
	f.call("task", "ready", f.contract.ID)
	f.call("task", "claim", f.contract.ID, "--actor", "developer-b")
	f.reject("stale or unknown", "task", "submit", f.contract.ID, "--run", submitted.Run.ID, "--head", submitted.Run.HeadSHA)
}

func TestForwardVerificationDetectsChangedCheckoutAndFailure(t *testing.T) {
	for _, tc := range []struct{ mode, status string }{{"mutate-checkout", "ERROR"}, {"fail", "FAIL"}} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newForwardFixture(t)
			f.contract.Verification[0].Argv[len(f.contract.Verification[0].Argv)-1] = tc.mode
			f.contract.Verification = f.contract.Verification[:1]
			submitted := f.submit()
			evidence := f.call("verify", f.contract.ID, "--run", submitted.Run.ID, "--check", "CHECK-MARKER", "--actor", "qa-a", "--execute").(Evidence)
			if evidence.Status != tc.status || f.task().Status != "VERIFYING" {
				t.Fatalf("invalid verification outcome: %+v", evidence)
			}
			f.reject("REVIEWED", "accept", f.contract.ID, "--actor", "controller-a")
		})
	}
}

func TestForwardIntegrationTreeChangeRequiresNewVerification(t *testing.T) {
	f := newForwardFixture(t)
	f.reviewed()
	f.call("accept", f.contract.ID, "--actor", "controller-a")
	f.write("implementation.txt", "changed after review\n")
	sha := f.commit("unreviewed integration change")
	f.reject("tree differs", "close", f.contract.ID, "--merged-sha", sha, "--actor", "controller-a")
	if f.task().Status != "ACCEPTED_FOR_MERGE" {
		t.Fatal("unverified integration was marked DONE")
	}
}

func TestForwardSourceAndEvidencePathsCannotEscapeProject(t *testing.T) {
	f := newForwardFixture(t)
	f.reject("path escapes", "source", "add", "--id", "REQ-ESCAPE", "--kind", "requirement", "--path", "../outside.txt", "--title", "Outside source", "--approve")
	f.reviewed()
	// Simulate a restored but malformed record through the store API; even a
	// structurally valid event cannot authorize reading outside the project.
	f.editTaskForBoundary(func(task *Task) {
		evidence := task.Run.Checks["CHECK-MARKER"]
		evidence.Log = "../outside.log"
		task.Run.Checks["CHECK-MARKER"] = evidence
	})
	f.reject("path escapes", "accept", f.contract.ID, "--actor", "controller-a")
}

func TestForwardInvalidUTF8SourceIsRejectedBeforeSnapshot(t *testing.T) {
	f := newForwardFixture(t)
	path := f.write("docs/invalid.md", string([]byte{'x', 0xff, 'y'}))
	if info, err := os.Stat(path); err != nil || info.Size() != 3 {
		t.Fatal("invalid UTF-8 fixture was not written")
	}
	f.reject("UTF-8", "source", "add", "--id", "REQ-BINARY", "--kind", "requirement", "--path", "docs/invalid.md", "--title", "Invalid source", "--approve")
}

func TestForwardSeparateWorktreeUsesOneAuthoritativeLedger(t *testing.T) {
	f := newForwardFixture(t)
	f.ready()
	rootHead := f.git("rev-parse", "HEAD")
	workerPath := filepath.Join(t.TempDir(), "worker checkout")
	f.git("worktree", "add", "--detach", workerPath, "HEAD")
	worker := &forwardFixture{t: t, root: workerPath, contract: f.contract}
	call := func(args ...string) any {
		t.Helper()
		result, err := ExecuteWorkspace(f.root, workerPath, args)
		if err != nil {
			t.Fatalf("ExecuteWorkspace(%q): %v", args, err)
		}
		return result
	}
	claimed := call("task", "claim", f.contract.ID, "--actor", "developer-a").(Task)
	worker.write("implementation.txt", "implemented\n")
	workerSHA := worker.commit("implement in isolated checkout")
	if f.git("rev-parse", "HEAD") != rootHead || workerSHA == rootHead {
		t.Fatal("worker changed the ledger checkout's HEAD")
	}
	call("task", "submit", f.contract.ID, "--run", claimed.Run.ID, "--head", workerSHA)
	for _, check := range f.contract.Verification {
		ev := call("verify", f.contract.ID, "--run", claimed.Run.ID, "--check", check.ID, "--actor", "qa-a", "--execute").(Evidence)
		if ev.Status != "PASS" || ev.Binding.HeadSHA != workerSHA {
			t.Fatalf("verification did not use worker code: %+v", ev)
		}
		if _, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(ev.Log))); err != nil {
			t.Fatalf("evidence missing from authoritative ledger: %v", err)
		}
		if _, err := os.Stat(filepath.Join(workerPath, filepath.FromSlash(ev.Log))); !os.IsNotExist(err) {
			t.Fatalf("verification wrote a second ledger in worker: %v", err)
		}
	}
	verified := f.task()
	call("review", f.contract.ID, "--file", f.recordReview(f.reviewRecord(verified)))
	call("accept", f.contract.ID, "--actor", "controller-a")
	completed := call("close", f.contract.ID, "--merged-sha", workerSHA, "--actor", "controller-a").(Task)
	if completed.Status != "DONE" || f.task().Status != "DONE" {
		t.Fatal("authoritative ledger did not retain worktree completion")
	}
	if _, err := os.Stat(filepath.Join(workerPath, Directory, "events")); !os.IsNotExist(err) {
		t.Fatalf("worker received an independent event ledger: %v", err)
	}
	other := newForwardFixture(t)
	if _, err := ExecuteWorkspace(f.root, other.root, []string{"status"}); err == nil || !strings.Contains(err.Error(), "share the ledger root") {
		t.Fatalf("unrelated repository accepted as worker: %v", err)
	}
}
