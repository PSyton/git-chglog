package chglog

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tsuyoshiwada/go-gitcmd"
)

var (
	// constants
	separator = "@@__CHGLOG__@@"
	delimiter = "@@__CHGLOG_DELIMITER__@@"

	// fields
	hashField      = "HASH"
	authorField    = "AUTHOR"
	committerField = "COMMITTER"
	subjectField   = "SUBJECT"
	bodyField      = "BODY"

	// formats
	hashFormat      = hashField + ":%H\t%h"
	authorFormat    = authorField + ":%an\t%ae\t%at"
	committerFormat = committerField + ":%cn\t%ce\t%ct"
	subjectFormat   = subjectField + ":%s"
	bodyFormat      = bodyField + ":%b"

	// log
	logFormat = separator + strings.Join([]string{
		hashFormat,
		authorFormat,
		committerFormat,
		subjectFormat,
		bodyFormat,
	}, delimiter)
)

func joinAndQuoteMeta(list []string, sep string) string {
	arr := make([]string, len(list))
	for i, s := range list {
		arr[i] = regexp.QuoteMeta(s)
	}
	return strings.Join(arr, sep)
}

type commitParser struct {
	logger                 *Logger
	client                 gitcmd.Client
	jiraClient             JiraClient
	config                 *Config
	reHeader               *regexp.Regexp
	reMerge                *regexp.Regexp
	reRevert               *regexp.Regexp
	reRef                  *regexp.Regexp
	reIssue                *regexp.Regexp
	reNotes                *regexp.Regexp
	reMention              *regexp.Regexp
	reSignOff              *regexp.Regexp
	reCoAuthor             *regexp.Regexp
	reJiraIssueDescription *regexp.Regexp
}

func newCommitParser(logger *Logger, client gitcmd.Client, jiraClient JiraClient, config *Config) *commitParser {
	opts := config.Options

	joinedRefActions := joinAndQuoteMeta(opts.RefActions, "|")
	joinedIssuePrefix := joinAndQuoteMeta(opts.IssuePrefix, "|")
	joinedNoteKeywords := joinAndQuoteMeta(opts.NoteKeywords, "|")

	return &commitParser{
		logger:                 logger,
		client:                 client,
		jiraClient:             jiraClient,
		config:                 config,
		reHeader:               regexp.MustCompile(opts.HeaderPattern),
		reMerge:                regexp.MustCompile(opts.MergePattern),
		reRevert:               regexp.MustCompile(opts.RevertPattern),
		reRef:                  regexp.MustCompile("(?i)(" + joinedRefActions + ")\\s?([\\w/\\.\\-]+)?(?:" + joinedIssuePrefix + ")(\\d+)"),
		reIssue:                regexp.MustCompile("(?:" + joinedIssuePrefix + ")(\\d+)"),
		reNotes:                regexp.MustCompile("^(?i)\\s*(" + joinedNoteKeywords + ")[:\\s]+(.*)"),
		reMention:              regexp.MustCompile(`@([\w-]+)`),
		reSignOff:              regexp.MustCompile(`Signed-off-by:\s+([\p{L}\s\-\[\]]+)\s+<([\w+\-\[\].@]+)>`),
		reCoAuthor:             regexp.MustCompile(`Co-authored-by:\s+([\p{L}\s\-\[\]]+)\s+<([\w+\-\[\].@]+)>`),
		reJiraIssueDescription: regexp.MustCompile(opts.JiraIssueDescriptionPattern),
	}
}

func (p *commitParser) Parse(rev string) ([]*Commit, error) {
	paths := p.config.Options.Paths

	args := []string{
		rev,
		"--no-decorate",
		"--pretty=" + logFormat,
	}

	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}

	out, err := p.client.Exec("log", args...)

	if err != nil {
		return nil, err
	}

	processor := p.config.Options.Processor
	lines := strings.Split(out, separator)
	lines = lines[1:]
	commits := make([]*Commit, len(lines))

	for i, line := range lines {
		commit, err := p.parseCommit(line)
		if err != nil {
			return nil, err
		}

		if processor != nil {
			commit = processor.ProcessCommit(commit)
			if commit == nil {
				continue
			}
		}

		commits[i] = commit
	}

	return commits, nil
}

func (p *commitParser) parseCommit(input string) (*Commit, error) {
	commit := &Commit{}
	tokens := strings.Split(input, delimiter)

	for _, token := range tokens {
		firstSep := strings.Index(token, ":")
		field := token[0:firstSep]
		value := strings.TrimSpace(token[firstSep+1:])

		switch field {
		case hashField:
			commit.Hash = p.parseHash(value)
		case authorField:
			commit.Author = p.parseAuthor(value)
		case committerField:
			commit.Committer = p.parseCommitter(value)
		case subjectField:
			p.processHeader(commit, value)
		case bodyField:
			p.processBody(commit, value)
		}
	}

	commit.Refs = p.uniqRefs(commit.Refs)
	commit.Mentions = p.uniqMentions(commit.Mentions)

	args := []string{
		"--no-commit-id",
		"--name-only",
		"-r",
		commit.Hash.Short,
	}
	out, err := p.client.Exec("diff-tree", args...)
	if err != nil {
		return nil, err
	}
	if len(out) > 0 {
		commit.ChangedFiles = strings.Split(out, "\n")
	}

	return commit, nil
}

