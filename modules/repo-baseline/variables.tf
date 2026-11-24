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
  description = "Whether required_checks must pass on the default branch. The stacks default it to whether Actions are on; a repository whose CI does not expose the checks yet must declare false, otherwise every merge blocks."
  type        = bool
  default     = false
}

variable "actions_push_bypass" {
  description = "Whether the repository's own workflows may push to the default branch past the baseline. Every workflow there reaches the branch through a reviewed pull request first, so this trusts them with what they do after a merge."
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

variable "sha_pinning_required" {
  description = "Whether to require every action and reusable workflow to be pinned to a full commit SHA. A workflow with a tag reference stops starting once this is on. False leaves the repository's current setting untouched rather than turning it off."
  type        = bool
  default     = true
}

variable "required_checks" {
  description = "GitHub Actions jobs that must pass on a gated repository. The aggregator job required is the convention; another list is for a repository whose CI does not expose it yet."
  type        = list(string)
  default     = ["required"]

  validation {
    condition     = length(var.required_checks) > 0
    error_message = "required_checks must name at least one job: an empty list would gate on nothing."
  }
}
