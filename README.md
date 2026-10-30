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

The default branch is always `master`. Identity and presentation — name, description, topics,
visibility — are set only when OpenTofu creates a repository and never touched afterwards.

## Layout

- `modules/repo-baseline` — the per-repository policy.
- `stacks/abrosimov`, `stacks/abrosimov-tech` — one root per owner. Each discovers its repositories
  on every plan and adopts new ones through `import` blocks; the organisation stack also manages
  organisation settings and its Actions policy.
- `cmd/tofu-sshagent-key` — the key provider OpenTofu runs to encrypt and decrypt state.
- `encryption/recipients.json` — the state key wrapped once per enrolled SSH key. Public by design.
- `stacks/abrosimov/delivery.tf` — this repository's CI/CD environments and the state branch rule.
- branch `tofu-state` — the encrypted state files.

## Creating a repository

Declare it in `declared_repositories` in the owner's stack and apply:

```hcl
declared_repositories = {
  "new-service" = { visibility = "private", description = "What it is for" }
}
```

It is created with an initial commit on `master`, and the baseline applies from the first apply.
Visibility and description are set at creation only; later edits happen on GitHub. A repository
created by hand needs no declaration: the next apply adopts it.

## Default branch

Every repository's default branch is `master`. A repository whose default branch has another name
gets it renamed on the next apply. An empty repository has no branch yet, so its first push decides:
push `master` first.

## How changes ship

CI does the work; a laptop is only needed for keys and emergencies.

1. Open a pull request. The `plan` job plans both stacks with read-only tokens and puts each plan
   in the run's summary; a failed plan fails `required`.
2. Merge it. The `apply` job applies both stacks with write tokens and commits the state to the
   `tofu-state` branch.

Secrets live in two environments of this repository and are set with `gh secret set --env`:

| Secret | `plan` | `apply` |
|---|---|---|
| `CI_SSH_KEY` | the CI ed25519 private key, enrolled as a state-key recipient | same |
| `GH_TOKEN_ABROSIMOV` | fine-grained token for `abrosimov`, read-only | same owner, read and write |
| `GH_TOKEN_ABROSIMOV_TECH` | fine-grained token for `abrosimov-tech`, read-only | same owner, read and write |

`apply` is reachable from `master` only; `plan` runs for pull requests from this repository, never
from forks. The environments themselves are managed in `stacks/abrosimov/delivery.tf`; their secret
values never pass through OpenTofu.

## State

The state of each stack is a file on the `tofu-state` branch: `abrosimov.tfstate`,
`abrosimov-tech.tfstate`. The baseline ruleset protects only the default branch, so CI pushes the
branch with the workflow token; a separate ruleset forbids deleting or force-pushing it.

## Running it locally

Requirements: OpenTofu 1.13 or newer, Go as pinned in `go.mod`, the `gh` CLI logged in (the
provider uses `gh auth token` when `GITHUB_TOKEN` is unset), and an enrolled ed25519 key in
`ssh-agent`. Check out the state branch once, as a worktree at the repository root:

```sh
git worktree add .tofu-state tofu-state
```

Then, from the repository root:

```sh
git -C .tofu-state pull
tofu -chdir=stacks/abrosimov init
tofu -chdir=stacks/abrosimov plan
```

The organisation stack needs the billing email, which the API requires on every update and which is
kept out of this repository:

```sh
export TF_VAR_billing_email="$(gh api orgs/abrosimov-tech --jq .billing_email)"
```

After a local apply, commit and push the state from `.tofu-state`, so CI starts from it.

The checks CI runs: `go vet -tags unit ./...`, `go test -tags unit ./...`, golangci-lint at the
version in `.github/workflows/ci.yml`, `tofu fmt -check -recursive`, and `tofu validate` per stack.

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
go run ./cmd/tofu-sshagent-key list --recipients encryption/recipients.json
go run ./cmd/tofu-sshagent-key enroll --recipients encryption/recipients.json
go run ./cmd/tofu-sshagent-key revoke --recipients encryption/recipients.json SHA256:…
```

`list` shows the enrolled keys and which are in the agent now; `enroll` wraps the key for every new
ed25519 key in the agent. To enrol a new laptop, have an already enrolled key and the new key in the
agent at the same time, run `enroll` and commit `encryption/recipients.json`.

The CI key (`gh-repo-settings-ci`) was enrolled the same way: added to the agent for one `enroll`,
then removed. To replace it, generate a new key, enrol it, store it in `CI_SSH_KEY` in both
environments, and revoke the old fingerprint.

Revoking a key removes its wrapping but not its knowledge of the data key, and the git history
keeps every earlier state. If a key is compromised, rotate the data key: move the current
configuration into a `fallback` block, initialise a new recipients file, apply, and commit.

If every key is lost, delete the state files and apply: the `import` blocks adopt every repository
again, so nothing is lost but the history.

## When a repository leaves the set

Archiving, transferring or deleting a repository removes it from discovery, and the next plan stops
at `prevent_destroy`. Forget it from the state (a `removed` block cannot name a single `for_each`
instance), and drop it from `declared_repositories` if it is there:

```sh
tofu -chdir=stacks/abrosimov state rm 'module.repo["<name>"]'
```

Then commit and push the updated state from `.tofu-state`.
