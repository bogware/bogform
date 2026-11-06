terraform {
  required_providers {
    null = {
      # Since this source address doesn't include an explicit hostname,
      # it gets treated as a shorthand for the default registry, which
      # differs between Bogform and its predecessor.
      source = "hashicorp/null"
    }
  }
}