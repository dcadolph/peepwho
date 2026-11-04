package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dcadolph/peepwho/internal/connector"
	"github.com/dcadolph/peepwho/internal/episode"
	"github.com/dcadolph/peepwho/internal/httputil"
	"github.com/dcadolph/peepwho/internal/slack"
)

// FetchSource builds the source from the registry and reads it, explaining a
// rejected credential in the source's own terms. It returns the source too, so
// a caller can read state only that source keeps, such as git's marks.
func FetchSource(
	ctx context.Context, spec SourceSpec, o SourceOptions,
) ([]connector.Record, []episode.Episode, connector.Source, error) {
	src, err := spec.New(o)
	if err != nil {
		return nil, nil, nil, err
	}
	recs, err := src.Fetch(ctx)
	if err != nil {
		return nil, nil, src, explainFetchError(spec, err)
	}
	var eps []episode.Episode
	if es, ok := src.(connector.EpisodeSource); ok {
		eps = es.Episodes()
	}
	return recs, eps, src, nil
}

// ExplainSourceError turns a low-level fetch failure into an actionable message
// naming the source and the credential to check. A token that expired or lost
// a scope otherwise surfaces as a bare status code next to an internal API
// path, which tells a user nothing about what to do. Other errors pass through.
func ExplainSourceError(source, tokenEnv string, err error) error {
	var se *httputil.StatusError
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf(
			"%w: %s rejected the credentials (HTTP %d). The token in %s is missing, expired, or "+
				"lacks the read access this needs. Recreate it and set %s, then try again",
			ErrAuth, source, se.Code, tokenEnv, tokenEnv)
	case http.StatusNotFound:
		return fmt.Errorf(
			"%w: %s returned not found (HTTP 404). Check the site URL is the root with no path, and "+
				"that the names you scoped to exist", ErrAuth, source)
	}
	return err
}

// explainSlackError turns a Slack auth or scope failure into an actionable
// message. Slack signals these as an ok=false code such as invalid_auth rather
// than an HTTP status, so ExplainSourceError, which reads status codes, never
// catches them; without this a descoped token surfaces as a bare code next to an
// internal method name.
func explainSlackError(err error) error {
	if err == nil || !errors.Is(err, slack.ErrAPI) {
		return err
	}
	code := "error"
	if _, c, ok := strings.Cut(err.Error(), "api error: "); ok {
		code = strings.TrimSpace(c)
	}
	switch {
	case strings.Contains(code, "invalid_auth"), strings.Contains(code, "not_authed"),
		strings.Contains(code, "token_revoked"), strings.Contains(code, "account_inactive"):
		return fmt.Errorf(
			"%w: Slack rejected the token in %s (%s). It is missing, expired, or revoked. Recreate "+
				"the bot token and set %s, then try again", ErrAuth, SlackTokenEnv, code, SlackTokenEnv)
	case strings.Contains(code, "missing_scope"), strings.Contains(code, "not_allowed_token_type"):
		return fmt.Errorf(
			"%w: the Slack token in %s is missing a required scope (%s). Add users:read, "+
				"users:read.email, channels:read, and channels:history in your Slack app and reinstall",
			ErrAuth, SlackTokenEnv, code)
	}
	return err
}

// explainFetchError explains a fetch failure in the vocabulary of the source
// that produced it: a Slack API code, or a rejected credential for a source
// whose registry entry names one. Other errors pass through.
func explainFetchError(spec SourceSpec, err error) error {
	if errors.Is(err, slack.ErrAPI) {
		return explainSlackError(err)
	}
	if spec.AuthEnv == "" {
		return err
	}
	return ExplainSourceError(spec.AuthName, spec.AuthEnv, err)
}
