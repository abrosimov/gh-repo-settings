output "actions_enabled" {
  description = "Whether GitHub Actions runs in the repository, after the topic overrides."
  value       = local.actions_enabled
}

output "ci_gated" {
  description = "Whether the required CI check is enforced on the default branch."
  value       = local.is_ci_gate
}
