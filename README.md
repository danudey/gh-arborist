# gh-arborist

A [GitHub CLI](https://cli.github.com) extension that reports which branches, pull
requests and issues in a repository could be pruned.

An arborist inspects a tree and says which limbs to cut. This does the same for a
repository, and like a good arborist it does not swing the saw itself: **this
version only ever produces a report.** Nothing is deleted, closed or reassigned.

## Install

```sh
gh extension install danudey/gh-arborist
```

## Use

```sh
gh arborist all                          # every check, in the current repository
gh arborist all -R owner/repo            # somewhere else
gh arborist stale --older-than 2y        # one check at a time
gh arborist all --json | jq .            # machine-readable
gh arborist all --format html -o out.html  # a page you can browse and share
gh arborist all -R owner/repo --git clone  # read the history locally, not over the API
```

Run inside a clone of the repository and the branch, commit and merge-status
questions are answered from it rather than over the API, and answers that cannot
change are cached between runs. See [What it costs](#what-it-costs).

## Checks

| Command | Finds | Suggests |
| --- | --- | --- |
| `gh arborist closed-prs` | Branches whose pull requests have all been merged or closed | `delete` when merged, `review` when closed unmerged |
| `gh arborist merged` | Branches whose commits are already reachable from another branch | `delete` |
| `gh arborist stale` | Branches with no commits for longer than a threshold (default 1 year) | `review` |
| `gh arborist orphans` | Branches, pull requests and issues belonging to people without repository access | `reassign` or `review` |
| `gh arborist all` | Everything above, reported once per item with all reasons | the most decisive of them |

Where several checks agree, the most decisive suggestion wins: a branch that is
both fully merged and owned by somebody who has left is reported as `delete`,
because deleting it settles both.

## Sub-categories

Every section of the report — Branches, Pull requests, Issues — is broken into
sub-sections, one per indicator, so you can look at one kind of problem at a
time. An item that matched several indicators is listed under each of them, while
the section's own count still counts it once.

| Indicator | Sub-section |
| --- | --- |
| `merged-into-base` | Fully merged into another branch |
| `duplicate-branch` | Duplicate of another branch |
| `pull-request-merged` | Pull request merged |
| `pull-request-closed-unmerged` | Pull request closed without merging |
| `older-than-threshold` | Older than the age threshold |
| `owner-without-access` / `owner-account-deleted` | Owner no longer has access / Owner's account was deleted |
| `author-without-access` / `author-account-deleted` | Author no longer has access / Author's account was deleted |
| `assignee-without-access` / `assignee-account-deleted` | Assignee no longer has access / Assignee's account was deleted |

### `closed-prs` — the work has landed or been dropped

Looks up the pull requests opened from each branch. A branch is left alone while
any of its pull requests is still open. Otherwise:

- **merged** — safe to delete. This is the only check that catches squash and
  rebase merges, which rewrite commits and so leave the branch looking unmerged
  to Git.
- **closed without merging** — reported for review, not deletion, because the
  branch may hold work somebody still wants. Use `--merged-only` to see only the
  certain cases.

Pull requests raised from a fork are ignored, even when the fork's branch happens
to share a name with one of yours.

### `merged` — the commits are already somewhere else

Compares each branch against a base and reports the ones with nothing unique
left. Also reports branches pointing at exactly the same commit as another
branch, which costs no extra API requests.

By default the only base is the repository's default branch. Add more:

```sh
gh arborist merged --base 'release/*'   # names or globs, repeatable
gh arborist merged --all-bases          # every branch against every other
```

A branch named by `--base` is excluded from **every** check, exactly as if it had
been passed to `--exclude`: a branch you measure other work against is not a
branch to prune. So `gh arborist all --base 'release/*'` will not report a
release branch as stale or orphaned either. `--all-bases` makes every branch a
base, so that exclusion cannot apply and does not.

`--all-bases` is thorough but needs a comparison for every pair of branches. Run
it from a local clone, or with `--git clone`, and those comparisons are free; over
the API alone, on a repository with hundreds of branches, expect it to take a
long time.

Squash and rebase merges are invisible to this check. `closed-prs` covers those.

### `stale` — nobody has touched it in a long time

Reports branches whose tip commit is older than `--older-than` (default `1y`).

Thresholds are a number and a unit: `y`, `mo`, `w`, `d`, `h`, `m` or `s`. Note
that `m` is minutes and `mo` is months, matching Go's duration syntax. Units
combine, as in `1y6mo`.

Age alone does not make a branch safe to delete, so every result here is
`review`. Run `gh arborist all` to see which stale branches are also merged, and
therefore safe.

### `orphans` — the owner has gone

Reports items belonging to accounts that no longer have access to the repository,
or whose account has been deleted. This is what a departure leaves behind.

Everyone with any access counts as present: direct collaborators, organization
members with access through a team, and outside collaborators. Reading that list
needs **push access** to the repository, so this check fails on repositories you
can only read. Leave it out with `gh arborist all --skip orphans`.

What it looks at:

- **branches** — whoever wrote the tip commit. Reported as `review`, since the
  branch may hold work worth keeping.
- **pull requests** — author and assignees. Reported as `reassign`: nobody can
  push to that branch to finish the work. A pull request whose author has gone
  but which is assigned to somebody who still has access is **not** a candidate,
  because the work already has an owner; the report says how many were left out
  on those grounds. A departed assignee is still reported.
- **issues** — author and assignees. An assignee is `reassign`; an author is
  `review`, because an issue's author cannot be changed.

Only open pull requests and issues are examined; add `--include-closed` for the
rest. App accounts are ignored unless you pass `--include-bots`.

This check is aimed at private repositories. On a public repository, people who
opened a pull request or issue without ever having access are perfectly normal,
so expect a long list to triage; the report says so.

## Flags

Global:

| Flag | Effect |
| --- | --- |
| `-R`, `--repo [HOST/]OWNER/REPO` | Repository to scan (default: the current directory's) |
| `--format table\|markdown\|html\|json` | Output shape (default `table`) |
| `--json` | Shorthand for `--format json` |
| `-o`, `--output FILE` | Write the report to a file instead of standard output |
| `--hyperlinks auto\|always\|never` | Clickable links in terminal output (default `auto`) |
| `--exclude GLOB` | Branch names no check may flag; repeatable (merge bases are added to this automatically) |
| `--include-protected` | Also report branches that protection rules forbid deleting |
| `--exit-code` | Exit 1 when anything is reported, for CI |
| `-v`, `--verbose` | Progress to stderr while scanning, including what came from where |
| `--page-size`, `--batch-size` | Tune API request sizes |
| `--git auto\|clone\|never` | Whether to read a local clone (default `auto`) |
| `--local-repo PATH` | A clone to read branches and merge status from |
| `--no-fetch` | Read the local clone as it is, without fetching first |
| `--no-cache` | Ignore the cache and ask GitHub everything again |

The default branch is never reported, and neither are the branches used as merge
bases, nor branches whose deletion a protection rule forbids unless you ask with
`--include-protected`.

Per check: `--older-than` (stale); `--base`, `--all-bases` (merged);
`--merged-only` (closed-prs); `--include-closed`, `--include-bots`,
`--skip-forks`, `--ignore-user`, `--limit` (orphans); `--skip` (all).

## Output

### Terminal

A table per kind of item, and within each kind a table per indicator, oldest
first, with each reason in a compact form so the rows fit an ordinary terminal:

```
2 BRANCHES
  Fully merged into another branch (1)
BRANCH                     AGE  OWNER          SUGGESTED  WHY
shilpa                     6y   bmckercher123  delete     merged into master; idle over 1y

  Older than the age threshold (2)
BRANCH                     AGE  OWNER          SUGGESTED  WHY
shilpa                     6y   bmckercher123  delete     merged into master; idle over 1y
release-v2.0               8y   matthewdupre   review     idle over 1y
```

A branch that matches several indicators is listed under each of them, which is
the point: the sub-sections answer "what is merged?" and "what is stale?"
separately, while the section count still counts each branch once.

Where the terminal supports OSC 8 hyperlinks, the branch, pull request and issue
names become clickable, as do the owner names, without the URLs taking up any
width. This is detected from the environment: kitty, WezTerm, iTerm2, Ghostty,
Windows Terminal, Konsole, VS Code, Alacritty and recent VTE terminals are
recognised. Terminals that print the escape sequence as text instead of acting
on it (Apple Terminal, and anything inside tmux or screen) are treated as not
supporting it. Override the guess with `--hyperlinks always` or `never`.

Piped into another command you get one row per item as tab-separated values with
no header, no colour, no escape sequences, the reasons written out in full, and a
final column listing the indicators the item matched. Nothing is repeated, so a
script sees each item exactly once:

```sh
# every branch that is safe to delete
gh arborist all | awk -F'\t' '$4 == "delete" { print $1 }'

# name        age            owner  suggested  why   indicators
# shilpa      6 years ago    ...    delete     ...   merged-into-base,older-than-threshold
```

Notes and warnings go to stderr, so they never pollute a pipe.

### HTML and Markdown

`--format html` writes a self-contained page and `--format markdown` writes a
document. Both keep the scannable tables, one per indicator under each kind, and
add what the terminal has no room for: a link to every resource, and an
expandable block per item holding the full reasons and the facts behind them.
The Markdown document writes each item's detail block once, however many
indicators listed it above.

For a branch that means the tip commit's abbreviated ID (linked to the commit),
its subject, when it was committed, the commit author and committer (linked to
their profiles), whether a protection rule blocks deletion, and every pull
request the branch has ever had with its number, state, title and author. For a
pull request or issue it means the title, state, author, each assignee
separately, the head branch, whether it came from a fork, and when it was merged
or closed.

The HTML page needs no network access and loads nothing remote, so it works from
a `file://` URL or as an email attachment. Rows expand with `<details>`, so that
works without script; the script only adds the filter box, the per-action
filters and expand-all. It follows the reader's light or dark theme. Every row
has a stable anchor, so `report.html#branch-feature-x` opens that row; an item
listed under a second indicator gets an anchor qualified by it, as in
`#branch-feature-x-older-than-threshold`.

The Markdown uses `<details>` blocks, which GitHub renders, so the document can
be pasted straight into an issue.

Both formats look up each branch's pull requests even when the check being run
does not need them, since the detail is the point of asking for these formats.
That costs one extra API request per fifty branches.

### JSON

`--json` gives the whole report: every finding, every reason, which check
produced it, which indicator (`category`) it files the item under, each item's
details, and the notes about how to read the results. Each item appears once,
with all of its reasons, so group by `reasons[].category` to rebuild the
sub-sections.

## What it costs

The scan is built to keep API traffic down. Nothing is ever fetched one item at
a time, and each piece of data is fetched once and shared between the checks, so
`gh arborist all` costs little more than the most expensive check on its own.
Branch pull-request lookups are batched by alias, fifty branches to a request.
Duplicate branches are found by comparing commit IDs already in hand.

Beyond that, the two things that make a repeat scan cheap are a local clone and
the cache.

### Git, where Git already knows

Branch names, tip commits, commit dates, subjects, authors and — the expensive
one — whether a branch is already merged into another are all plain Git facts.
Given a local clone, they are read locally and cost no API requests at all:

```sh
gh arborist all                          # already in a clone? it is used
gh arborist all -R owner/repo --git clone  # otherwise, make one under the cache
gh arborist all --local-repo ~/src/repo  # or point at one
gh arborist all --git never              # or don't
```

`--git auto`, the default, uses the current directory's clone when it is a clone
of the repository being scanned, and otherwise leaves everything to the API.
`--git clone` creates a bare `--filter=blob:none` clone under the cache
directory instead, which downloads the commits but none of the file contents.
Either way the clone is fetched before it is read, unless you pass `--no-fetch`;
a clone that cannot be fetched is still read, with a warning. A shallow clone is
refused, because it has no history to compare and every branch would look
unmerged.

Only one remote is ever read: the one whose URL is the repository being scanned.
A clone with the repository as `upstream` and your own fork as `origin` reports
on `upstream`, and the fork's branches — and your local-only branches — are not
reported. Where those branches live is taken from that remote's own fetch
refspec, so a bare repository holding several remotes' refs is read correctly
too. Somebody else's clone is only ever read, never reconfigured: the
partial-clone filter and ref updates are used only on clones arborist made
itself.

An SSH remote written against a host alias is recognised, so a remote of
`git@gh-work:owner/repo` matches `owner/repo` on `github.com` given the usual
stanza:

```
Host gh-work
  HostName github.com
  User git
```

The alias is resolved by asking `ssh -G`, which reads your real configuration
without connecting to anything, and only once a remote's owner and repository
name already match — so a fork on a different aliased host is still not
confused for the repository. A remote whose host is spelled out in full always
wins over one that needs resolving. `https://` and `git://` remotes never
consult the SSH config, because SSH is not what fetches them.

On a 244-branch repository this turns thirteen comparison requests into one
local command, and `--all-bases` — which needs a comparison for *every pair* of
branches, and is the one genuinely expensive option here — from thousands of
requests into one command per base. Two things still come from the API, because
Git cannot know them: which GitHub account each commit's author is, and whether
a protection rule forbids deleting a branch.

One visible difference: Git reports a commit's whole subject line, where the API
truncates it to about seventy characters.

### The cache

Answers that cannot change are kept under `XDG_CACHE_HOME/gh-arborist` and
reused on the next run — which is the point of the example in the name: once a
branch has been found merged into `master`, that pair of commits will never
un-merge.

| Remembered | Keyed by | Expires |
| --- | --- | --- |
| Whether a branch is merged into a base | both commit IDs | never |
| Which account a commit's author is | the commit ID | 7 days |
| A branch's pull requests | branch and tip commit | 30 days, and confirmed |
| Repository metadata, protection rules, collaborators | the repository | 1 hour |

A branch can gain a pull request without its tip commit moving, so a cached
pull-request answer is only used once the list of currently open pull requests
confirms the branch has none — one bulk fetch, which the `orphans` check wants
anyway. The saving has to beat that fetch, so on a repository small enough to
look up in a single request the cache stays out of the way.

Use `--no-cache` to bypass it for one run, `gh arborist cache path` to find it
and `gh arborist cache clear` to throw it away. Clones made by `--git clone`
survive `cache clear`, since a fetch brings one up to date.

Together, on a 164-branch private repository: a first scan takes about ten
seconds, and the next one about one and a half.

## Limitations

- Squash and rebase merges are only detectable through their pull request, so a
  squash-merged branch whose pull request was deleted cannot be identified as
  merged.
- The `orphans` check needs push access to read the collaborator list.
- A commit whose email address is not linked to a GitHub account has no
  identifiable owner. Those branches are counted in a note rather than flagged,
  because an unlinked email is too weak a signal on its own.

## Develop

```sh
go build -o gh-arborist .   # or: make build
go test ./...               # or: make test
gh extension install .      # install the local build as a gh extension
```
