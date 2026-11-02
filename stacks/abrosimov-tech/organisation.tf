variable "billing_email" {
  description = "Billing email of the organisation. Required by the API on every update, and kept out of this public repository: `task plan` and `task apply` read it from the API into TF_VAR_billing_email."
  type        = string
  sensitive   = true
}

# Two-factor enforcement is not here because neither the API nor the
# provider can set it; it is switched on in the organisation's settings.
resource "github_organization_settings" "this" {
  billing_email = var.billing_email

  # Restated as GitHub has them, so the configuration says what the
  # organisation looks like.
  name                        = "abrosimov.tech"
  web_commit_signoff_required = true

  # Members may create public repositories too: GitHub refuses a
  # private-only creation policy on this plan ("Private-only repository
  # creation policy is not allowed for this organization", 422). The
  # stricter policy it does allow is members_can_create_repositories =
  # false, which takes private repositories from members as well.
  default_repository_permission           = "read"
  members_can_create_public_repositories  = true
  members_can_create_private_repositories = true
  members_can_fork_private_repositories   = false
  has_repository_projects                 = false
}

resource "github_actions_organization_permissions" "this" {
  enabled_repositories = "all"
  allowed_actions      = "all"
  sha_pinning_required = true
}

resource "github_actions_organization_workflow_permissions" "this" {
  organization_slug                = local.owner
  default_workflow_permissions     = "read"
  can_approve_pull_request_reviews = false
}
