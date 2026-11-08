locals {
  # A repository that requires Actions ignores the switch, so no
  # declaration can cut it off.
  actions_enabled = var.actions_required || var.actions_enabled
}

resource "github_actions_repository_permissions" "this" {
  repository      = github_repository.this.name
  enabled         = local.actions_enabled
  allowed_actions = local.actions_enabled ? "all" : null
  # Enforced or left alone, never relaxed: while the stack has not turned
  # it on, a repository that already requires pinning keeps requiring it.
  sha_pinning_required = local.actions_enabled && var.sha_pinning_required ? true : null
}

# Read by default: a workflow that writes declares it in its own
# permissions block, where a reviewer sees it.
resource "github_workflow_repository_permissions" "this" {
  repository                       = github_repository.this.name
  default_workflow_permissions     = "read"
  can_approve_pull_request_reviews = false
}
