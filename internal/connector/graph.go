package connector

import (
	"context"
	"strings"

	"github.com/dcadolph/peepwho/internal/graph"
)

// Graph ingests the org chart from Microsoft Graph, turning each directory user
// into a person record carrying the manager as the reporting line.
type Graph struct {
	// client reads users from Microsoft Graph.
	client *graph.Client
}

// NewGraph returns a Graph connector over client.
func NewGraph(client *graph.Client) *Graph {
	if client == nil {
		panic("connector.NewGraph: client required")
	}
	return &Graph{client: client}
}

// Fetch reads every user and returns one person record each, with the manager's
// email as the reporting line. A user with neither an email nor a name is
// skipped, since nothing could key or show it.
func (g *Graph) Fetch(ctx context.Context) ([]Record, error) {
	users, err := g.client.Users(ctx)
	if err != nil {
		return nil, err
	}
	recs := make([]Record, 0, len(users))
	for _, u := range users {
		email := u.Email()
		if email == "" && u.DisplayName == "" {
			continue
		}
		var alt []string
		if u.UserPrincipalName != "" && !strings.EqualFold(u.UserPrincipalName, email) {
			alt = append(alt, u.UserPrincipalName)
		}
		recs = append(recs, Record{
			Kind:     KindPerson,
			PersonID: u.ID,
			Name:     u.DisplayName,
			Email:    email,
			AltIDs:   alt,
			Title:    u.JobTitle,
			Team:     u.Department,
			Manager:  u.ManagerEmail(),
			Source:   "graph",
		})
	}
	return recs, nil
}
