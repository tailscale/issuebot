// Copyright (c) 2022 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/go-github/v72/github"
)

// debounceInterval is how long after we've checked a PR that we are willing to
// check it again, to avoid duplicate stubbing.
const debounceInterval = 5 * time.Second

var debounceCache = struct {
	sync.Mutex
	m map[string]time.Time // :: string repo#PR → last checked
}{
	m: make(map[string]time.Time),
}

// debounce reports whether checking the given pull request on repo should be
// skipped because we just checked it recently.
func debounce(pr *github.PullRequest, repo *github.Repository) bool {
	debounceCache.Lock()
	defer debounceCache.Unlock()

	// Clean out stale cache entries.
	now := time.Now()
	for old, then := range debounceCache.m {
		if now.Sub(then) > debounceInterval {
			delete(debounceCache.m, old)
		}
	}

	key := fmt.Sprintf("%s#%d", repo.GetFullName(), pr.GetNumber())
	when, ok := debounceCache.m[key]
	if !ok {
		debounceCache.m[key] = now
		return false
	}
	return now.Sub(when) <= debounceInterval
}