func (p *commitParser) parseHash(input string) *Hash {
	arr := strings.Split(input, "\t")

	return &Hash{
		Long:  arr[0],
		Short: arr[1],
	}
}

func (p *commitParser) parseAuthor(input string) *Author {
	arr := strings.Split(input, "\t")
	ts, err := strconv.Atoi(arr[2])
	if err != nil {
		ts = 0
	}

	return &Author{
		Name:  arr[0],
		Email: arr[1],
		Date:  time.Unix(int64(ts), 0),
	}
}

func (p *commitParser) parseCommitter(input string) *Committer {
	author := p.parseAuthor(input)

	return &Committer{
		Name:  author.Name,
		Email: author.Email,
		Date:  author.Date,
	}
}

func (p *commitParser) processHeader(commit *Commit, input string) {
	opts := p.config.Options

	// header (raw)
	commit.Header = input

	var res [][]string

	// Type, Scope, Subject etc ...
	res = p.reHeader.FindAllStringSubmatch(input, -1)
	if len(res) > 0 {
		assignDynamicValues(commit, opts.HeaderPatternMaps, res[0][1:])
	}

	// Merge
	res = p.reMerge.FindAllStringSubmatch(input, -1)
	if len(res) > 0 {
		merge := &Merge{}
		assignDynamicValues(merge, opts.MergePatternMaps, res[0][1:])
		commit.Merge = merge
	}

	// Revert
	res = p.reRevert.FindAllStringSubmatch(input, -1)
	if len(res) > 0 {
		revert := &Revert{}
		assignDynamicValues(revert, opts.RevertPatternMaps, res[0][1:])
		commit.Revert = revert
	}

	// refs & mentions
	commit.Refs = p.parseRefs(input)
	commit.Mentions = p.parseMentions(input)

	// Jira
	if commit.JiraIssueID != "" {
		p.processJiraIssue(commit, commit.JiraIssueID)
	}
}

func (p *commitParser) extractLineMetadata(commit *Commit, line string) bool {
	meta := false

	refs := p.parseRefs(line)
	if len(refs) > 0 {
		meta = true
		commit.Refs = append(commit.Refs, refs...)
	}

	mentions := p.parseMentions(line)
	if len(mentions) > 0 {
		meta = true
		commit.Mentions = append(commit.Mentions, mentions...)
	}

	coAuthors := p.parseCoAuthors(line)
	if len(coAuthors) > 0 {
		meta = true
		commit.CoAuthors = append(commit.CoAuthors, coAuthors...)
	}

	signers := p.parseSigners(line)
	if len(signers) > 0 {
		meta = true
		commit.Signers = append(commit.Signers, signers...)
	}

	return meta
}

func (p *commitParser) processBody(commit *Commit, input string) {
	input = convNewline(input, "\n")

	// body
	commit.Body = input

	opts := p.config.Options
	if opts.MultilineCommit {
		// additional headers in body
		body := p.reNotes.ReplaceAllString(input, "") // strip notes from body
		inputs := strings.Split(body, "\n")
		for _, i := range inputs {
			subCommit := Commit{}
			// Type, Scope, Subject etc ...
			res := p.reHeader.FindAllStringSubmatch(i, -1)
			if len(res) == 0 {
				continue
			}

			subCommit.Header = i
			subCommit.Hash = commit.Hash
			subCommit.Author = commit.Author
			subCommit.Committer = commit.Committer
			subCommit.ChangedFiles = commit.ChangedFiles
			assignDynamicValues(&subCommit, opts.HeaderPatternMaps, res[0][1:])
			// refs & mentions
			if refs := p.parseRefs(i); len(refs) > 0 {
				subCommit.Refs = refs
			}
			if mentions := p.parseMentions(i); len(mentions) > 0 {
				subCommit.Mentions = p.parseMentions(i)
			}

			// Jira
			if subCommit.JiraIssueID != "" {
				p.processJiraIssue(&subCommit, subCommit.JiraIssueID)
			}
			commit.SubCommits = append(commit.SubCommits, &subCommit)
		}
	}
	// notes & refs & mentions
	commit.Notes = []*Note{}
	inNote := false
	trim := false
	fenceDetector := newMdFenceDetector()
	lines := strings.Split(input, "\n")

	// body without notes & refs & mentions
	trimmedBody := make([]string, 0, len(lines))

	for _, line := range lines {
		if !inNote {
			trim = false
		}
		fenceDetector.Update(line)

		if !fenceDetector.InCodeblock() && p.extractLineMetadata(commit, line) {
			trim = true
			inNote = false
		}
		// Q: should this check also only be outside of code blocks?
		res := p.reNotes.FindAllStringSubmatch(line, -1)

		if len(res) > 0 {
			inNote = true
			trim = true
			for _, r := range res {
				commit.Notes = append(commit.Notes, &Note{
					Title: r[1],
					Body:  r[2],
				})
			}
		} else if inNote {
			last := commit.Notes[len(commit.Notes)-1]
			last.Body = last.Body + "\n" + line
		}

		if !trim {
			trimmedBody = append(trimmedBody, line)
		}
	}

	commit.TrimmedBody = strings.TrimSpace(strings.Join(trimmedBody, "\n"))
	p.trimSpaceInNotes(commit)
}

