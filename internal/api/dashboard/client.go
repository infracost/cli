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
	RunParameters(ctx context.Context, repoURL, branchName string) (RunParameters, error)
}

var (
	_ Client = (*client)(nil)
)

type client struct {
	client *http.Client
	config *Config
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
