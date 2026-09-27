package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

func ValidateContract(c Contract) error {
	if e := validID(c.ID); e != nil {
		return e
	}
	if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Goal) == "" {
		return errors.New("title and goal are required")
	}
	if len(c.Requirements) == 0 || len(c.Acceptance) == 0 || len(c.Verification) == 0 {
		return errors.New("requirements, acceptance and verification must be nonempty")
	}
	for _, list := range [][]string{c.Requirements, c.Constraints, c.Decisions, c.Sources, c.Dependencies} {
		seen := map[string]bool{}
		for _, v := range list {
			if e := validID(v); e != nil {
				return e
			}
			if seen[v] {
				return fmt.Errorf("duplicate ID %s", v)
			}
			seen[v] = true
		}
	}
	if contains(c.Dependencies, c.ID) {
		return errors.New("task cannot depend on itself")
	}
	for _, p := range c.Files {
		if !filepath.IsLocal(filepath.FromSlash(p)) {
			return errors.New("files must stay inside project")
		}
	}
	seen := map[string]bool{}
	for _, a := range c.Acceptance {
		if e := validID(a.ID); e != nil {
			return e
		}
		if strings.TrimSpace(a.Description) == "" || seen[a.ID] {
			return errors.New("acceptance descriptions required and IDs must be unique")
		}
		seen[a.ID] = true
	}
	seen = map[string]bool{}
	for _, v := range c.Verification {
		if e := validID(v.ID); e != nil {
			return e
		}
		if seen[v.ID] || len(v.Argv) == 0 || strings.TrimSpace(v.Argv[0]) == "" {
			return errors.New("verification needs unique IDs and nonempty argv")
		}
		seen[v.ID] = true
		for _, a := range v.Argv {
			if strings.ContainsRune(a, 0) {
				return errors.New("NUL in verification argv")
			}
		}
	}
	return nil
}
func CheckCycles(tasks map[string]Task) error {
	visited, active := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if active[id] {
			return fmt.Errorf("dependency cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		t, ok := tasks[id]
		if !ok {
			return nil
		}
		active[id] = true
		for _, dep := range t.Dependencies {
			if e := visit(dep); e != nil {
				return e
			}
		}
		delete(active, id)
		visited[id] = true
		return nil
	}
	for id := range tasks {
		if e := visit(id); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) Package(t Task, role string, maxChars int) (map[string]any, error) {
	if !contains([]string{"developer", "reviewer", "qa", "auditor"}, role) {
		return nil, errors.New("invalid role")
	}
	if _, e := s.ReadyCheck(t, true); e != nil {
		return nil, e
	}
	sources, e := s.Sources(t)
	if e != nil {
		return nil, e
	}
	code := Head(s.Worktree)
	runID := ""
	candidateSubmitted := t.Run != nil && t.Run.HeadSHA != ""
	if t.Run != nil {
		runID = t.Run.ID
		code = t.Run.BaseSHA
		if t.Run.HeadSHA != "" {
			code = t.Run.HeadSHA
		}
	}
	decisions := []Decision{}
	for _, id := range t.Decisions {
		decisions = append(decisions, s.State.Decisions[id])
	}
	dependencies := []map[string]string{}
	for _, id := range t.Dependencies {
		dependencies = append(dependencies, map[string]string{"id": id, "merged_sha": s.State.Tasks[id].MergedSHA})
	}
	coverage := []map[string]any{}
	for _, a := range t.Acceptance {
		coverage = append(coverage, map[string]any{"acceptance_id": a.ID, "description": a.Description, "source_ids": t.Requirements, "status": "requires_independent_verification"})
	}
	limitations := []string{"Source mapping is not proof of semantic completeness.", "Files are navigation, not a complete impact boundary.", "Reviewer input excludes developer self-evaluation; inspect original sources and actual code independently."}
	p := map[string]any{"schema_version": 1, "builder_version": Version, "role": role, "task": t.Contract, "source_versions": t.SourceVersions,
		"code_sha": code, "run_id": runID, "candidate_submitted": candidateSubmitted, "policy_digest": s.PolicyDigest, "sources": sources, "decisions": decisions, "dependencies": dependencies, "coverage": coverage, "limitations": limitations}
	raw, _ := json.Marshal(p)
	size := utf8.RuneCount(raw)
	if maxChars == 0 {
		maxChars = s.Config.ContextMaxChars
	}
	if maxChars < 1 || size > maxChars {
		return nil, fmt.Errorf("mandatory context is %d characters; budget %d: split task/sources instead of truncating", size, maxChars)
	}
	h := Hash(p)
	p["digest"] = h
	rel := filepath.Join(Directory, "packages", t.ID)
	folder, e := SafePath(s.Root, rel)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(folder, 0755); e != nil {
		return nil, e
	}
	file := filepath.Join(folder, role+"-"+h[:16]+".json")
	if _, e = SafePath(s.Root, filepath.Join(rel, filepath.Base(file))); e != nil {
		return nil, e
	}
	if e = WriteJSON(file, p); e != nil {
		return nil, e
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# %s — %s\n\nPackage: `%s`\n\nCode: `%s`\n\n%s\n\n## Acceptance\n", t.ID, role, h, code, t.Goal)
	fmt.Fprintf(&md, "\nRun: `%s`\n\nCandidate submitted: `%t` (false means this code SHA is a starting baseline, not a completed implementation).\n", runID, candidateSubmitted)
	for _, a := range t.Acceptance {
		fmt.Fprintf(&md, "\n- %s: %s", a.ID, a.Description)
	}
	spec, _ := json.MarshalIndent(t.Contract, "", "  ")
	fmt.Fprintf(&md, "\n\n## Contract\n\n```json\n%s\n```\n", spec)
	for _, v := range sources {
		fmt.Fprintf(&md, "\n## %s — %s\n\nRevision %d / sha256:%s\n\n%s\n", v.ID, v.Path, v.Revision, v.ContentHash, v.Content)
	}
	d, _ := json.MarshalIndent(decisions, "", "  ")
	deps, _ := json.MarshalIndent(dependencies, "", "  ")
	fmt.Fprintf(&md, "\n## Resolved decisions\n\n```json\n%s\n```\n\n## Dependencies\n\n```json\n%s\n```\n\n## Boundaries\n", d, deps)
	for _, v := range limitations {
		fmt.Fprintf(&md, "\n- %s", v)
	}
	md.WriteString("\n")
	markdown := strings.TrimSuffix(file, ".json") + ".md"
	if _, e = SafePath(s.Root, filepath.Join(rel, filepath.Base(markdown))); e != nil {
		return nil, e
	}
	if e = os.WriteFile(markdown, []byte(md.String()), 0644); e != nil {
		return nil, e
	}
	return map[string]any{"digest": h, "json": file, "markdown": markdown, "characters": size}, nil
}
func (s *Store) Status() map[string]any {
	tasks := []Task{}
	for _, t := range s.State.Tasks {
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Priority == tasks[j].Priority {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].Priority < tasks[j].Priority
	})
	rows := []map[string]any{}
	for _, t := range tasks {
		problems := []string{}
		if t.Status != "PROPOSED" && t.Status != "BLOCKED" {
			if _, e := s.ReadyCheck(t, true); e != nil {
				problems = append(problems, e.Error())
			}
		}
		if t.Status == "IMPLEMENTING" && t.Run != nil && t.Run.LeaseUntil <= time.Now().Unix() {
			problems = append(problems, "execution lease expired")
		}
		rows = append(rows, map[string]any{"id": t.ID, "title": t.Title, "status": t.Status, "attempts": t.Attempts, "problems": problems})
	}
	decisions := []Decision{}
	for _, d := range s.State.Decisions {
		decisions = append(decisions, d)
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].ID < decisions[j].ID })
	return map[string]any{"project": s.Config.ProjectName, "ledger_events": s.Sequence, "ledger_head": s.LastHash, "tasks": rows, "decisions": decisions, "note": "Task completion is not requirement fulfillment or deployment progress."}
}
func (s *Store) Next() map[string]any {
	candidates := []Task{}
	excluded := []map[string]string{}
	for _, t := range s.State.Tasks {
		if t.Status != "READY" {
			continue
		}
		_, e := s.ReadyCheck(t, true)
		if e == nil && t.Attempts >= s.Config.MaxAttempts {
			e = errors.New("attempt budget exhausted")
		}
		if e != nil {
			excluded = append(excluded, map[string]string{"id": t.ID, "reason": e.Error()})
		} else {
			candidates = append(candidates, t)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority == candidates[j].Priority {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].Priority < candidates[j].Priority
	})
	ids := []string{}
	var next any
	for _, t := range candidates {
		ids = append(ids, t.ID)
	}
	if len(ids) > 0 {
		next = ids[0]
	}
	return map[string]any{"next": next, "ready": ids, "excluded": excluded}
}
func (s *Store) Audit() map[string]any {
	findings := []map[string]string{}
	mapped := map[string]bool{}
	for _, t := range s.State.Tasks {
		for _, id := range t.Requirements {
			mapped[id] = true
		}
	}
	for _, v := range s.State.Sources {
		_, h, e := s.SourceText(v.Path)
		issue := ""
		if e != nil {
			issue = e.Error()
		} else if h != v.ContentHash {
			issue = "source_changed"
		} else if !v.Approved {
			issue = "source_unapproved"
		}
		if issue != "" {
			findings = append(findings, map[string]string{"id": v.ID, "issue": issue})
		}
		if v.Kind == "requirement" && !mapped[v.ID] {
			findings = append(findings, map[string]string{"id": v.ID, "issue": "requirement_has_no_task"})
		}
	}
	for _, t := range s.State.Tasks {
		if t.Status == "PROPOSED" || t.Status == "BLOCKED" {
			continue
		}
		_, e := s.ReadyCheck(t, true)
		if e == nil && contains([]string{"REVIEWED", "ACCEPTED_FOR_MERGE", "DONE"}, t.Status) {
			_, e = s.CheckedEvidence(t)
		}
		if e != nil {
			findings = append(findings, map[string]string{"id": t.ID, "issue": e.Error()})
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i]["id"]+findings[i]["issue"] < findings[j]["id"]+findings[j]["issue"]
	})
	return map[string]any{"findings": findings, "scope": "Structural/source/evidence audit only; independently audit semantic design conformity."}
}
