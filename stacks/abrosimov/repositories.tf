# This repository is public and the owner's private repositories are not,
# so no repository is named here: the set is discovered on every plan, and
# a new repository is adopted by the next apply without an edit.
data "github_repositories" "owned" {
  query = "user:${local.owner} archived:false"
}

data "github_repository" "owned" {
  for_each = toset(data.github_repositories.owned.names)

  full_name = "${local.owner}/${each.key}"
}

locals {
  # Stack-wide switch for GitHub Actions. One repository is switched against
  # it with the topic actions-enabled or actions-disabled.
  actions_enabled = true

  # Off until every workflow is pinned. On 2026-10-30, 63 action references
  # in 12 workflows still named a tag; Renovate's pinning pull requests for
  # them wait on CI, and turning this on first would stop those workflows
  # from starting at all.
  enforce_sha_pinning = false

  # Forks follow their upstream's conventions, not this baseline. The search
  # already leaves them out; the filter keeps that true if the query changes.
  repositories = {
    for name, repo in data.github_repository.owned : name => {
      visibility = repo.visibility
      topics     = repo.topics
    }
    if !repo.fork
  }
}

module "repo" {
  source   = "../../modules/repo-baseline"
  for_each = local.repositories

  name       = each.key
  visibility = each.value.visibility
  topics     = each.value.topics

  actions_enabled      = local.actions_enabled
  sha_pinning_required = local.enforce_sha_pinning
}

# Adopts each discovered repository; a no-op for those already in state.
# A repository that leaves the set (archived, transferred, deleted) stops the
# plan at prevent_destroy: forget it with `tofu state rm`, see the README.
import {
  for_each = local.repositories

  to = module.repo[each.key].github_repository.this
  id = each.key
}
