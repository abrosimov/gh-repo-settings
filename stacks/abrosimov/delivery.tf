# CI/CD of this repository: pull requests plan, the default branch applies.
# The environments hold the secrets; their values are set with
# `gh secret set --env`, never through OpenTofu, so a CI apply that does not
# know them cannot blank them.
locals {
  delivery_repository = "gh-repo-settings"
}

# Read-only tokens: available to every same-repository pull request.
resource "github_repository_environment" "plan" {
  repository  = local.delivery_repository
  environment = "plan"

  depends_on = [module.repo]
}

# Write tokens: only a run on master reaches them.
resource "github_repository_environment" "apply" {
  repository  = local.delivery_repository
  environment = "apply"

  deployment_branch_policy {
    protected_branches     = false
    custom_branch_policies = true
  }

  depends_on = [module.repo]
}

resource "github_repository_environment_deployment_policy" "apply_master" {
  repository     = local.delivery_repository
  environment    = github_repository_environment.apply.environment
  branch_pattern = "master"
}

# The state branch is pushed by CI with fast-forwards only; losing it or
# rewriting it would lose the record of what is applied.
resource "github_repository_ruleset" "tofu_state" {
  name        = "tofu-state"
  repository  = local.delivery_repository
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["refs/heads/tofu-state"]
      exclude = []
    }
  }

  bypass_actors {
    actor_id    = 5
    actor_type  = "RepositoryRole"
    bypass_mode = "always"
  }

  rules {
    deletion         = true
    non_fast_forward = true
  }

  depends_on = [module.repo]
}
