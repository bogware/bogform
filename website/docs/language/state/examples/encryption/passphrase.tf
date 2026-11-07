terraform {
  encryption {
    key_provider "passphrase" "state" {
      # Tried in this order. The first one that's set is used.
      env             = "BOGFORM_STATE_PASSPHRASE"
      keychain        = "my-repository"
      aws_ssm         = "/my-repository/state-passphrase"
      aws_region      = "us-east-1"
      azure_key_vault = "https://my-vault.vault.azure.net/secrets/state-passphrase"
    }

    key_provider "pbkdf2" "state" {
      chain = key_provider.passphrase.state
    }

    method "aes_gcm" "state" {
      keys = key_provider.pbkdf2.state
    }

    state {
      method   = method.aes_gcm.state
      enforced = true
    }

    plan {
      method   = method.aes_gcm.state
      enforced = true
    }
  }
}
