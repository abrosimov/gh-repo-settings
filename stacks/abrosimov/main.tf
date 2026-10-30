terraform {
  required_version = ">= 1.13.0"

  required_providers {
    github = {
      source  = "integrations/github"
      version = "6.13.0"
    }
  }

  # The state lives on the tofu-state branch, checked out as a worktree in
  # .tofu-state at the repository root, so CI can commit it without
  # touching the protected default branch. See the README.
  backend "local" {
    path = "../../.tofu-state/abrosimov.tfstate"
  }

  # The state is in a public repository, so it is never written in the
  # clear. The key comes from an enrolled key in ssh-agent through
  # cmd/tofu-sshagent-key, run with `go run` so nothing has to be built
  # first. GOTOOLCHAIN=auto lets go fetch the toolchain go.mod pins where
  # the environment says local. Paths are relative to the stack directory,
  # the working directory under `tofu -chdir`.
  encryption {
    key_provider "external" "sshagent" {
      command = ["env", "GOTOOLCHAIN=auto", "go", "run", "../../cmd/tofu-sshagent-key", "key", "--recipients", "../../encryption/recipients.json"]
    }

    method "aes_gcm" "sshagent" {
      keys = key_provider.external.sshagent
    }

    state {
      method   = method.aes_gcm.sshagent
      enforced = true
    }

    plan {
      method   = method.aes_gcm.sshagent
      enforced = true
    }
  }
}

# The token comes from `gh auth token` when GITHUB_TOKEN is unset.
provider "github" {
  owner = local.owner
}

locals {
  owner = "abrosimov"
}
