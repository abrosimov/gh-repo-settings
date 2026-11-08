# declared_repositories below is the list of repositories this owner has.
# Discovery reads what GitHub has on every plan, only to check the list
# against it and to read the attributes GitHub owns.
# The organisation's own listing rather than the search the personal stack
# uses: search answers 422 for an organisation with no repositories yet.
data "github_organization" "this" {
  name                  = local.owner
  ignore_archived_repos = true
}

data "github_repository" "owned" {
  for_each = toset([for full_name in data.github_organization.this.repositories : split("/", full_name)[1]])

  full_name = "${local.owner}/${each.key}"
}

locals {
  # Stack-wide switch for GitHub Actions. One repository is switched against
  # it with actions_enabled in its entry below.
  # Off since 2026-11-01 to save Actions minutes while the baseline settles.
  actions_enabled = false

  # No template yet: whether this organisation's token may generate from a
  # template another owner holds is untested. Until then a created
  # repository starts empty, with an initial commit.
  repository_template = null

  # Every repository of this owner, forks aside. A repository on GitHub but
  # not here stops the plan; one here but not on GitHub is created.
  # visibility, description, topics and is_template apply at creation only;
  # afterwards they are presentation and stay with GitHub. actions_enabled
  # and ci_gated are policy and apply on every plan.
  #
  #   "new-service" = {
  #     visibility      = "private" # required to create
  #     description     = "…"
  #     topics          = ["go"]    # optional
  #     is_template     = false     # optional
  #     actions_enabled = true      # optional: overrides the stack-wide switch
  #     ci_gated        = true      # optional: requires the check required
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

  undeclared_repositories = setsubtract(keys(local.discovered_repositories), keys(local.declared_repositories))
  repositories_to_create  = setsubtract(keys(local.declared_repositories), keys(local.discovered_repositories))

  # What GitHub reports wins over the declared presentation once the
  # repository exists; the declared policy always wins.
  repositories = {
    for name, repo in local.declared_repositories : name => merge(
      contains(local.repositories_to_create, name) ? {
        visibility     = try(repo.visibility, null)
        description    = try(repo.description, null)
        topics         = tolist(try(repo.topics, []))
        default_branch = null
      } : local.discovered_repositories[name],
      {
        is_template     = try(repo.is_template, false)
        template        = contains(local.repositories_to_create, name) ? try(repo.template, local.repository_template) : null
        actions_enabled = try(repo.actions_enabled, local.actions_enabled)
        ci_gated        = try(repo.ci_gated, false)
      },
    )
  }
}

# Holds the checks of the list against GitHub, so a mismatch stops the plan
# in a pull request and in the scheduled run on master.
resource "terraform_data" "inventory" {
  lifecycle {
    precondition {
      condition     = length(local.undeclared_repositories) == 0
      error_message = "Repositories on GitHub but not in declared_repositories: ${join(", ", sort(local.undeclared_repositories))}. Add them to stacks/abrosimov-tech/repositories.tf."
    }

    precondition {
      condition     = alltrue([for name in local.repositories_to_create : try(local.declared_repositories[name].visibility, null) != null])
      error_message = "Repositories in declared_repositories but not on GitHub, without a visibility to create them with: ${join(", ", sort([for name in local.repositories_to_create : name if try(local.declared_repositories[name].visibility, null) == null]))}. Declare a visibility to create one, or remove the entry of one that was archived, transferred or deleted (see the README)."
    }
  }
}

module "repo" {
  source   = "../../modules/repo-baseline"
  for_each = local.repositories

  name           = each.key
  visibility     = each.value.visibility
  description    = each.value.description
  topics         = each.value.topics
  current_branch = each.value.default_branch
  is_template    = each.value.is_template
  template       = each.value.template

  actions_enabled = each.value.actions_enabled
  ci_gated        = each.value.ci_gated
}

# Adopts each declared repository that already exists; a no-op for those
# already in state, including the ones OpenTofu created. A repository that
# leaves GitHub (archived, transferred, deleted) must leave the list and the
# state too, see the README.
import {
  for_each = { for name, repo in local.discovered_repositories : name => repo if contains(keys(local.declared_repositories), name) }

  to = module.repo[each.key].github_repository.this
  id = each.key
}
