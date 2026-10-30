variable "name" {
  description = "Repository name, without the owner. The owner comes from the provider configuration."
  type        = string
}

variable "visibility" {
  description = "Repository visibility as GitHub reports it. Read, never written: the baseline does not change who can see a repository."
  type        = string

  validation {
    condition     = contains(["public", "private", "internal"], var.visibility)
    error_message = "The visibility must be public, private or internal."
  }
}

variable "topics" {
  description = "Repository topics as GitHub reports them. The topic named by ci_gate_topic opts the repository into the required CI check."
  type        = list(string)
  default     = []
}

variable "ci_gate_topic" {
  description = "Topic that makes the aggregator check required on the default branch. Set it only once the repository's CI exposes that check, otherwise every merge blocks."
  type        = string
  default     = "ci-gated"
}

variable "actions_enabled" {
  description = "Stack-wide switch for GitHub Actions, used when the repository carries neither override topic."
  type        = bool
  default     = true
}

variable "actions_enabled_topic" {
  description = "Topic that turns GitHub Actions on for one repository whatever the stack-wide switch says."
  type        = string
  default     = "actions-enabled"
}

variable "actions_disabled_topic" {
  description = "Topic that turns GitHub Actions off for one repository whatever the stack-wide switch says."
  type        = string
  default     = "actions-disabled"
}

variable "sha_pinning_required" {
  description = "Whether to require every action and reusable workflow to be pinned to a full commit SHA. A workflow with a tag reference stops starting once this is on. False leaves the repository's current setting untouched rather than turning it off."
  type        = bool
  default     = true
}

variable "required_check" {
  description = "Name of the aggregator job every gated repository's CI exposes."
  type        = string
  default     = "required"
}
