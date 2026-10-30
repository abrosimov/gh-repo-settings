terraform {
  required_version = ">= 1.13.0"

  required_providers {
    github = {
      source  = "integrations/github"
      version = "6.13.0"
    }
  }

  # The state is committed to a public repository, so it is never written
  # in the clear. The key comes from the owner's ssh-agent through
  # cmd/tofu-sshagent-key; paths are relative to the stack directory,
  # which is the working directory under `tofu -chdir`.
  encryption {
    key_provider "external" "sshagent" {
      command = ["../../bin/tofu-sshagent-key", "key", "--recipients", "../../encryption/recipients.json"]
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
  owner = "abrosimov-tech"
}
