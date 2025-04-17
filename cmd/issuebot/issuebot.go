// Copyright (c) 2022 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// issuebot listens for webhooks from GitHub for Pull Request notifications. It
// checks that some commit in the pull request mentions an issue, allowing us
// to better track development of the codebase. If no commit links to an issue,
// issuebot marks the PR as failing checks.
//
// There are two special cases allowing the requirement to be skipped:
//
//   - If a commit contains "#cleanup".
//
//   - If the author of a commit is a known automation bot.
//
// An author is considered a bot if:
//
//   - Its name contains the string "[bot]"
//
//   - Its e-mail address matches the --bot-author-regxp, and its first
//     parenthesized subexpression (if any) is a case-insensitive match for its
//     name once spaces are replaced by "-".
//
// If any commit contains "skip-issuebot" (and no issue is mentioned from other
// commits), a stub issue will be created for the PR that you can fill out
// later. This also makes the CI check pass, like with "#cleanup".
package main

import (
	"context"
	"expvar"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v71/github"
	"github.com/tailscale/setec/client/setec"
	"tailscale.com/tsweb"
)

var (
	// Metrics
	pullsChecked   = expvar.NewInt("issuebot_pull_requests_checked")
	webhookWakeups = expvar.NewInt("issuebot_webhook_wakeups")

	// Flags
	enableStubIssues = flag.Bool("enable-stub-issues", true,
		"Create stub issues when 'skip-issuebot' is used and no issue is found.")
	useSecretsService = flag.String("use-secrets-service", "",
		"If set, fetch secrets from this service (https://hostname)")
	botAuthorEmail = flag.String("bot-author-regexp", "",
		"If set, a regexp matching author e-mails to be treated as automation bots (RE2)")

	// Access tokens
	//
	// TODO(creachadair): Remove the environment fallback once we are
	// comfortably deployed against the secrets server.
	appPrivateKey       = setec.StaticSecret(os.Getenv("ISSUEBOT_APP_PRIVATE_KEY"))
	githubWebhookSecret = setec.StaticSecret(os.Getenv("WEBHOOK_SECRET"))
	appId               int64
	appInstall          int64

	client      *github.Client
	botAuthorRE *regexp.Regexp
)

const (
	appPrivateKeyName       = "prod/issuebot/app-private-key"
	githubWebhookSecretName = "prod/issuebot/github-webhook-secret"

	// The description is limited to 140 characters, so be brief.
	missingCommitExplanation = `Any non-trivial git commit must link to a GitHub issue tracking the work. Edit each commit with a tag like "Updates #nn", and update the PR.`
)

// Return an HTTP client suitable to use with the GitHub API, initialized with
// our API keys and certificate.
//
// This bot expects to run as an organization-level GitHub app, as seen in
// https://github.com/organizations/<name>/settings/installations
// This gives it permission to access private repos without using an individual's
// Personal Access Token.
func getGitHubApiClient() *github.Client {
	itr, err := ghinstallation.New(http.DefaultTransport, appId, appInstall, appPrivateKey())
	if err != nil {
		log.Fatal(err)
	}
	return github.NewClient(&http.Client{Transport: itr})
}

// A pullRequest bundles a pull request and its affiliated repository.
type pullRequest struct {
	repo *github.Repository
	pr   *github.PullRequest
}

func (p pullRequest) logf(msg string, args ...any) {
	log.Printf(fmt.Sprintf("PR %s#%d ", p.repo.GetFullName(), p.pr.GetNumber())+msg, args...)
}

func (p pullRequest) checkCommitMessage(message string) pullRequestStatus {
	var verbs = []string{"close", "closes", "closed", "fix", "fixes", "fixed",
		"resolve", "resolves", "resolved", "updates", "for"}

	lines := strings.Split(message, "\n")

	for idx, line := range lines {
		if idx == 0 && strings.HasPrefix(line, "Revert") {
			// If the commit being reverted did not contain an issue link, we
			// don't want to encourage editing the revert message to add one.
			p.logf("accept: found revert commit")
			return prRevert
		}
		lower := strings.ToLower(line)
		for _, verb := range verbs {
			if !strings.HasPrefix(lower, verb) {
				continue
			}
			if strings.Contains(lower, "#") || strings.Contains(lower, "github.com") {
				// This isn't a perfect check, determined miscreants could sneak
				// something through like "Updates github.com to be more fabulous"
				// or "Fixes #nothing-whatsoever", but we'll trust the team to
				// keep such malappropriate impulses under control.
				p.logf("accept: %q", line)
				return prLinked
			}
		}
	}

	if strings.Contains(message, "skip-issuebot") {
		p.logf("accept: manual override (skip-issuebot)")
		return prSkipped
	} else if strings.Contains(message, "#cleanup") {
		p.logf("accept: manual override (#cleanup)")
		return prCleanup
	}

	return prFailed
}

