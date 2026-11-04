package connector

import "errors"

// ErrNoRepoPaths indicates no repository paths were given to the git connector.
var ErrNoRepoPaths = errors.New("git: no repository paths")

// ErrNoHeader indicates the CSV had no header row.
var ErrNoHeader = errors.New("org csv: missing header row")

// ErrNoColumns indicates required columns were absent from the header.
var ErrNoColumns = errors.New("org csv: required columns missing")

// ErrNoCodeOwners indicates no CODEOWNERS file was found.
var ErrNoCodeOwners = errors.New("codeowners: no CODEOWNERS file found")

// ErrNoRepos indicates no repositories were given to the GitHub connector.
var ErrNoRepos = errors.New("github: no repositories (use repos or an org)")
