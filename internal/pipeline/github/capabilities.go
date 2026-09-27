package github

import (
	"context"
	"fmt"
)

type CapabilityState string

const (
	Supported   CapabilityState = "supported"
	Unsupported CapabilityState = "unsupported"
	Unknown     CapabilityState = "unknown"
)

type Capability struct {
	State  CapabilityState
	Reason string
}

func (c Capability) IsSupported() bool { return c.State == Supported }

// Names distinguish read access, enabled settings and permissions. None of
// these alone proves safe auto-merge, protected branches or publisher isolation.
type Capabilities struct {
	IssuesRead                 Capability
	CommentsRead               Capability
	MilestonesRead             Capability
	DependenciesRead           Capability
	IssuesEnabled              Capability
	DeleteBranchOnMergeEnabled Capability
	AutoMergeEnabled           Capability
	PushPermission             Capability
	AdminPermission            Capability
	IssueAutoCloseEnabled      Capability
	ProtectedMergeGate         Capability
}

func unknown(reason string) Capability { return Capability{State: Unknown, Reason: reason} }
func observedBool(value *bool, field string) Capability {
	if value == nil {
		return unknown("repository did not expose " + field)
	}
	if *value {
		return Capability{State: Supported, Reason: field + " is explicitly true"}
	}
	return Capability{State: Unsupported, Reason: field + " is explicitly false (disabled or not granted)"}
}
func readCapability(err error) Capability {
	if err != nil {
		return unknown("read could not be established: " + err.Error())
	}
	return Capability{State: Supported, Reason: "complete authenticated read succeeded; no write capability implied"}
}

func (g *Gateway) ProbeCapabilities(ctx context.Context, dependencyIssue int64) Capabilities {
	c := Capabilities{
		IssuesEnabled: unknown("repository metadata unavailable"), DeleteBranchOnMergeEnabled: unknown("repository metadata unavailable"), AutoMergeEnabled: unknown("repository metadata unavailable"), PushPermission: unknown("repository metadata unavailable"), AdminPermission: unknown("repository metadata unavailable"),
		IssueAutoCloseEnabled: unknown("REST repository metadata does not prove the issue auto-close setting"),
		ProtectedMergeGate:    unknown("rules, trusted checks and bypass policy require a separate gate preflight"),
	}
	var repo struct {
		ID                  int64  `json:"id"`
		NodeID              string `json:"node_id"`
		FullName            string `json:"full_name"`
		HasIssues           *bool  `json:"has_issues"`
		DeleteBranchOnMerge *bool  `json:"delete_branch_on_merge"`
		AllowAutoMerge      *bool  `json:"allow_auto_merge"`
		Permissions         *struct {
			Push  *bool `json:"push"`
			Admin *bool `json:"admin"`
		} `json:"permissions"`
	}
	err := g.get(ctx, g.repoPath(), &repo)
	if err == nil {
		err = stable(repo.ID, repo.NodeID)
	}
	if err == nil && repo.FullName != g.project.Owner+"/"+g.project.Repo {
		err = fmt.Errorf("repository identity mismatch")
	}
	if err == nil {
		c.IssuesEnabled = observedBool(repo.HasIssues, "has_issues")
		c.DeleteBranchOnMergeEnabled = observedBool(repo.DeleteBranchOnMerge, "delete_branch_on_merge")
		c.AutoMergeEnabled = observedBool(repo.AllowAutoMerge, "allow_auto_merge")
		if repo.Permissions != nil {
			c.PushPermission = observedBool(repo.Permissions.Push, "permissions.push")
			c.AdminPermission = observedBool(repo.Permissions.Admin, "permissions.admin")
		}
	}
	_, err = g.ListIssues(ctx)
	c.IssuesRead = readCapability(err)
	_, err = g.ListComments(ctx, Resource{Kind: "issue", Number: g.project.ControlIssue})
	c.CommentsRead = readCapability(err)
	_, err = g.ListMilestones(ctx)
	c.MilestonesRead = readCapability(err)
	if dependencyIssue > 0 {
		_, err = g.ListDependencies(ctx, dependencyIssue, "blocked_by")
		if err == nil {
			_, err = g.ListDependencies(ctx, dependencyIssue, "blocking")
		}
		c.DependenciesRead = readCapability(err)
	} else {
		c.DependenciesRead = unknown("no explicit dependency probe issue supplied")
	}
	return c
}
