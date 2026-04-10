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
}

type Role struct {
	ID string `json:"id"`
}

type CurrentUser struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Email         string         `json:"email"`
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
}

type Client interface {
	CurrentUser(ctx context.Context) (CurrentUser, error)
	RunParameters(ctx context.Context, repoURL, branchName string) (RunParameters, error)
	HasRepo(ctx context.Context, orgID, repoName string) (bool, error)
}

var (
	_ Client = (*client)(nil)
)

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
    organizations {
      id
      name
      slug
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
