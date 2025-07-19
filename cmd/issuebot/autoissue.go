// Copyright (c) 2022 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/google/go-github/v72/github"
)

// issuebotStubLabel is a label attached to issues created by the bot so that
// it can more easily find them when checking whether a stub has previously
// been filed for a PR.
const issuebotStubLabel = "issuebot-stub"

// issueTitleTemplate is the string template for stub issue titles, containing
// a %d for the associated PR number.
const issueTitleTemplate = "Placeholder issue for PR #%d"

// issueCommentTemplate is the string template for the PR thread comment,
// containing a %d for the associated stub issue number.
const issueCommentTemplate = ":robot: IssueBot here. I noticed none of the commits on this PR has an issue attached. I have filed issue #%d for you. Please update it at your convenience."

// issueCommentRE is used to recognize issuebot PR comments.
var issueCommentRE = regexp.MustCompile(`(?i)IssueBot here\..*I have filed issue #(\d+) for you`)

// checkStubIssue checks whether the specified pull request already has a stub
// issue created by the bot. If so, it returns the issue number > 0; otherwise
// it returns 0.
func (p pullRequest) checkStubIssue(ctx context.Context, cli *github.Client) (int, error) {
	owner := p.repo.GetOwner().GetLogin()
	repoName := p.repo.GetName()
	prNumber := p.pr.GetNumber()

	issues, _, err := cli.Issues.ListByRepo(ctx, owner, repoName, &github.IssueListByRepoOptions{
		Assignee: p.pr.GetUser().GetLogin(),
		Labels:   []string{issuebotStubLabel},
		State:    "open",
	})
	if err != nil {
		return 0, fmt.Errorf("list issues: %w", err)
	}

	wantTitle := fmt.Sprintf(issueTitleTemplate, prNumber)
	for _, issue := range issues {
		if issue.GetTitle() == wantTitle {
			return issue.GetNumber(), nil
		}
	}

	// If a PR gets updated twice within a short span of time, a stub issue may
	// not show up in search results by the time we get the second ping.  To
	// reduce the likelihood that we create duplicate issues, check for the PR
	// comment too before reporting a missing issue.
	comments, _, err := cli.Issues.ListComments(ctx, owner, repoName, prNumber, nil)
	if err != nil {
		return 0, fmt.Errorf("list comments: %w", err)
	}
	for _, comment := range comments {
		m := issueCommentRE.FindStringSubmatch(comment.GetBody())
		if m != nil {
			num, _ := strconv.Atoi(m[1])
			return num, nil
		}
	}

	return 0, nil
}

// createStubIssue creates a new "placeholder" issue for the specified PR in
// repo, and assigns that issue to the author. It then adds a comment to the PR
// mentioning that issue.
//
// If an issue is successfully created, its number > 0 is returned whether or
// not there is a subsequent error in commenting on the PR.
func (p pullRequest) createStubIssue(ctx context.Context, cli *github.Client) (int, error) {
	owner := p.repo.GetOwner().GetLogin()
	repoName := p.repo.GetName()
	prNumber := p.pr.GetNumber()

	// Create a stub issue to link to the PR.
	prAuthor := p.pr.GetUser().GetLogin()
	labels := []string{issuebotStubLabel}
	issue, _, err := cli.Issues.Create(ctx, owner, repoName, &github.IssueRequest{
		Title:    github.Ptr(fmt.Sprintf(issueTitleTemplate, prNumber)),
		Assignee: github.Ptr(prAuthor),
		Body: github.Ptr(fmt.Sprintf("TODO(@%s): Add details about PR #%d",
			prAuthor, prNumber)),
		Labels: &labels,
	})
	if err != nil {
		return 0, fmt.Errorf("creating issue: %w", err)
	}
	issueNumber := issue.GetNumber()

	// Add a comment to the PR thread indicating what we did.
	if _, _, err := cli.Issues.CreateComment(ctx, owner, repoName, prNumber, &github.IssueComment{
		Body: github.Ptr(fmt.Sprintf(issueCommentTemplate, issueNumber)),
	}); err != nil {
		p.logf("error adding comment (continuing): %v", err)
	}
	return issueNumber, nil
}
