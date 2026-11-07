terraform {
  encryption {
    # The old passphrase keeps its original names, because pbkdf2 finds the settings
    # it stored in the state by its key provider's name.
    key_provider "passphrase" "state" {
      env     = "BOGFORM_STATE_PASSPHRASE"
      aws_ssm = "/my-repository/state-passphrase"
    }
    key_provider "pbkdf2" "state" {
      chain = key_provider.passphrase.state
    }
    method "aes_gcm" "state" {
      keys = key_provider.pbkdf2.state
    }

    # The new passphrase, under new names.
    key_provider "passphrase" "v2" {
      env     = "BOGFORM_STATE_PASSPHRASE_V2"
      aws_ssm = "/my-repository/state-passphrase-v2"
    }
    key_provider "pbkdf2" "v2" {
      chain = key_provider.passphrase.v2
    }
    method "aes_gcm" "v2" {
      keys = key_provider.pbkdf2.v2
    }

    state {
      method   = method.aes_gcm.v2
      enforced = true
      fallback {
        method = method.aes_gcm.state
      }
    }

    plan {
      method   = method.aes_gcm.v2
      enforced = true
      fallback {
        method = method.aes_gcm.state
      }
    }
  }
}
