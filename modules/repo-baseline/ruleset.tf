locals {
  # integration_id of the GitHub Actions app, so a status of the same name
  # posted by anything else cannot satisfy the check.
  github_actions_integration_id = 15368

  # Built-in repository role id of Admin.
  admin_repository_role_id = 5
}

resource "github_repository_ruleset" "baseline" {
  name        = "baseline"
  repository  = github_repository.this.name
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["~DEFAULT_BRANCH"]
      exclude = []
    }
  }

  # The owner may merge a pull request past the rules (unsigned commits, a
  # red check) but cannot push to the default branch directly, so every
  # change reaches it through a pull request and its CI. This is the only
  # ruleset on a default branch: a second one would narrow the merge methods
  # or drop the bypass behind the baseline's back.
  bypass_actors {
    actor_id    = local.admin_repository_role_id
    actor_type  = "RepositoryRole"
    bypass_mode = "pull_request"
  }

  lifecycle {
    precondition {
      condition     = !(local.is_ci_gate && !local.actions_enabled)
      error_message = "Repository ${var.name} is declared ci_gated but has GitHub Actions off: the required check would never report and every merge would block. Change one of the two in its entry."
    }
  }

  rules {
    deletion            = true
    non_fast_forward    = true
    required_signatures = true

    pull_request {
      # Zero, so a bot's pull request merges on green checks alone: a solo
      # owner has nobody else to approve it.
      required_approving_review_count = 0
      allowed_merge_methods           = ["merge", "squash"]
    }

    dynamic "required_status_checks" {
      for_each = local.is_ci_gate ? [1] : []

      content {
        dynamic "required_check" {
          for_each = var.required_checks

          content {
            context        = required_check.value
            integration_id = local.github_actions_integration_id
          }
        }

        # Off: with it on, every merge leaves each other open pull request
        # behind and re-runs its CI. The trade-off is that two pull requests
        # green on their own can break the branch together; CI on push to
        # the default branch is what catches that.
        strict_required_status_checks_policy = false
      }
    }
  }
}
