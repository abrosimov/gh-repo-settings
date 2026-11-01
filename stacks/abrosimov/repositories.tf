# declared_repositories below is the list of repositories this owner has.
# Discovery reads what GitHub has on every plan, only to check the list
# against it and to read the attributes GitHub owns.
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
  # Off since 2026-11-01 to save Actions minutes while the baseline settles.
  actions_enabled = false

  # Off until every workflow is pinned. On 2026-10-30, 63 action references
  # in 12 workflows still named a tag; Renovate's pinning pull requests for
  # them wait on CI, and turning this on first would stop those workflows
  # from starting at all.
  enforce_sha_pinning = false

  # Every repository OpenTofu creates is generated from this template,
  # unless its entry sets template = null.
  repository_template = {
    owner      = local.owner
    repository = "repo-template"
  }

  # Every repository of this owner, forks aside. A repository on GitHub but
  # not here stops the plan; one here but not on GitHub is created. The
  # attributes apply at creation only; afterwards visibility, description
  # and topics are edited on GitHub, so an existing repository needs none.
  #
  #   "new-service" = {
  #     visibility  = "private"    # required to create
  #     description = "…"
  #     topics      = ["ci-gated"] # optional
  #     is_template = false        # optional
  #     template    = null         # optional: an empty repository instead
  #   }
  declared_repositories = {
    "2026-june-ai-ideas-synthesis"    = {}
    "agents-hooks-guard"              = {}
    "aletheia"                        = {}
    "apme"                            = {}
    "cv"                              = {}
    "daidalos"                        = {}
    "devbox-setup"                    = {}
    "drive"                           = {}
    "fpf-to-obsidian-vault-converter" = {}
    "gh-repo-settings" = {
      visibility  = "public"
      description = "Unified GitHub repository and organisation settings for abrosimov and abrosimov-tech, managed with OpenTofu"
    }
    "go-core"                 = {}
    "go-devtools"             = {}
    "go-kdl2"                 = {}
    "go-provider"             = {}
    "grpcApp"                 = {}
    "kadmos"                  = {}
    "llm-orchestrator"        = {}
    "llm-tools-configuration" = {}
    "ncp_database"            = {}
    "nomos"                   = {}
    "obsidian-content-plugin" = {}
    "obsidian-notes"          = {}
    "obsidian-tasks-system"   = {}
    "otelcol-otelbox"         = {}
    "personal-iwe-template"   = {}
    "pinax"                   = {}
    "politeia"                = {}
    "proj"                    = {}
    "py-core"                 = {}
    "renovate-config"         = {}
    "repo-template" = {
      visibility  = "public"
      description = "Starting point for new repositories: pinned Renovate preset, CI with the required check, Apache-2.0"
      topics      = ["actions-enabled"]
      is_template = true
      template    = null
    }
    "setup_lab"                 = {}
    "tamias"                    = {}
    "vddl"                      = {}
    "warp-and-weft"             = {}
    "workbench-design-spec-tmp" = {}
  }

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

  # What GitHub reports wins over the declaration once the repository exists.
  repositories = {
    for name, repo in local.declared_repositories : name => merge(
      contains(local.repositories_to_create, name) ? {
        visibility     = try(repo.visibility, null)
        description    = try(repo.description, null)
        topics         = tolist(try(repo.topics, []))
        default_branch = null
      } : local.discovered_repositories[name],
      {
        is_template = try(repo.is_template, false)
        template    = contains(local.repositories_to_create, name) ? try(repo.template, local.repository_template) : null
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
      error_message = "Repositories on GitHub but not in declared_repositories: ${join(", ", sort(local.undeclared_repositories))}. Add them to stacks/abrosimov/repositories.tf."
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

  actions_enabled      = local.actions_enabled
  actions_required     = each.key == local.delivery_repository
  sha_pinning_required = local.enforce_sha_pinning
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
