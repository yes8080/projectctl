package control

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	gh "github.com/yes8080/projectctl/internal/github"
)

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }
func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}
func parse(f *flag.FlagSet, args []string) error {
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	return nil
}
func need(v, name string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}
func splitID(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, errors.New("task/record ID is required")
	}
	if e := validID(args[0]); e != nil {
		return "", nil, e
	}
	return args[0], args[1:], nil
}

func (s *Store) sourceCommand(args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("source add|approve required")
	}
	command := args[0]
	args = args[1:]
	switch command {
	case "add":
		f := flags("source add")
		id := f.String("id", "", "")
		kind := f.String("kind", "", "")
		path := f.String("path", "", "")
		title := f.String("title", "", "")
		actor := f.String("actor", "planner", "")
		approve := f.Bool("approve", false, "")
		var scope stringsFlag
		f.Var(&scope, "scope", "")
		if e := parse(f, args); e != nil {
			return nil, e
		}
		if e := validID(*id); e != nil {
			return nil, e
		}
		if _, ok := s.State.Sources[*id]; ok {
			return nil, errors.New("source exists; use source approve to refresh")
		}
		if !contains([]string{"requirement", "constraint", "adr", "design"}, *kind) {
			return nil, errors.New("invalid source kind")
		}
		if e := need(*title, "title"); e != nil {
			return nil, e
		}
		content, h, e := s.SourceText(*path)
		if e != nil {
			return nil, e
		}
		v := Source{ID: *id, Kind: *kind, Path: filepath.ToSlash(*path), Title: *title, Scope: scope, Content: content, ContentHash: h, Revision: 1, Approved: *approve}
		if *approve {
			v.ApprovedBy = *actor
		}
		return v, s.Emit("SourceRegistered", *actor, "source", v, nil)
	case "approve":
		id, rest, e := splitID(args)
		if e != nil {
			return nil, e
		}
		f := flags("source approve")
		actor := f.String("actor", "", "")
		reason := f.String("reason", "", "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		if e = need(*actor, "actor"); e != nil {
			return nil, e
		}
		if e = need(*reason, "reason"); e != nil {
			return nil, e
		}
		v, ok := s.State.Sources[id]
		if !ok {
			return nil, errors.New("unknown source")
		}
		content, h, e := s.SourceText(v.Path)
		if e != nil {
			return nil, e
		}
		v.Content = content
		v.ContentHash = h
		v.Revision++
		v.Approved = true
		v.ApprovedBy = *actor
		return v, s.Emit("SourceApproved", *actor, "source", v, map[string]string{"reason": *reason})
	}
	return nil, errors.New("unknown source command")
}
func (s *Store) taskCommand(args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("task subcommand required")
	}
	command := args[0]
	args = args[1:]
	if command == "create" || command == "revise" {
		f := flags(command)
		file := f.String("file", "", "")
		actor := f.String("actor", "planner", "")
		reason := f.String("reason", "", "")
		if e := parse(f, args); e != nil {
			return nil, e
		}
		var c Contract
		if e := readJSON(*file, &c); e != nil {
			return nil, e
		}
		if e := ValidateContract(c); e != nil {
			return nil, e
		}
		old, exists := s.State.Tasks[c.ID]
		c.Revision = 1
		if command == "create" && exists {
			return nil, errors.New("task already exists")
		}
		if command == "revise" {
			if !exists {
				return nil, errors.New("unknown task")
			}
			if !contains([]string{"PROPOSED", "BLOCKED", "CHANGES_REQUESTED"}, old.Status) {
				return nil, errors.New("block or retry before revising task")
			}
			if e := need(*reason, "reason"); e != nil {
				return nil, e
			}
			c.Revision = old.Revision + 1
		}
		t := Task{Contract: c, Status: "PROPOSED", SourceVersions: map[string]SourceVersion{}, History: []Run{}}
		if exists {
			t.History = old.History
			if old.Run != nil {
				t.History = append(t.History, *old.Run)
			}
			t.Attempts = old.Attempts
		}
		candidates := map[string]Task{}
		for id, x := range s.State.Tasks {
			candidates[id] = x
		}
		candidates[t.ID] = t
		if e := CheckCycles(candidates); e != nil {
			return nil, e
		}
		return t, s.SaveTask(t, "Task"+strings.ToUpper(command[:1])+command[1:], *actor, map[string]string{"reason": *reason})
	}
	id, rest, e := splitID(args)
	if e != nil {
		return nil, e
	}
	t, e := s.Task(id)
	if e != nil {
		return nil, e
	}
	f := flags(command)
	switch command {
	case "show":
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		return t, nil
	case "ready":
		actor := f.String("actor", "controller", "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		if t.Status != "PROPOSED" {
			return nil, errors.New("only PROPOSED tasks can become READY")
		}
		fp, e := s.ReadyCheck(t, false)
		if e != nil {
			return nil, e
		}
		t.SourceVersions = fp
		t.Status = "READY"
		if _, e = s.Package(t, "developer", 0); e != nil {
			return nil, e
		}
		return t, s.SaveTask(t, "TaskReady", *actor, nil)
	case "claim":
		actor := f.String("actor", "", "")
		lease := f.Int64("lease-seconds", 3600, "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		if e = need(*actor, "actor"); e != nil {
			return nil, e
		}
		if *lease < 1 {
			return nil, errors.New("lease duration must be positive")
		}
		if t.Status != "READY" {
			return nil, errors.New("only READY tasks can be claimed")
		}
		if _, e = s.ReadyCheck(t, true); e != nil {
			return nil, e
		}
		if t.Attempts >= s.Config.MaxAttempts {
			return nil, errors.New("attempt budget exhausted; explicit policy decision required")
		}
		baseline := Head(s.Worktree)
		if baseline == "" {
			return nil, errors.New("create an initial Git commit before execution")
		}
		t.Attempts++
		t.Status = "IMPLEMENTING"
		t.Run = &Run{ID: newID("RUN-"), Developer: *actor, BaseSHA: baseline, LeaseUntil: time.Now().Unix() + *lease, StartedAt: timestamp(), Checks: map[string]Evidence{}}
		return t, s.SaveTask(t, "TaskClaimed", *actor, nil)
	case "heartbeat":
		runID := f.String("run", "", "")
		lease := f.Int64("lease-seconds", 3600, "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		if t.Status != "IMPLEMENTING" {
			return nil, errors.New("heartbeat only applies during implementation")
		}
		run, e := s.ActiveRun(&t, *runID, true)
		if e != nil {
			return nil, e
		}
		if *lease < 1 {
			return nil, errors.New("lease must be positive")
		}
		run.LeaseUntil = time.Now().Unix() + *lease
		return t, s.SaveTask(t, "RunHeartbeat", run.Developer, nil)
	case "submit":
		runID := f.String("run", "", "")
		sha := f.String("head", "", "")
		pr := f.String("pr", "", "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		if t.Status != "IMPLEMENTING" {
			return nil, errors.New("task is not implementing")
		}
		run, e := s.ActiveRun(&t, *runID, true)
		if e != nil {
			return nil, e
		}
		if _, e = s.ReadyCheck(t, true); e != nil {
			return nil, e
		}
		resolved, e := ResolveCommit(s.Worktree, *sha)
		if e != nil {
			return nil, e
		}
		if e = CleanCheckout(s.Worktree, resolved); e != nil {
			return nil, e
		}
		tree, e := ProductTree(s.Worktree, resolved)
		if e != nil {
			return nil, e
		}
		run.HeadSHA = resolved
		run.PR = *pr
		run.ProductTree = tree
		binding, e := s.Binding(t)
		if e != nil {
			return nil, e
		}
		run.Binding = binding
		t.Status = "VERIFYING"
		return t, s.SaveTask(t, "ImplementationSubmitted", run.Developer, nil)
	case "block":
		actor := f.String("actor", "controller", "")
		reason := f.String("reason", "", "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		if e = need(*reason, "reason"); e != nil {
			return nil, e
		}
		if t.Status == "DONE" {
			return nil, errors.New("create a regression task instead of rewriting completed history")
		}
		t.Status = "BLOCKED"
		t.Blocker = *reason
		return t, s.SaveTask(t, "TaskBlocked", *actor, map[string]string{"reason": *reason})
	case "retry":
		actor := f.String("actor", "controller", "")
		reason := f.String("reason", "", "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		if e = need(*reason, "reason"); e != nil {
			return nil, e
		}
		if t.Status == "DONE" {
			return nil, errors.New("completed tasks need a new regression task")
		}
		if t.Status == "IMPLEMENTING" && t.Run != nil && t.Run.LeaseUntil > time.Now().Unix() {
			return nil, errors.New("active lease; explicitly block before replacing this run")
		}
		if t.Run != nil {
			t.History = append(t.History, *t.Run)
		}
		t.Run = nil
		t.Status = "PROPOSED"
		t.SourceVersions = map[string]SourceVersion{}
		t.Blocker = ""
		return t, s.SaveTask(t, "TaskRetried", *actor, map[string]string{"reason": *reason})
	}
	return nil, errors.New("unknown task command")
}
func (s *Store) decisionCommand(args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("decision create|resolve required")
	}
	command := args[0]
	f := flags(command)
	if command == "create" {
		file := f.String("file", "", "")
		actor := f.String("actor", "planner", "")
		if e := parse(f, args[1:]); e != nil {
			return nil, e
		}
		var d Decision
		if e := readJSON(*file, &d); e != nil {
			return nil, e
		}
		if e := validID(d.ID); e != nil {
			return nil, e
		}
		if _, ok := s.State.Decisions[d.ID]; ok {
			return nil, errors.New("decision already exists")
		}
		if strings.TrimSpace(d.Question) == "" || len(d.Options) == 0 {
			return nil, errors.New("decision needs question and options")
		}
		d.Status = "OPEN"
		d.Resolution = ""
		d.ResolvedBy = ""
		return d, s.Emit("DecisionRequested", *actor, "decision", d, nil)
	}
	if command == "resolve" {
		id, rest, e := splitID(args[1:])
		if e != nil {
			return nil, e
		}
		actor := f.String("actor", "", "")
		resolution := f.String("resolution", "", "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		if e = need(*actor, "actor"); e != nil {
			return nil, e
		}
		if e = need(*resolution, "resolution"); e != nil {
			return nil, e
		}
		d, ok := s.State.Decisions[id]
		if !ok || d.Status != "OPEN" {
			return nil, errors.New("decision not OPEN; create a superseding decision for revisions")
		}
		d.Status = "RESOLVED"
		d.Resolution = *resolution
		d.ResolvedBy = *actor
		return d, s.Emit("DecisionResolved", *actor, "decision", d, nil)
	}
	return nil, errors.New("unknown decision command")
}