func (p pullRequest) checkCommitMetadata(repoCommit *github.RepositoryCommit) pullRequestStatus {
	// Requiring bots to link to a bug means they'd link all of their commits to
	// the same bug, which wouldn't be useful.
	if commit := repoCommit.GetCommit(); commit != nil && commit.Author != nil {
		name := commit.GetAuthor().GetName()

		// Author: dependabot[bot] <49699333+dependabot[bot]@users.noreply.github.com>
		if strings.Contains(name, "[bot]") {
			p.logf("accept: author %q is a tagged bot", name)
			return prBot
		}
		// Author is an automation bot.
		if isAutomationBotAuthor(commit.GetAuthor()) {
			p.logf("accept: author %q is an automation bot", name)
			return prBot
		}
	}

	return prFailed
}

// isAutomationBotAuthor reports whether u denotes an automation bot.
//
// This applies if the user has a e-mail address that matches the specified bot
// author regexp, and the user's name is a case-insensitive match for the first
// parenthesized submatch (if any) after spaces in the name are replaced by
// hyphens.
//
// If the regexp is "noreply\+(\w+)@example.com", then a matching example is:
//
//	OSS Updater <noreply+oss-updater@example.com>
//
// Non-matching examples:
//
//	Nonsense <noreply@example.com>
//	Bad Horse <noreply+neigh@example.com>
func isAutomationBotAuthor(u *github.CommitAuthor) bool {
	if botAuthorRE == nil {
		return false // no bot author match is defined
	}
	if u == nil || u.Name == nil || u.Email == nil {
		return false // no name or e-mail to compare
	}
	m := botAuthorRE.FindStringSubmatch(*u.Email)
	if m == nil {
		return false
	}
	if len(m) > 0 {
		name := strings.Join(strings.Fields(*u.Name), "-")
		return strings.EqualFold(name, m[1])
	}
	return true
}

func (p pullRequest) annotateCommitStatus(headSHA string, failed bool) {
	now := time.Now()
	status := &github.RepoStatus{
		Context:   github.Ptr("issuebot"),
		UpdatedAt: &github.Timestamp{Time: now},
	}
	if failed {
		status.State = github.Ptr("failure")
		status.Description = github.Ptr(missingCommitExplanation)
	} else {
		status.State = github.Ptr("success")
	}

	ctx := context.Background()
	_, _, err := client.Repositories.CreateStatus(ctx, *p.repo.Owner.Login, *p.repo.Name, headSHA, status)
	if err != nil {
		log.Fatalf("annotateCommitStatus: err=%v", err)
	}

}

// pullRequestStatus indicates the disposition of a PR.
type pullRequestStatus byte

// These disposition values are ordered, with higher values being "better".
const (
	prFailed  pullRequestStatus = iota // failed, post a notice
	prSkipped                          // manually skipped (skip-issuebot)
	prCleanup                          // manually skipped (#cleanup)
	prSmall                            // diff is small
	prRevert                           // found a revert commit
	prBot                              // author is a well-known bot
	prLinked                           // found a linked issue
)

