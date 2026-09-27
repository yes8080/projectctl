// projectctl is a portable skill installer and cooperative engineering ledger.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	projectctl "github.com/yes8080/projectctl"
	"github.com/yes8080/projectctl/internal/control"
	"github.com/yes8080/projectctl/internal/install"
)

var version = "1.0.0"

type agentsFlag []string

func (a *agentsFlag) String() string     { return strings.Join(*a, ",") }
func (a *agentsFlag) Set(v string) error { *a = append(*a, v); return nil }
func main()                              { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, errout io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, control.Help)
		return 0
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintln(out, version)
		return 0
	}
	global := flag.NewFlagSet("projectctl", flag.ContinueOnError)
	global.SetOutput(errout)
	root := global.String("root", ".", "ledger project root")
	worktree := global.String("worktree", "", "separate code worktree in the same Git repository")
	if e := global.Parse(args); e != nil {
		return 2
	}
	args = global.Args()
	if len(args) == 0 {
		fmt.Fprint(out, control.Help)
		return 0
	}
	var result any
	var err error
	switch args[0] {
	case "install":
		f := flag.NewFlagSet("install", flag.ContinueOnError)
		f.SetOutput(errout)
		project := f.String("project", *root, "target project")
		dry := f.Bool("dry-run", false, "preview only")
		upgrade := f.Bool("upgrade", false, "upgrade unmodified managed files")
		var agents agentsFlag
		f.Var(&agents, "agent", "codex|claude|generic (repeatable)")
		if e := f.Parse(args[1:]); e != nil {
			return 2
		}
		if f.NArg() != 0 {
			err = errors.New("unexpected installation arguments")
			break
		}
		assets, e := fs.Sub(projectctl.SkillAssets, "skills/github-project-control")
		if e != nil {
			err = e
			break
		}
		executable, e := os.Executable()
		if e != nil {
			err = e
			break
		}
		result, err = install.Install(install.Options{Project: *project, Agents: agents, DryRun: *dry, Upgrade: *upgrade, Executable: executable, Assets: assets, Version: version})
	case "uninstall":
		f := flag.NewFlagSet("uninstall", flag.ContinueOnError)
		f.SetOutput(errout)
		project := f.String("project", *root, "target project")
		dry := f.Bool("dry-run", false, "preview only")
		purge := f.Bool("purge", false, "remove memory after backup")
		backup := f.String("backup", "", "external ZIP backup path")
		if e := f.Parse(args[1:]); e != nil {
			return 2
		}
		if f.NArg() != 0 {
			err = errors.New("unexpected uninstall arguments")
			break
		}
		result, err = install.Uninstall(install.UninstallOptions{Project: *project, DryRun: *dry, Purge: *purge, Backup: *backup})
	default:
		result, err = control.ExecuteWorkspace(*root, *worktree, args)
	}
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintln(errout, string(b))
		return 2
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if e := encoder.Encode(result); e != nil {
		fmt.Fprintln(errout, e)
		return 2
	}
	if ev, ok := result.(control.Evidence); ok && ev.Status != "PASS" {
		return 1
	}
	if r, ok := result.(map[string]any); ok && (args[0] == "audit" || args[0] == "doctor") {
		if rows, ok := r["findings"].([]map[string]string); ok && len(rows) > 0 {
			return 1
		}
	}
	return 0
}