// Execute invokes one bounded command. It never starts models or a daemon.
func Execute(root string, args []string) (any, error) {
	return ExecuteWorkspace(root, "", args)
}

func ExecuteWorkspace(root, worktree string, args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New(Help)
	}
	s, e := Open(root)
	if e != nil {
		return nil, e
	}
	if worktree != "" {
		if e = s.SetWorktree(worktree); e != nil {
			return nil, e
		}
	}
	if args[0] == "verify" {
		return s.verifyCommand(args[1:])
	}
	var result any
	e = s.Locked(func() error { var err error; result, err = s.dispatch(args); return err })
	return result, e
}
func (s *Store) dispatch(args []string) (any, error) {
	command := args[0]
	rest := args[1:]
	switch command {
	case "source":
		return s.sourceCommand(rest)
	case "task":
		return s.taskCommand(rest)
	case "decision":
		return s.decisionCommand(rest)
	case "review":
		return s.reviewCommand(rest)
	case "accept":
		return s.acceptCommand(rest)
	case "close":
		return s.closeCommand(rest)
	case "context":
		id, rest, e := splitID(rest)
		if e != nil {
			return nil, e
		}
		f := flags(command)
		role := f.String("role", "developer", "")
		budget := f.Int("max-chars", 0, "")
		if e = parse(f, rest); e != nil {
			return nil, e
		}
		t, e := s.Task(id)
		if e != nil {
			return nil, e
		}
		return s.Package(t, *role, *budget)
	case "github":
		if len(rest) < 2 {
			return nil, errors.New("github plan|sync TASK-ID required")
		}
		sub := rest[0]
		id, flagsArgs, e := splitID(rest[1:])
		if e != nil {
			return nil, e
		}
		f := flags("github")
		apply := f.Bool("apply", false, "")
		if e = parse(f, flagsArgs); e != nil {
			return nil, e
		}
		t, e := s.Task(id)
		if e != nil {
			return nil, e
		}
		task := map[string]any{}
		raw := mustJSON(t)
		if e = json.Unmarshal(raw, &task); e != nil {
			return nil, e
		}
		if sub == "plan" {
			if *apply {
				return nil, errors.New("--apply only applies to github sync")
			}
			return gh.PlanIssue(s.Config.GitHub.Repository, task)
		}
		if sub == "sync" {
			result, e := gh.SyncIssue(s.Config.GitHub.Repository, task, *apply, s.Config.Autonomy.RemoteWrites)
			if e != nil {
				return nil, e
			}
			if *apply {
				e = s.Emit("GitHubProjectionSynced", "controller", "", nil, map[string]any{"task_id": id, "result": result})
			}
			return result, e
		}
		return nil, errors.New("unknown github command")
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("unexpected arguments for %s", command)
	}
	switch command {
	case "status":
		return s.Status(), nil
	case "next":
		return s.Next(), nil
	case "rebuild":
		p, e := SafePath(s.Root, filepath.Join(Directory, "state.json"))
		if e != nil {
			return nil, e
		}
		e = WriteJSON(p, s.State)
		return map[string]any{"events": s.Sequence, "projection": p}, e
	case "audit":
		return s.Audit(), nil
	case "doctor":
		r := s.Audit()
		r["version"] = Version
		r["ledger_valid"] = true
		r["events"] = s.Sequence
		r["git_head"] = Head(s.Worktree)
		r["github_repository"] = s.Config.GitHub.Repository
		r["runtime"] = "cooperative-local-single-writer"
		r["strong_identity_enforcement"] = false
		return r, nil
	}
	return nil, fmt.Errorf("unknown command %s\n%s", command, Help)
}

