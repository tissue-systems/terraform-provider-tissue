terraform {
  required_providers {
    tissue = {
      source = "tissue-systems/tissue"
    }
  }
}

# The token can also come from TISSUE_TOKEN, which keeps it out of the
# configuration and the plan.
provider "tissue" {}
