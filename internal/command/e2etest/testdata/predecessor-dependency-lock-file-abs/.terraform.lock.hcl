
# This intentionally refers to the registry of Bogform's predecessor, but
# the associated configuration refers to the shorthand "hashicorp/null"
# and so will be understood by Bogform as depending instead on
# "registry.opentofu.org/hashicorp/null", thereby activating our special
# fixup behavior and selecting the same version of Bogform's re-release
# of this provider.
provider "registry.terraform.io/hashicorp/null" {
  version = "3.2.0"
  hashes = [
    "h1:DvLRiv4Pbjq3Rh0yNWtq+9dwVXqHF+bQspfhckLyFWU=",
  ]
}