const Help = `projectctl [--root PATH] [--worktree PATH] COMMAND

install --project PATH [--agent codex|claude|generic] [--dry-run] [--upgrade]
uninstall --project PATH [--dry-run] [--purge --backup FILE.zip]
status | doctor | rebuild | next | audit
source add --id ID --kind requirement|constraint|adr|design --path FILE --title TEXT [--scope MODULE] [--approve --actor NAME]
source approve ID --actor NAME --reason TEXT
task create --file TASK.json [--actor NAME]
task revise --file TASK.json --reason TEXT [--actor NAME]
task show ID | task ready ID
task claim ID --actor NAME [--lease-seconds 3600]
task heartbeat ID --run RUN-ID [--lease-seconds 3600]
task submit ID --run RUN-ID --head SHA [--pr URL]
task block ID --reason TEXT | task retry ID --reason TEXT
context ID [--role developer|reviewer|qa|auditor] [--max-chars 60000]
verify ID --run RUN-ID --check CHECK-ID --actor NAME --execute [--timeout 300]
review ID --file REVIEW.json
accept ID --actor NAME
close ID --merged-sha SHA --actor NAME [--pr NUMBER]
decision create --file DECISION.json
decision resolve ID --actor NAME --resolution TEXT
github plan ID | github sync ID [--apply]

IDs use uppercase letters, digits, hyphens and underscores.
Outputs are JSON. Actor names are cooperative labels, not authenticated identities.
verify executes explicit task argv locally; review it before using --execute.
`

// Small wrappers keep map conversion at the GitHub boundary.
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
