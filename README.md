# gh-repo-settings

One set of repository settings for every non-fork repository of `abrosimov` and of the
`abrosimov-tech` organisation, plus the organisation's own settings, managed with OpenTofu.

## What the baseline enforces

Every repository gets the same policy from [`modules/repo-baseline`](modules/repo-baseline):

| Area | Setting |
|---|---|
| Merging | merge commit and squash allowed, rebase off; auto-merge on; head branch deleted after merge |
| Ruleset `baseline` on the default branch | no deletion, no force-push, signed commits required, pull request required with zero approvals, merge or squash only; the admin role bypasses it |
| CI gate | on repositories with the topic `ci-gated`, the check `required` from GitHub Actions must pass (not required to be up to date with the base) |
| Actions | on or off per owner or per repository (see below); all actions allowed; pinning to a full commit SHA enforced once every workflow is pinned (`enforce_sha_pinning` in the stack); `GITHUB_TOKEN` read-only by default and unable to approve pull requests |
| Security | Dependabot alerts on (Renovate reads them), Dependabot security pull requests off (Renovate raises them); secret scanning and push protection on public repositories |

Identity and presentation — name, description, topics, visibility — are never touched.

## Layout

- `modules/repo-baseline` — the per-repository policy.
- `stacks/abrosimov`, `stacks/abrosimov-tech` — one root per owner. Each discovers its repositories
  on every plan and adopts new ones through `import` blocks; the organisation stack also manages
  organisation settings and its Actions policy.
- `cmd/tofu-sshagent-key` — the key provider OpenTofu runs to encrypt and decrypt state.
- `encryption/recipients.json` — the state key wrapped once per enrolled SSH key. Public by design.

No repository is named in the configuration: this repository is public and most of the others are
not. Keep it that way, and do not paste plan output into issues, pull requests or CI logs.

## Running it

Requirements: OpenTofu 1.13 or newer, Go as pinned in `go.mod`, the `gh` CLI logged in with full
scopes, and an enrolled ed25519 key in `ssh-agent`.

```sh
make plan                       # stack abrosimov
make apply
make plan OWNER=abrosimov-tech  # organisation stack
make apply OWNER=abrosimov-tech
make check                      # everything CI runs
```

Apply from the feature branch and commit the updated `terraform.tfstate` in the same pull request,
so the default branch always carries the state that matches its configuration.

## Gating a repository on CI

1. Add an aggregator job to the repository's CI, named exactly `required`:

   ```yaml
   required:
     name: required
     if: always()
     needs: [lint, test] # every other job
     runs-on: ubuntu-latest
     steps:
       - run: test "${{ contains(needs.*.result, 'failure') || contains(needs.*.result, 'cancelled') }}" = false
   ```

   A skipped job does not fail the gate, so a job skipped on purpose (`if:` on a release step)
   is fine. A job skipped because one it needs failed still fails the gate through that failure.

   The workflow must run on every pull request: a `paths:` filter leaves pull requests outside
   those paths without the check, and they never merge.

2. Once it is on the default branch, add the topic `ci-gated` to the repository and apply.

Adding the topic first blocks every merge, because the required check never reports.

## Switching GitHub Actions on or off

- **Every repository of an owner:** set `actions_enabled` in `stacks/<owner>/repositories.tf` and
  apply.
- **One repository:** add the topic `actions-disabled` or `actions-enabled` to it and apply. The
  topic wins over the stack-wide switch; with both topics present, Actions is off.

A `ci-gated` repository cannot have Actions off: the plan stops, because the required check would
never report and every merge would block.

## State encryption

The state and plan files are encrypted with AES-GCM. The key is a random data key, stored wrapped
once per enrolled SSH key; each wrapping key is derived from an ed25519 signature made by
`ssh-agent`, so only someone holding an enrolled key can decrypt.

```sh
make key-list    # enrolled keys, and which are in the agent now
make key-enroll  # wrap the key for every new ed25519 key in the agent
bin/tofu-sshagent-key revoke --recipients encryption/recipients.json SHA256:…
```

To enrol a new laptop, have an already enrolled key and the new key in the agent at the same time,
run `make key-enroll` and commit `encryption/recipients.json`.

Revoking a key removes its wrapping but not its knowledge of the data key, and the git history
keeps every earlier state. If a key is compromised, rotate the data key: move the current
configuration into a `fallback` block, initialise a new recipients file, apply, and commit.

If every key is lost, delete the state files and apply: the `import` blocks adopt every repository
again, so nothing is lost but the history.

## When a repository leaves the set

Archiving, transferring or deleting a repository removes it from discovery, and the next plan stops
at `prevent_destroy`. Forget it from the state rather than with a `removed` block, which would
put the name into this public repository:

```sh
tofu -chdir=stacks/abrosimov state rm 'module.repo["<name>"]'
```

Then commit the updated state.
