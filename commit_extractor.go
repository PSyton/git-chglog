package chglog

import (
	"sort"
	"strings"
)

type commitExtractor struct {
	opts *Options
}

func newCommitExtractor(opts *Options) *commitExtractor {
	return &commitExtractor{
		opts: opts,
	}
}

func (e *commitExtractor) Extract(commits []*Commit) ([]*CommitGroup, []*Commit, []*Commit, []*NoteGroup) {
	commitGroups := []*CommitGroup{}
	noteGroups := []*NoteGroup{}
	mergeCommits := []*Commit{}
	revertCommits := []*Commit{}

	filteredCommits := commitFilter(commits, e.opts.CommitFilters, e.opts.NoCaseSensitive)

	for _, commit := range commits {
		if commit.Merge != nil {
			mergeCommits = append(mergeCommits, commit)
			continue
		}

		if commit.Revert != nil {
			revertCommits = append(revertCommits, commit)
			continue
		}
	}

	for _, commit := range filteredCommits {
		if commit.Merge == nil && commit.Revert == nil {
			e.processCommitGroups(&commitGroups, commit, e.opts.NoCaseSensitive)
		}

		e.processNoteGroups(&noteGroups, commit)
	}

	e.sortCommitGroups(commitGroups)
	e.sortNoteGroups(noteGroups)

	return commitGroups, mergeCommits, revertCommits, noteGroups
}

func (e *commitExtractor) processCommitGroups(groups *[]*CommitGroup, commit *Commit, noCaseSensitive bool) {
	var group *CommitGroup

	// commit group
	raw, ttl := e.commitGroupTitle(commit)

	for _, g := range *groups {
		rawTitleTmp := g.RawTitle
		if noCaseSensitive {
			rawTitleTmp = strings.ToLower(g.RawTitle)
		}

		rawTmp := raw
		if noCaseSensitive {
			rawTmp = strings.ToLower(raw)
		}
		if rawTitleTmp == rawTmp {
			group = g
		}
	}

	if group != nil {
		group.Commits = append(group.Commits, commit)
	} else if raw != "" {
		*groups = append(*groups, &CommitGroup{
			RawTitle: raw,
			Title:    ttl,
			Commits:  []*Commit{commit},
		})
	}
}

func (e *commitExtractor) processNoteGroups(groups *[]*NoteGroup, commit *Commit) {
	if len(commit.Notes) != 0 {
		for _, note := range commit.Notes {
			e.appendNoteToNoteGroups(groups, note)
		}
	}
}

func (e *commitExtractor) appendNoteToNoteGroups(groups *[]*NoteGroup, note *Note) {
	exist := false

	for _, g := range *groups {
		if g.Title == note.Title {
			exist = true
			g.Notes = append(g.Notes, note)
		}
	}

	if !exist {
		*groups = append(*groups, &NoteGroup{
			Title: note.Title,
			Notes: []*Note{note},
		})
	}
}

func (e *commitExtractor) commitGroupTitle(commit *Commit) (string, string) {
	var (
		raw string
		ttl string
	)

	if title, ok := dotGet(commit, e.opts.CommitGroupBy); ok {
		if v, ok := title.(string); ok {
			raw = v
			if t, ok := e.opts.CommitGroupTitleMaps[v]; ok {
				ttl = t
			} else {
				//nolint:staticcheck
				ttl = strings.Title(raw)
			}
		}
	}

	return raw, ttl
}

func (e *commitExtractor) sortCommitGroups(groups []*CommitGroup) { //nolint:gocyclo
	// NOTE(khos2ow): this function is over our cyclomatic complexity goal.
	// Be wary when adding branches, and look for functionality that could
	// be reasonably moved into an injected dependency.

	order := make(map[string]int)
	if e.opts.CommitGroupSortBy == "Custom" {
		for i, t := range e.opts.CommitGroupTitleOrder {
			order[t] = i
		}
	}

	// groups
	// TODO(khos2ow): move the inline sort function to
	// conceret implementation of sort.Interface in order
	// to reduce cyclomatic complaxity.
	sort.Slice(groups, func(i, j int) bool {
		if e.opts.CommitGroupSortBy == "Custom" {
			return order[groups[i].RawTitle] < order[groups[j].RawTitle]
		}

		var (
			a, b interface{}
			ok   bool
		)

		a, ok = dotGet(groups[i], e.opts.CommitGroupSortBy)
		if !ok {
			return false
		}

		b, ok = dotGet(groups[j], e.opts.CommitGroupSortBy)
		if !ok {
			return false
		}

		res, err := compare(a, "<", b)
		if err != nil {
			return false
		}
		return res
	})

	// commits
	for _, group := range groups {
		group := group // pin group to avoid potential bugs with passing group to lower functions

		// TODO(khos2ow): move the inline sort function to
		// conceret implementation of sort.Interface in order
		// to reduce cyclomatic complaxity.
		sort.SliceStable(group.Commits, func(i, j int) bool {
			var (
				a, b interface{}
				ok   bool
			)

			a, ok = dotGet(group.Commits[i], e.opts.CommitSortBy)
			if !ok {
				return false
			}

			b, ok = dotGet(group.Commits[j], e.opts.CommitSortBy)
			if !ok {
				return false
			}

			res, err := compare(a, "<", b)
			if err != nil {
				return false
			}
			return res
		})
	}
}

func (e *commitExtractor) sortNoteGroups(groups []*NoteGroup) {
	// groups
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Title) < strings.ToLower(groups[j].Title)
	})

	// notes
	for _, group := range groups {
		group := group // pin group to avoid potential bugs with passing group to lower functions
		sort.Slice(group.Notes, func(i, j int) bool {
			return strings.ToLower(group.Notes[i].Title) < strings.ToLower(group.Notes[j].Title)
		})
	}
}
