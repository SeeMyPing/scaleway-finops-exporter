# Repository rulesets

GitHub does not apply these files automatically: they are the source of truth
for the rulesets configured in **Settings → Rules → Rulesets**, kept in the
repository so that changes are reviewed like code.

| File | Target | Effect |
|---|---|---|
| [`main-branch.json`](main-branch.json) | default branch (`main`) | Changes only through pull requests, approved by a code owner, with every CI check green and no high CodeQL alert. No force push, no deletion, linear history. |
| [`release-tags.json`](release-tags.json) | `refs/tags/v*` | Only repository admins can create release tags (which trigger the release workflow); nobody can move or delete them. |

## Who can do what

- **Anyone** can fork the repository and open a pull request.
- **Only @SeeMyPing can approve**: [`CODEOWNERS`](../CODEOWNERS) makes
  @SeeMyPing the owner of every file, and the ruleset requires a code owner
  review.
- **Only @SeeMyPing can merge**: merging requires write access, which only
  the repository owner has. Do not add collaborators with the write role or
  above, or they could merge too.
- **Nobody pushes to `main` directly**, admins included.
- Admins can bypass the rules **only when merging a pull request**
  (`bypass_mode: pull_request`). This is how the maintainer merges their own
  pull requests, since GitHub does not let authors approve their own work.
  The merge box then shows "Merge without waiting for requirements to be
  met (bypass rules)": use it only for your own pull requests, once CI is
  green.

## Import or update

In the web UI: **Settings → Rules → Rulesets → New ruleset → Import a
ruleset**, then pick the JSON file. To update an existing ruleset, delete it
and import the file again, or edit it in the UI and export it back here.

With the GitHub CLI:

```sh
gh api -X POST repos/SeeMyPing/scaleway-finops-exporter/rulesets --input .github/rulesets/main-branch.json
gh api -X POST repos/SeeMyPing/scaleway-finops-exporter/rulesets --input .github/rulesets/release-tags.json
# later updates: gh api repos/SeeMyPing/scaleway-finops-exporter/rulesets to find the id, then
gh api -X PUT repos/SeeMyPing/scaleway-finops-exporter/rulesets/<id> --input .github/rulesets/main-branch.json
```

## Keep the required checks in sync

The `required_status_checks` contexts are the **job names** of
`.github/workflows/ci.yml` and `codeql.yml`. Renaming a job, or changing the
build matrix, without updating `main-branch.json` blocks every pull request
on a check that never reports. `integration_id` 15368 is the GitHub Actions
app, so that no other app can satisfy these checks.

## Related settings (not part of rulesets)

- **Settings → Actions → General → Fork pull request workflows**: require
  approval for all external contributors, so that workflows from forks only
  run after review.
- **Settings → General → Pull Requests**: allow squash and rebase merging only,
  and enable "Automatically delete head branches".
