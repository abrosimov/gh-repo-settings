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

variable "description" {
  description = "Repository description, applied when OpenTofu creates the repository and ignored afterwards."
  type        = string
  default     = null
}

variable "is_template" {
  description = "Whether the repository is a template others are generated from. Applied when OpenTofu creates the repository and ignored afterwards."
  type        = bool
  default     = false
}

variable "template" {
  description = "Template the repository is generated from when OpenTofu creates it, null for an empty repository with an initial commit. Pass it for a repository about to be created only: the provider reports a permanent difference for one that already exists."
  type = object({
    owner      = string
    repository = string
  })
  default = null
}

variable "current_branch" {
  description = "Default branch as GitHub reports it, null for a repository OpenTofu is about to create. An empty repository reports the account's default branch name although it has no branch; when that is already master, setting it is a no-op. An empty string skips the setting."
  type        = string
  default     = null
}

variable "default_branch" {
  description = "Default branch every repository must have; an existing default branch of another name is renamed to it."
  type        = string
  default     = "master"
}

variable "topics" {
  description = "Repository topics as GitHub reports them, or as declared for a repository OpenTofu is about to create, which gets them at creation. Presentation only: no policy reads them."
  type        = list(string)
  default     = []
}

variable "ci_gated" {
  description = "Whether the aggregator check is required on the default branch. Set it only once the repository's CI exposes that check, otherwise every merge blocks."
  type        = bool
  default     = false
}

variable "actions_enabled" {
  description = "Whether GitHub Actions is on: the repository's own declaration, or the stack-wide switch when it declares none."
  type        = bool
  default     = true
}

variable "actions_required" {
  description = "Keeps GitHub Actions on whatever actions_enabled says. For a repository whose own CI applies this configuration: with Actions off there, only a local apply could turn them back on."
  type        = bool
  default     = false
}

variable "actions_bypass" {
  description = "Whether the GitHub Actions app bypasses the baseline ruleset on the default branch. For a repository whose own workflows commit to it — an append-only ledger, a generated index — where a pull request and its CI run per machine commit buys nothing. A bypass is whole and per ruleset: it exempts every workflow of the repository from every rule here, on every path, and another ruleset on the same branch needs its own."
  type        = bool
  default     = false
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
