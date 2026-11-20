# The baseline is the only ruleset on a default branch. These are the
# rulesets that were created by hand on GitHub before it, adopted here only
# to be switched off: each narrowed the baseline (merge commits only, no
# bypass for unsigned commits, linear history, force-push forbidden on every
# branch) or required checks that required_checks in repositories.tf now
# carries. OpenTofu can delete a ruleset only once it is in the state, so
# they are disabled first and deleted by removing them from this map.
locals {
  retired_rulesets = {
    "agents-hooks-guard:24428572"              = "protect default branch"
    "arche-go:20180672"                        = "protect master"
    "devbox-setup:18532781"                    = "No force-push"
    "fpf-to-obsidian-vault-converter:24428577" = "protect default branch"
    "go-kdl2:24428579"                         = "protect default branch"
    "llm-tools-configuration:24413193"         = "master protection"
    "nomos:20182108"                           = "protect master"
    "personal-iwe-template:24428580"           = "protect default branch"
    "politeia:24428583"                        = "protect default branch"
    "setup_lab:23721745"                       = "master history safety"
    "setup_lab:23721747"                       = "master CI admission"
    "vddl:24428591"                            = "protect default branch"
    "warp-and-weft:24428593"                   = "protect default branch"
  }
}

resource "github_repository_ruleset" "retired" {
  for_each = local.retired_rulesets

  name        = each.value
  repository  = split(":", each.key)[0]
  target      = "branch"
  enforcement = "disabled"

  conditions {
    ref_name {
      include = ["~DEFAULT_BRANCH"]
      exclude = []
    }
  }

  rules {
    deletion = true
  }

  depends_on = [module.repo]
}

import {
  for_each = local.retired_rulesets

  to = github_repository_ruleset.retired[each.key]
  id = each.key
}

moved {
  from = github_repository_ruleset.go_kdl2_protect_default_branch
  to   = github_repository_ruleset.retired["go-kdl2:24428579"]
}

moved {
  from = github_repository_ruleset.setup_lab_master_ci_admission
  to   = github_repository_ruleset.retired["setup_lab:23721747"]
}
