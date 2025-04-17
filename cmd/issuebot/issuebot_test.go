// Copyright (c) 2022 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"regexp"
	"testing"

	"github.com/google/go-github/v69/github"
)

func TestIsAutmationBotAuthor(t *testing.T) {
	// Setup: Install a regular expression for the tests to use.
	botAuthorRE = regexp.MustCompile(`^noreply\+([-\w]+)@example.com$`)
	t.Cleanup(func() { botAuthorRE = nil })

	sptr := func(s string) *string { return &s }
	tests := []struct {
		input *github.CommitAuthor
		match bool
	}{
		// Basic invalid cases.
		{nil, false},
		{&github.CommitAuthor{Name: nil, Email: nil}, false},
		{&github.CommitAuthor{Name: sptr(""), Email: nil}, false},
		{&github.CommitAuthor{Name: nil, Email: sptr("")}, false},
		{&github.CommitAuthor{Name: sptr(""), Email: sptr("")}, false},

		// E-mail is not noreply+suffix@example.com.
		{&github.CommitAuthor{Name: sptr("Foo"), Email: sptr("foo@bar.com")}, false},
		{&github.CommitAuthor{Name: sptr("Foo"), Email: sptr("foo@example.com")}, false},
		{&github.CommitAuthor{Name: sptr("Foo"), Email: sptr("noreply@example")}, false},
		{&github.CommitAuthor{Name: sptr("Foo"), Email: sptr("noreply@example.com")}, false},

		// E-mail suffix does not match the user name.
		{&github.CommitAuthor{Name: sptr("Foo"), Email: sptr("noreply+bar@example.com")}, false},
		{&github.CommitAuthor{Name: sptr("Foo Bar"), Email: sptr("noreply+baz-quux@example.com")}, false},
		{&github.CommitAuthor{Name: sptr("Foo Bar"), Email: sptr("noreply+foo_bar@example.com")}, false},

		// Suffix matches case-insensitively, spaces convert to hyphens.
		{&github.CommitAuthor{Name: sptr("Apple"), Email: sptr("noreply+apple@example.com")}, true},
		{&github.CommitAuthor{Name: sptr("Pear Plum"), Email: sptr("noreply+pear-plum@example.com")}, true},
		{&github.CommitAuthor{Name: sptr("cherry"), Email: sptr("noreply+CHERRY@example.com")}, true},
		{&github.CommitAuthor{Name: sptr("OSS Updater"), Email: sptr("noreply+oss-updater@example.com")}, true},
	}
	for _, tc := range tests {
		got := isAutomationBotAuthor(tc.input)
		if got != tc.match {
			t.Errorf("isAutomationBotAuthor: got %v, want %v", got, tc.match)
			t.Logf("Input: %+v", tc.input)
		}
	}
}

func TestCheckCommitMessage(t *testing.T) {
	tests := []struct {
		commit string
		result pullRequestStatus
	}{
		{"prLinked GitHub updates number\nUpdates #1", prLinked},
		{"prLinked GitHub updates URL\nUpdates https://github.com/tailscale/example/issues/1", prLinked},
		{"prLinked GitHub close number\nClose #1", prLinked},
		{"prLinked GitHub closes number\nCloses #1", prLinked},
		{"prLinked GitHub closed number\nClosed #1", prLinked},
		{"prLinked GitHub fix number\nFix #1", prLinked},
		{"prLinked GitHub fixes number\nFixes #1", prLinked},
		{"prLinked GitHub fixed number\nFixed #1", prLinked},
		{"prLinked GitHub resolve number\nResolve #1", prLinked},
		{"prLinked GitHub resolves number\nResolves #1", prLinked},
		{"prLinked GitHub resolved number\nResolved #1", prLinked},
		{"prLinked GitHub for number\nFor #1", prLinked},

		{"prLinked Linear number\nUpdates XXX-123", prFailed}, // https://github.com/tailscale/corp/issues/21347

		{"Revert 0123456789abcdef", prRevert},
		{"prCleanup\nJust a #cleanup", prCleanup},
		{"prSkipped\nskip-issuebot", prSkipped},
		{"", prFailed},
	}
	for _, tc := range tests {
		p := pullRequest{}
		got := p.checkCommitMessage(tc.commit)
		if got != tc.result {
			t.Errorf("checkCommitMessage: got %v, want %v", got, tc.result)
			t.Logf("commit: %+v", tc.commit)
		}
	}
}
