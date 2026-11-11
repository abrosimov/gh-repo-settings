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