func checkPullRequest(pr *github.PullRequest, repo *github.Repository) {
	p := pullRequest{repo: repo, pr: pr}
	p.logf("begin check")
	if debounce(pr, repo) {
		p.logf("skipping because it was recently checked")
		return
	}

	ctx := context.Background()
	opts := github.ListOptions{PerPage: 100}

	// A PR is initially "failed". Scan as many commits as necessary to find a
	// reason better than prSkipped (skip-issuebot), if there is one.
	status := prFailed
	totalDiff := 0
	for status <= prSkipped {
		repoCommits, resp, err := client.PullRequests.ListCommits(
			ctx, *repo.Owner.Login, *repo.Name, *pr.Number, &opts)
		if err != nil {
			log.Fatalf("checkPullRequest: err=%v", err)
		}

		for _, rc := range repoCommits {
			// You may be wondering why we are looking up a RepositoryCommit when
			// we already have one in hand (rc).
			//
			// We do so because the RepositoryCommit messages we get from the
			// ListCommits API are not complete -- in particular they lack diff
			// stats.  GetCommit returns the same result type, but all the fields
			// are populated.
			commit, _, err := client.Repositories.GetCommit(ctx, *repo.Owner.Login, *repo.Name, *rc.SHA, &opts)
			if err != nil {
				log.Fatalf("GetCommit: sha=%s err=%v", rc.GetSHA(), err)
			}
			totalDiff += commit.GetStats().GetTotal()

			// Check the commit message for tags.
			if disp := p.checkCommitMessage(*commit.Commit.Message); disp > status {
				status = disp
			}
			// Check commit metadata for well-known bots.
			if disp := p.checkCommitMetadata(commit); disp > status {
				status = disp
			}

			if status > prSkipped {
				break
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Very small diffs are typically small cleanup changes and need not be
	// subjected to strict scrutiny (assuming we didn't find a better reason).
	if status <= prSkipped && totalDiff < 5 {
		p.logf("accept: total diff is %d lines", totalDiff)
		status = prSmall
	}

	// If the best-available reason to accept the PR was a commit with a manual
	// skip-issuebot tag, (maybe) create a stub issue and attach it to the PR.
	if status == prSkipped && *enableStubIssues {
		// First check whether we have already created an issue for this PR.
		issue, err := p.checkStubIssue(ctx, client)
		if issue > 0 {
			p.logf("accept: stub issue #%d found", issue)
		} else if issue, err = p.createStubIssue(ctx, client); issue > 0 {
			p.logf("accept: stub issue #%d created", issue)
		}
		if err != nil {
			p.logf("error adding stub issue (accepting anyway): %v", err)
		}
	}

	if status == prFailed {
		p.logf("reject")
		p.annotateCommitStatus(*pr.Head.SHA, true)
	}
}

func handleWebhook(w http.ResponseWriter, r *http.Request) {
	webhookWakeups.Add(1)
	if r.Method != "POST" && r.Method != "PUT" {
		log.Printf("method not allowed: %s\n", r.Method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	payload, err := github.ValidatePayload(r, githubWebhookSecret())
	if err != nil {
		log.Printf("error validating request body: err=%s\n", err)
		http.Error(w, "webhook signature bad", http.StatusUnauthorized)
		return
	}
	defer r.Body.Close()

	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		log.Printf("could not parse webhook: err=%s\n", err)
		http.Error(w, "could not parse payload", http.StatusBadRequest)
		return
	}

	switch e := event.(type) {
	case *github.PullRequestEvent:
		pullsChecked.Add(1)
		checkPullRequest(e.PullRequest, e.Repo)

	default:
		// not something we need to respond to
		log.Printf("ignoring webhook event\n")
		return
	}
}

func main() {
	flag.Parse()
	log.Print("IssueBot is starting")

	appIdString := os.Getenv("ISSUEBOT_APP_ID")
	appInstallString := os.Getenv("ISSUEBOT_APP_INSTALL")

	if appIdString == "" || appInstallString == "" {
		log.Fatal("ISSUEBOT_APP_ID and ISSUEBOT_APP_INSTALL are required environment variables")
	}

	var err error
	appId, err = strconv.ParseInt(appIdString, 10, 64)
	if err != nil {
		log.Fatalf("Cannot parse ISSUEBOT_APP_ID as integer: %v", appIdString)
	}
	appInstall, err = strconv.ParseInt(appInstallString, 10, 64)
	if err != nil {
		log.Fatalf("Cannot parse ISSUEBOT_APP_INSTALL as integer: %v", appInstallString)
	}
	if *botAuthorEmail != "" {
		botAuthorRE = regexp.MustCompile(*botAuthorEmail)
		log.Printf("Enabled bot regexp matching: %q", botAuthorRE)
	}

	// Fetch secrets from the secrets service, if configured.
	if *useSecretsService != "" {
		log.Printf("Fetching secrets from %q", *useSecretsService)
		st, err := setec.NewStore(context.Background(), setec.StoreConfig{
			Client:  setec.Client{Server: *useSecretsService},
			Secrets: []string{appPrivateKeyName, githubWebhookSecretName},
		})
		if err != nil {
			log.Fatalf("Fetching secrets failed: %v", err)
		}
		log.Print("Secret store is ready")
		appPrivateKey = st.Secret(appPrivateKeyName)
		githubWebhookSecret = st.Secret(githubWebhookSecretName)
	} else if len(appPrivateKey()) == 0 {
		log.Fatalf("Missing required %q", appPrivateKeyName)
	} else if len(githubWebhookSecret()) == 0 {
		log.Fatalf("Missing required %q", githubWebhookSecretName)
	}

	// TODO(creachadair): This currently only runs once; plumb in a Watcher and
	// refresh the client when necessary.
	client = getGitHubApiClient()

	mux := http.NewServeMux()
	tsweb.Debugger(mux)
	mux.HandleFunc("/webhook", handleWebhook)
	srv := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}
	log.Fatal(srv.ListenAndServe())
}
