locals {
  is_public  = var.visibility == "public"
  is_ci_gate = contains(var.topics, var.ci_gate_topic)
}

# Policy only. Identity and presentation — name, description, topics,
# visibility — stay with whoever edits them on GitHub, so the baseline can
# never flip a private repository public or rename one. The repository is
# imported, not created, and must survive any mistake in this configuration:
# prevent_destroy stops the plan, archive_on_destroy is the second line.
resource "github_repository" "this" {
  name = var.name

  has_issues   = true
  has_wiki     = false
  has_projects = false

  # Merge commits keep a branch's atomic commits reachable; squash is for
  # single-commit pull requests such as Renovate's. Rebase is off because
  # GitHub cannot sign the commits it rewrites, which required_signatures
  # in the ruleset would then refuse.
  allow_merge_commit          = true
  merge_commit_title          = "PR_TITLE"
  merge_commit_message        = "PR_BODY"
  allow_squash_merge          = true
  squash_merge_commit_title   = "PR_TITLE"
  squash_merge_commit_message = "COMMIT_MESSAGES"
  allow_rebase_merge          = false

  allow_auto_merge       = true
  allow_update_branch    = true
  delete_branch_on_merge = true

  archive_on_destroy = true

  # Secret scanning and push protection are free on public repositories
  # only; on a private one they need a paid licence and the API rejects
  # them, so the block exists for public repositories alone.
  dynamic "security_and_analysis" {
    for_each = local.is_public ? [1] : []

    content {
      secret_scanning {
        status = "enabled"
      }

      secret_scanning_push_protection {
        status = "enabled"
      }
    }
  }

  lifecycle {
    prevent_destroy = true

    ignore_changes = [
      description,
      homepage_url,
      topics,
      visibility,
      private,
      is_template,
      template,
      pages,
      archived,
      has_discussions,
      has_downloads,
      auto_init,
      gitignore_template,
      license_template,
      vulnerability_alerts,
    ]
  }
}

# Renovate reads the alerts to raise its security pull requests, so the
# alerts are on everywhere and Dependabot's own security pull requests are
# off: two bots proposing the same fix is noise.
resource "github_repository_vulnerability_alerts" "this" {
  repository = github_repository.this.name
  enabled    = true
}

resource "github_repository_dependabot_security_updates" "this" {
  repository = github_repository.this.name
  enabled    = false

  depends_on = [github_repository_vulnerability_alerts.this]
}
