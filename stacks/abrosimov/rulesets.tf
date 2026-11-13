# Rulesets a repository has beyond the baseline's.

# go-kdl2's ruleset from before the baseline, created by hand on GitHub and
# adopted here as it stands. Every rule in it is also enforced by the
# baseline ruleset, or by a stricter one: GitHub applies every active ruleset,
# so the baseline's merge methods leave rebase out regardless, and the
# baseline's check required needs the job "Build, lint, test" in go-kdl2's CI.
# Dropping this resource deletes the ruleset without loosening the branch.
resource "github_repository_ruleset" "go_kdl2_protect_default_branch" {
  name        = "protect default branch"
  repository  = "go-kdl2"
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["~DEFAULT_BRANCH"]
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

    pull_request {
      required_approving_review_count = 0
      allowed_merge_methods           = ["merge", "squash", "rebase"]
    }

    required_status_checks {
      required_check {
        context        = "Build, lint, test"
        integration_id = 15368
      }

      strict_required_status_checks_policy = false
    }
  }

  depends_on = [module.repo]
}

import {
  to = github_repository_ruleset.go_kdl2_protect_default_branch
  id = "go-kdl2:24428579"
}

# llm-tools-configuration's ruleset from before the baseline, created by hand
# on GitHub and adopted here as it stands but for the Actions bypass. It is
# the repository's CI gate: it names validate and inspect directly, where
# ci_gated in the baseline would name the aggregator required instead, so
# dropping this resource would loosen the branch rather than tidy it.
#
# The bypass is here as well as in the baseline because GitHub applies every
# active ruleset and reads each one's bypass list separately: without this
# block the ledger workflows are refused by this ruleset alone.
resource "github_repository_ruleset" "llm_tools_configuration_master_protection" {
  name        = "master protection"
  repository  = "llm-tools-configuration"
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["~DEFAULT_BRANCH"]
      exclude = []
    }
  }

  bypass_actors {
    actor_id    = 33803
    actor_type  = "User"
    bypass_mode = "pull_request"
  }

  bypass_actors {
    actor_id    = 15368
    actor_type  = "Integration"
    bypass_mode = "always"
  }

  rules {
    deletion            = true
    non_fast_forward    = true
    required_signatures = true

    pull_request {
      required_approving_review_count = 0
      allowed_merge_methods           = ["merge"]
    }

    required_status_checks {
      required_check {
        context        = "validate"
        integration_id = 15368
      }

      required_check {
        context        = "inspect"
        integration_id = 15368
      }

      strict_required_status_checks_policy = false
    }
  }

  depends_on = [module.repo]
}

import {
  to = github_repository_ruleset.llm_tools_configuration_master_protection
  id = "llm-tools-configuration:24413193"
}
