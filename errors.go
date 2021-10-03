package chglog

import "errors"

var (
	errNotFoundTag      = errors.New("could not find the tag")
	errFailedQueryParse = errors.New("failed to parse the query")
	errNoGitTag         = errors.New("git-tag does not exist")
)
