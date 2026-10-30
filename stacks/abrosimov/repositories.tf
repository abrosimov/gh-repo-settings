# Existing repositories are discovered on every plan, so one created by hand
# is adopted by the next apply without an edit. A repository to be created
# by OpenTofu is declared in declared_repositories below.
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

  # Repositories OpenTofu creates. Visibility and description apply at
  # creation only; afterwards they are edited on GitHub, like those of any
  # discovered repository. Once created, an entry may stay or go: discovery
  # keeps the repository managed either way.
  #
  #   declared_repositories = {
  #     "new-service" = { visibility = "private", description = "…" }
  #   }
  declared_repositories = {}

  # Forks follow their upstream's conventions, not this baseline. The search
  # already leaves them out; the filter keeps that true if the query changes.
  discovered_repositories = {
    for name, repo in data.github_repository.owned : name => {
      visibility     = repo.visibility
      description    = repo.description
      topics         = repo.topics
      default_branch = repo.default_branch
    }
    if !repo.fork
  }

  # What GitHub reports wins over the declaration once the repository exists.
  repositories = merge(
    {
      for name, repo in local.declared_repositories : name => {
        visibility     = repo.visibility
        description    = try(repo.description, null)
        topics         = []
        default_branch = null
      }
    },
    local.discovered_repositories,
  )
}

module "repo" {
  source   = "../../modules/repo-baseline"
  for_each = local.repositories

  name           = each.key
  visibility     = each.value.visibility
  description    = each.value.description
  topics         = each.value.topics
  current_branch = each.value.default_branch

  actions_enabled      = local.actions_enabled
  sha_pinning_required = local.enforce_sha_pinning
}

# Adopts each discovered repository; a no-op for those already in state,
# including the ones OpenTofu created. A repository that leaves the set
# (archived, transferred, deleted) stops the plan at prevent_destroy: forget
# it with `tofu state rm`, see the README.
import {
  for_each = local.discovered_repositories

  to = module.repo[each.key].github_repository.this
  id = each.key
}