func (*commitParser) trimSpaceInNotes(commit *Commit) {
	for _, note := range commit.Notes {
		note.Body = strings.TrimSpace(note.Body)
	}
}

func (p *commitParser) parseRefs(input string) []*Ref {
	refs := []*Ref{}

	// references
	res := p.reRef.FindAllStringSubmatch(input, -1)

	for _, r := range res {
		refs = append(refs, &Ref{
			Action: r[1],
			Source: r[2],
			Ref:    r[3],
		})
	}

	// issues
	res = p.reIssue.FindAllStringSubmatch(input, -1)
	for _, r := range res {
		duplicate := false
		for _, ref := range refs {
			if ref.Ref == r[1] {
				duplicate = true
			}
		}
		if !duplicate {
			refs = append(refs, &Ref{
				Action: "",
				Source: "",
				Ref:    r[1],
			})
		}
	}

	return refs
}

func (p *commitParser) parseSigners(input string) []Contact {
	res := p.reSignOff.FindAllStringSubmatch(input, -1)
	contacts := make([]Contact, len(res))

	for i, r := range res {
		contacts[i].Name = r[1]
		contacts[i].Email = r[2]
	}

	return contacts
}

func (p *commitParser) parseCoAuthors(input string) []Contact {
	res := p.reCoAuthor.FindAllStringSubmatch(input, -1)
	contacts := make([]Contact, len(res))

	for i, r := range res {
		contacts[i].Name = r[1]
		contacts[i].Email = r[2]
	}

	return contacts
}

func (p *commitParser) parseMentions(input string) []string {
	res := p.reMention.FindAllStringSubmatch(input, -1)
	mentions := make([]string, len(res))

	for i, r := range res {
		mentions[i] = r[1]
	}

	return mentions
}

func (p *commitParser) uniqRefs(refs []*Ref) []*Ref {
	arr := []*Ref{}

	for _, ref := range refs {
		exist := false
		for _, r := range arr {
			if ref.Ref == r.Ref && ref.Action == r.Action && ref.Source == r.Source {
				exist = true
			}
		}
		if !exist {
			arr = append(arr, ref)
		}
	}

	return arr
}

func (p *commitParser) uniqMentions(mentions []string) []string {
	arr := []string{}

	for _, mention := range mentions {
		exist := false
		for _, m := range arr {
			if mention == m {
				exist = true
			}
		}
		if !exist {
			arr = append(arr, mention)
		}
	}

	return arr
}

func (p *commitParser) processJiraIssue(commit *Commit, issueID string) {
	issue, err := p.jiraClient.GetJiraIssue(commit.JiraIssueID)
	if err != nil {
		p.logger.Error(fmt.Sprintf("Failed to parse Jira story %s: %s\n", issueID, err))
		return
	}
	commit.Type = p.config.Options.JiraTypeMaps[issue.Fields.Type.Name]
	commit.JiraIssue = &JiraIssue{
		Type:        issue.Fields.Type.Name,
		Summary:     issue.Fields.Summary,
		Description: issue.Fields.Description,
		Labels:      issue.Fields.Labels,
	}

	if p.config.Options.JiraIssueDescriptionPattern != "" {
		res := p.reJiraIssueDescription.FindStringSubmatch(commit.JiraIssue.Description)
		if len(res) > 1 {
			commit.JiraIssue.Description = res[1]
		}
	}
}

var (
	fenceTypes = []string{
		"```",
		"~~~",
		"    ",
		"\t",
	}
)

type mdFenceDetector struct {
	fence int
}

func newMdFenceDetector() *mdFenceDetector {
	return &mdFenceDetector{
		fence: -1,
	}
}

func (d *mdFenceDetector) InCodeblock() bool {
	return d.fence > -1
}

func (d *mdFenceDetector) Update(input string) {
	for i, s := range fenceTypes {
		if d.fence < 0 {
			if strings.Index(input, s) == 0 {
				d.fence = i
				break
			}
		} else {
			if strings.Index(input, s) == 0 && i == d.fence {
				d.fence = -1
				break
			}
		}
	}
}
