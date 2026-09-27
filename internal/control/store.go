// Package control implements a local, cooperative, append-only project ledger.
package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const Version = "1.0.0"
const Directory = ".project-control"

var idPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,99}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

type Config struct {
	SchemaVersion   int    `json:"schema_version"`
	ProjectName     string `json:"project_name"`
	MaxAttempts     int    `json:"max_attempts"`
	ContextMaxChars int    `json:"context_max_chars"`
	GitHub          struct {
		Repository string `json:"repository"`
	} `json:"github"`
	Autonomy struct {
		RemoteWrites bool `json:"remote_writes"`
	} `json:"autonomy"`
}
type Source struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Path        string   `json:"path"`
	Title       string   `json:"title"`
	Scope       []string `json:"scope"`
	Content     string   `json:"content"`
	ContentHash string   `json:"content_hash"`
	Revision    int      `json:"revision"`
	Approved    bool     `json:"approved"`
	ApprovedBy  string   `json:"approved_by,omitempty"`
}
type Acceptance struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}
type Verification struct {
	ID   string   `json:"id"`
	Argv []string `json:"argv"`
}
type Contract struct {
	ID           string         `json:"id"`
	Revision     int            `json:"revision"`
	Title        string         `json:"title"`
	Goal         string         `json:"goal"`
	Requirements []string       `json:"requirements"`
	Constraints  []string       `json:"constraints"`
	Decisions    []string       `json:"decisions"`
	Sources      []string       `json:"sources"`
	Scope        []string       `json:"scope"`
	Dependencies []string       `json:"dependencies"`
	Acceptance   []Acceptance   `json:"acceptance"`
	Verification []Verification `json:"verification"`
	Priority     int            `json:"priority"`
	NonGoals     []string       `json:"non_goals"`
	Files        []string       `json:"files"`
}
type SourceVersion struct {
	Revision int    `json:"revision"`
	Hash     string `json:"hash"`
}
type Binding struct {
	TaskID         string `json:"task_id"`
	TaskRevision   int    `json:"task_revision"`
	RunID          string `json:"run_id"`
	HeadSHA        string `json:"head_sha"`
	ContractDigest string `json:"contract_digest"`
	SourcesDigest  string `json:"sources_digest"`
	PolicyDigest   string `json:"policy_digest"`
}
type Evidence struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	CheckID     string            `json:"check_id"`
	Actor       string            `json:"actor"`
	Binding     Binding           `json:"binding"`
	Argv        []string          `json:"argv"`
	StartedAt   string            `json:"started_at"`
	FinishedAt  string            `json:"finished_at,omitempty"`
	ReturnCode  int               `json:"returncode"`
	Log         string            `json:"log,omitempty"`
	LogHash     string            `json:"log_hash,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}
type Criterion struct {
	Result   string   `json:"result"`
	Reason   string   `json:"reason"`
	Evidence []string `json:"evidence"`
}
type Review struct {
	Actor         string               `json:"actor"`
	RunID         string               `json:"run_id"`
	HeadSHA       string               `json:"head_sha"`
	PackageDigest string               `json:"package_digest"`
	Criteria      map[string]Criterion `json:"criteria"`
	Decision      string               `json:"decision"`
	Reason        string               `json:"reason"`
	Binding       Binding              `json:"binding"`
	RecordedAt    string               `json:"recorded_at,omitempty"`
}
type Run struct {
	ID          string              `json:"id"`
	Developer   string              `json:"developer"`
	BaseSHA     string              `json:"base_sha"`
	HeadSHA     string              `json:"head_sha,omitempty"`
	LeaseUntil  int64               `json:"lease_until"`
	StartedAt   string              `json:"started_at"`
	PR          string              `json:"pr,omitempty"`
	ProductTree string              `json:"product_tree,omitempty"`
	Binding     Binding             `json:"binding"`
	Checks      map[string]Evidence `json:"checks"`
	Review      *Review             `json:"review,omitempty"`
}
type Task struct {
	Contract
	Status             string                   `json:"status"`
	Attempts           int                      `json:"attempts"`
	Run                *Run                     `json:"run,omitempty"`
	History            []Run                    `json:"history"`
	SourceVersions     map[string]SourceVersion `json:"source_versions"`
	Blocker            string                   `json:"blocker,omitempty"`
	Remediation        map[string]any           `json:"remediation,omitempty"`
	MergedSHA          string                   `json:"merged_sha,omitempty"`
	CompletionBoundary string                   `json:"completion_boundary,omitempty"`
}
type Decision struct {
	ID         string   `json:"id"`
	Question   string   `json:"question"`
	Options    []string `json:"options"`
	Status     string   `json:"status"`
	Resolution string   `json:"resolution,omitempty"`
	ResolvedBy string   `json:"resolved_by,omitempty"`
}
type State struct {
	Sources   map[string]Source   `json:"sources"`
	Tasks     map[string]Task     `json:"tasks"`
	Decisions map[string]Decision `json:"decisions"`
}
type Event struct {
	SchemaVersion int                        `json:"schema_version"`
	Sequence      int                        `json:"sequence"`
	Previous      string                     `json:"previous"`
	Timestamp     string                     `json:"timestamp"`
	Type          string                     `json:"type"`
	Actor         string                     `json:"actor"`
	Updates       map[string]json.RawMessage `json:"updates"`
	Details       any                        `json:"details,omitempty"`
	Hash          string                     `json:"hash,omitempty"`
}
type Store struct {
	Worktree     string
	Root         string
	Base         string
	Config       Config
	PolicyDigest string
	State        State
	Sequence     int
	LastHash     string
}

func Hash(v any) string        { b, _ := json.Marshal(v); return ByteHash(b) }
func ByteHash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func timestamp() string        { return time.Now().UTC().Format(time.RFC3339Nano) }
func newID(prefix string) string {
	b := make([]byte, 12)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + strings.ToUpper(hex.EncodeToString(b))
}
func validID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid ID %q (uppercase letters, digits, hyphens, underscores)", id)
	}
	return nil
}
func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, v); e != nil {
		return fmt.Errorf("%s: %w", path, e)
	}
	return nil
}
func WriteJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	b = append(b, '\n')
	f, e := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}
func SafePath(root, relative string) (string, error) {
	if !filepath.IsLocal(relative) {
		return "", fmt.Errorf("path escapes project: %s", relative)
	}
	current := root
	for _, part := range strings.Split(filepath.Clean(relative), string(os.PathSeparator)) {
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", e
		}
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink not allowed: %s", current)
		}
	}
	return current, nil
}
func Open(root string) (*Store, error) {
	root, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return nil, e
	}
	base, e := SafePath(root, Directory)
	if e != nil {
		return nil, e
	}
	if _, e = os.Stat(base); e != nil {
		return nil, fmt.Errorf("project is not installed; run projectctl install --project PATH: %w", e)
	}
	configPath, e := SafePath(root, filepath.Join(Directory, "config.json"))
	if e != nil {
		return nil, e
	}
	raw, e := os.ReadFile(configPath)
	if e != nil {
		return nil, e
	}
	var config Config
	if e = json.Unmarshal(raw, &config); e != nil {
		return nil, e
	}
	if config.SchemaVersion != 1 || config.MaxAttempts < 1 || config.ContextMaxChars < 1 {
		return nil, errors.New("invalid config schema, attempt limit or context budget")
	}
	s := &Store{Worktree: root, Root: root, Base: base, Config: config, PolicyDigest: ByteHash(raw)}
	for _, dir := range []string{"events", "packages", "evidence"} {
		p, e := SafePath(root, filepath.Join(Directory, dir))
		if e != nil {
			return nil, e
		}
		if e = os.MkdirAll(p, 0755); e != nil {
			return nil, e
		}
	}
	return s, nil
}
func (s *Store) Locked(fn func() error) error {
	p, e := SafePath(s.Root, filepath.Join(Directory, ".runtime.lock"))
	if e != nil {
		return e
	}
	release, e := lockFile(p)
	if e != nil {
		return e
	}
	defer release()
	if e = s.Load(); e != nil {
		return e
	}
	return fn()
}
func (s *Store) Load() error {
	state := State{Sources: map[string]Source{}, Tasks: map[string]Task{}, Decisions: map[string]Decision{}}
	files, e := filepath.Glob(filepath.Join(s.Base, "events", "*.json"))
	if e != nil {
		return e
	}
	sort.Strings(files)
	previous := ""
	seq := 0
	for _, path := range files {
		rel, _ := filepath.Rel(s.Root, path)
		if _, e = SafePath(s.Root, rel); e != nil {
			return e
		}
		var event Event
		if e = readJSON(path, &event); e != nil {
			return e
		}
		claimed := event.Hash
		event.Hash = ""
		if event.SchemaVersion != 1 || event.Sequence != seq+1 || event.Previous != previous || claimed != Hash(event) {
			return fmt.Errorf("ledger hash/sequence mismatch: %s", filepath.Base(path))
		}
		for kind, raw := range event.Updates {
			switch kind {
			case "source":
				var v Source
				if e = json.Unmarshal(raw, &v); e == nil {
					e = validID(v.ID)
					state.Sources[v.ID] = v
				}
			case "task":
				var v Task
				if e = json.Unmarshal(raw, &v); e == nil {
					e = validID(v.ID)
					state.Tasks[v.ID] = v
				}
			case "decision":
				var v Decision
				if e = json.Unmarshal(raw, &v); e == nil {
					e = validID(v.ID)
					state.Decisions[v.ID] = v
				}
			default:
				return fmt.Errorf("unknown ledger entity kind %s", kind)
			}
			if e != nil {
				return e
			}
		}
		seq++
		previous = claimed
	}
	s.State = state
	s.Sequence = seq
	s.LastHash = previous
	return nil
}
func (s *Store) Emit(kind, actor, entity string, value, details any) error {
	if strings.TrimSpace(actor) == "" {
		return errors.New("actor is required")
	}
	updates := map[string]json.RawMessage{}
	if entity != "" {
		raw, e := json.Marshal(value)
		if e != nil {
			return e
		}
		updates[entity] = raw
	}
	event := Event{SchemaVersion: 1, Sequence: s.Sequence + 1, Previous: s.LastHash, Timestamp: timestamp(), Type: kind, Actor: actor, Updates: updates, Details: details}
	event.Hash = Hash(event)
	path := filepath.Join(s.Base, "events", fmt.Sprintf("%08d-%s.json", event.Sequence, newID("")))
	if e := WriteJSON(path, event); e != nil {
		return e
	}
	return s.Load()
}
func (s *Store) Task(id string) (Task, error) {
	if e := validID(id); e != nil {
		return Task{}, e
	}
	t, ok := s.State.Tasks[id]
	if !ok {
		return t, fmt.Errorf("unknown task %s", id)
	}
	b, _ := json.Marshal(t)
	var result Task
	_ = json.Unmarshal(b, &result)
	return result, nil
}
func (s *Store) SaveTask(t Task, kind, actor string, details any) error {
	return s.Emit(kind, actor, "task", t, details)
}
func (s *Store) SourceText(path string) (string, string, error) {
	p, e := SafePath(s.Root, filepath.FromSlash(path))
	if e != nil {
		return "", "", e
	}
	info, e := os.Stat(p)
	if e != nil {
		return "", "", e
	}
	if !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return "", "", errors.New("source must be a regular UTF-8 file <=1 MiB; split large documents")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return "", "", e
	}
	if !utf8.Valid(b) {
		return "", "", errors.New("source must contain valid UTF-8 text")
	}
	if strings.TrimSpace(string(b)) == "" || strings.ContainsRune(string(b), 0) {
		return "", "", errors.New("source is empty or binary")
	}
	return string(b), ByteHash(b), nil
}
func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
func intersects(a, b []string) bool {
	for _, v := range a {
		if contains(b, v) {
			return true
		}
	}
	return false
}
func (s *Store) Sources(t Task) ([]Source, error) {
	keys := map[string]bool{}
	for _, list := range [][]string{t.Requirements, t.Constraints, t.Sources} {
		for _, id := range list {
			keys[id] = true
		}
	}
	for id, v := range s.State.Sources {
		if v.Kind == "constraint" && (len(v.Scope) == 0 || intersects(v.Scope, t.Scope)) {
			keys[id] = true
		}
	}
	ids := []string{}
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := []Source{}
	for _, id := range ids {
		v, ok := s.State.Sources[id]
		if !ok {
			return nil, fmt.Errorf("unknown source %s", id)
		}
		if !v.Approved {
			return nil, fmt.Errorf("unapproved source %s", id)
		}
		_, hash, e := s.SourceText(v.Path)
		if e != nil {
			return nil, e
		}
		if hash != v.ContentHash {
			return nil, fmt.Errorf("source %s changed; reapprove and re-plan", id)
		}
		result = append(result, v)
	}
	for _, id := range t.Requirements {
		if s.State.Sources[id].Kind != "requirement" {
			return nil, fmt.Errorf("%s must reference a requirement", id)
		}
	}
	for _, id := range t.Constraints {
		if s.State.Sources[id].Kind != "constraint" {
			return nil, fmt.Errorf("%s must reference a constraint", id)
		}
	}
	return result, nil
}
func (s *Store) Fingerprints(t Task) (map[string]SourceVersion, error) {
	sources, e := s.Sources(t)
	if e != nil {
		return nil, e
	}
	r := map[string]SourceVersion{}
	for _, v := range sources {
		r[v.ID] = SourceVersion{v.Revision, v.ContentHash}
	}
	return r, nil
}
func (s *Store) ReadyCheck(t Task, frozen bool) (map[string]SourceVersion, error) {
	fingerprints, e := s.Fingerprints(t)
	if e != nil {
		return nil, e
	}
	for _, id := range t.Decisions {
		v, ok := s.State.Decisions[id]
		if !ok || v.Status != "RESOLVED" {
			return nil, fmt.Errorf("unresolved decision %s", id)
		}
	}
	for _, id := range t.Dependencies {
		v, ok := s.State.Tasks[id]
		if !ok || v.Status != "DONE" {
			return nil, fmt.Errorf("dependency %s is not DONE", id)
		}
	}
	if frozen && Hash(fingerprints) != Hash(t.SourceVersions) {
		return nil, errors.New("task context is stale; retry/revise and ready again")
	}
	return fingerprints, nil
}
func (s *Store) Binding(t Task) (Binding, error) {
	fp, e := s.ReadyCheck(t, true)
	if e != nil {
		return Binding{}, e
	}
	if t.Run == nil || t.Run.HeadSHA == "" {
		return Binding{}, errors.New("submit a commit first")
	}
	// Re-read policy: edits during a command/verification invalidate the evidence.
	p, e := SafePath(s.Root, filepath.Join(Directory, "config.json"))
	if e != nil {
		return Binding{}, e
	}
	raw, e := os.ReadFile(p)
	if e != nil {
		return Binding{}, e
	}
	return Binding{t.ID, t.Revision, t.Run.ID, t.Run.HeadSHA, Hash(t.Contract), Hash(fp), ByteHash(raw)}, nil
}
func (s *Store) ActiveRun(t *Task, id string, lease bool) (*Run, error) {
	if t.Run == nil || t.Run.ID != id {
		return nil, errors.New("stale or unknown run ID")
	}
	if lease && t.Run.LeaseUntil <= time.Now().Unix() {
		return nil, errors.New("execution lease expired; retry task")
	}
	return t.Run, nil
}
func Git(root string, args ...string) (string, error) {
	all := append([]string{"-C", root}, args...)
	cmd := exec.Command("git", all...)
	b, e := cmd.CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("git: %s (%w)", strings.TrimSpace(string(b)), e)
	}
	return strings.TrimSpace(string(b)), nil
}
func Head(root string) string { s, _ := Git(root, "rev-parse", "--verify", "HEAD"); return s }
func ResolveCommit(root, sha string) (string, error) {
	if !shaPattern.MatchString(sha) {
		return "", errors.New("use an explicit commit SHA")
	}
	return Git(root, "rev-parse", "--verify", sha+"^{commit}")
}
func CleanCheckout(root, expected string) error {
	if Head(root) != expected {
		return errors.New("checkout HEAD differs from submitted commit")
	}
	for _, args := range [][]string{{"diff", "--name-only", "-z", "HEAD", "--"}, {"ls-files", "--others", "--exclude-standard", "-z"}} {
		raw, e := Git(root, args...)
		if e != nil {
			return e
		}
		for _, p := range strings.Split(raw, "\x00") {
			if p != "" && !strings.HasPrefix(p, Directory+"/") {
				return fmt.Errorf("commit or remove working change outside .project-control: %s", p)
			}
		}
	}
	return nil
}
func ProductTree(root, sha string) (string, error) {
	raw, e := Git(root, "ls-tree", "-rz", sha)
	if e != nil {
		return "", e
	}
	lines := []string{}
	for _, line := range strings.Split(raw, "\x00") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && !strings.HasPrefix(parts[1], Directory+"/") {
			lines = append(lines, line)
		}
	}
	return Hash(lines), nil
}

// SetWorktree separates the single writer's ledger from a worker's code checkout.
// Both locations must belong to the same Git repository; no ledger is copied.
func (s *Store) SetWorktree(value string) error {
	absolute, e := filepath.Abs(value)
	if e != nil {
		return e
	}
	absolute, e = filepath.EvalSymlinks(absolute)
	if e != nil {
		return e
	}
	common := func(root string) (string, error) {
		v, e := Git(root, "rev-parse", "--git-common-dir")
		if e != nil {
			return "", e
		}
		if !filepath.IsAbs(v) {
			v = filepath.Join(root, v)
		}
		return filepath.EvalSymlinks(v)
	}
	a, e := common(s.Root)
	if e != nil {
		return e
	}
	b, e := common(absolute)
	if e != nil {
		return e
	}
	ai, e := os.Stat(a)
	if e != nil {
		return e
	}
	bi, e := os.Stat(b)
	if e != nil {
		return e
	}
	if !os.SameFile(ai, bi) {
		return errors.New("worktree must share the ledger root's Git repository")
	}
	s.Worktree = absolute
	return nil
}
