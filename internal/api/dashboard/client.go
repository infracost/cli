package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/infracost/cli/internal/api/dashboard/graphql"
)

type Organization struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Roles []Role `json:"roles"`
	// AgentsEnabled reports whether Infracost Agents (findings / tasks /
	// actions) is enabled for this org. Driven server-side by the
	// coast-access entitlement; the CLI gates the Agents commands and MCP
	// tools on it, surfacing a waitlist message when it's false.
	AgentsEnabled bool `json:"agentsEnabled"`
}

type Role struct {
	ID string `json:"id"`
}

type CurrentUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	// AgentsEnabled reports whether Infracost Agents is enabled for this user
	// directly (rather than via one of their orgs). Driven server-side by the
	// coast-access entitlement targeted at the user. When true the Agents
	// commands / MCP tools are enabled regardless of the active org's flag.
	AgentsEnabled bool           `json:"agentsEnabled"`
	Organizations []Organization `json:"organizations"`
}

type RunParameters struct {
	OrganizationID string `json:"organizationId"`
	RepositoryName string `json:"repositoryName"`

	UsageDefaults     json.RawMessage   `json:"usageDefaults"`
	ProductionFilters []json.RawMessage `json:"productionFilters"`
	TagPolicies       []json.RawMessage `json:"tagPolicies"`
	FinopsPolicies    []json.RawMessage `json:"finopsPolicies"`
	Guardrails        []json.RawMessage `json:"guardrails"`
	Budgets           []json.RawMessage `json:"budgets"`
	ConfigTemplate    string            `json:"configTemplate"`
}

type Client interface {
	CurrentUser(ctx context.Context) (CurrentUser, error)
	CreateOrganization(ctx context.Context, name string) (Organization, error)
	RunParameters(ctx context.Context, repoURL, branchName string) (RunParameters, error)
	HasRepo(ctx context.Context, orgID, repoName string) (bool, error)
}

var _ Client = (*client)(nil)

type client struct {
	client *http.Client
	config *Config
}

func (c *client) CurrentUser(ctx context.Context) (CurrentUser, error) {
	const query = `{
  currentUser {
    id
    name
    email
    agentsEnabled
    organizations {
      id
      name
      slug
      agentsEnabled
      roles {
        id
      }
    }
  }
}`

	type response struct {
		CurrentUser CurrentUser `json:"currentUser"`
	}

	r, err := graphql.Query[response](ctx, c.client, fmt.Sprintf("%s/graphql", c.config.Endpoint), query, nil)
	if err != nil {
		return CurrentUser{}, err
	}

	if len(r.Errors) > 0 {
		var errs []string
		for _, e := range r.Errors {
			errs = append(errs, e.Message)
		}
		return r.Data.CurrentUser, errors.New(strings.Join(errs, ";"))
	}
	return r.Data.CurrentUser, nil
}

func (c *client) CreateOrganization(ctx context.Context, name string) (Organization, error) {
	const query = `mutation CreateOrganization($organization: CreateOrganizationInput!) {
  createOrganization(organization: $organization) {
    id
    name
    slug
    roles {
      id
    }
  }
}`

	type response struct {
		CreateOrganization Organization `json:"createOrganization"`
	}

	variables := map[string]interface{}{
		"organization": map[string]interface{}{"name": name},
	}

	r, err := graphql.Query[response](ctx, c.client, fmt.Sprintf("%s/graphql", c.config.Endpoint), query, variables)
	if err != nil {
		return Organization{}, err
	}

	if len(r.Errors) > 0 {
		var errs []string
		for _, e := range r.Errors {
			errs = append(errs, e.Message)
		}
		return Organization{}, errors.New(strings.Join(errs, ";"))
	}
	return r.Data.CreateOrganization, nil
}

func (c *client) RunParameters(ctx context.Context, repoURL, branchName string) (RunParameters, error) {
	const query = `query RunParameters($repoUrl: String, $branchName: String) {
  runParameters(repoUrl: $repoUrl, branchName: $branchName) {
    organizationId
    repositoryName
    usageDefaults
    productionFilters
    tagPolicies
    finopsPolicies
    guardrails
    budgets
    configTemplate
  }
}`

	type response struct {
		RunParameters RunParameters `json:"runParameters"`
	}

	variables := map[string]interface{}{}
	if repoURL != "" {
		variables["repoUrl"] = repoURL
	}
	if branchName != "" {
		variables["branchName"] = branchName
	}

	r, err := graphql.Query[response](ctx, c.client, fmt.Sprintf("%s/graphql", c.config.Endpoint), query, variables)
	if err != nil {
		return RunParameters{}, err
	}

	if len(r.Errors) > 0 {
		var errs []string
		for _, e := range r.Errors {
			// The dashboard API returns this message when the authenticated
			// user hasn't been added to any organization yet.
			if strings.Contains(e.Message, "no associated organization") {
				errs = append(errs, e.Message+" (create an organization at https://dashboard.infracost.io or ask a teammate to invite you)")
			} else {
				errs = append(errs, e.Message)
			}
		}
		return r.Data.RunParameters, errors.New(strings.Join(errs, ";"))
	}
	return r.Data.RunParameters, nil
}

func (c *client) HasRepo(ctx context.Context, orgID, repoName string) (bool, error) {
	const query = `query Repos($orgId: String!, $searchFilter: String) {
  repos(organizationId: $orgId, searchFilter: $searchFilter, first: 10) {
    edges {
      node {
        name
      }
    }
  }
}`

	type node struct {
		Name string `json:"name"`
	}
	type edge struct {
		Node node `json:"node"`
	}
	type repos struct {
		Edges []edge `json:"edges"`
	}
	type response struct {
		Repos repos `json:"repos"`
	}

	r, err := graphql.Query[response](ctx, c.client, fmt.Sprintf("%s/graphql", c.config.Endpoint), query, map[string]interface{}{
		"orgId":        orgID,
		"searchFilter": repoName,
	})
	if err != nil {
		return false, err
	}

	if len(r.Errors) > 0 {
		var errs []string
		for _, e := range r.Errors {
			errs = append(errs, e.Message)
		}
		return false, errors.New(strings.Join(errs, ";"))
	}

	for _, edge := range r.Data.Repos.Edges {
		if edge.Node.Name == repoName {
			return true, nil
		}
	}
	return false, nil
}
