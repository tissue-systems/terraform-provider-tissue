# Terraform provider for Tissue

Manages [Tissue Mast](https://tissue.systems/mast/) from Terraform or OpenTofu: channels,
vitals, on-call rotations and their overrides, and escalation policies. The docs that go on
the registry are in [docs/](docs/), generated from the schema and [examples/](examples/).

```hcl
terraform {
  required_providers {
    tissue = {
      source = "tissue-systems/tissue"
    }
  }
}

provider "tissue" {} # reads TISSUE_TOKEN

resource "tissue_mast_channel" "deploys" {
  name = "deploys"
}
```

The token comes from the dashboard's API tokens page and needs `mast:read` and `mast:write`.
`TISSUE_SERVER` points the provider somewhere other than `https://api.tissue.systems`.

## Developing

You need Go 1.26 and a `terraform` (or `tofu`) on your `PATH`.

```sh
go test ./...      # unit tests plus acceptance tests against an in-process fake of the Mast API
go generate ./...  # rebuilds docs/
```

The acceptance tests never touch a real account. They run Terraform against
`internal/provider/fake_test.go`, which answers the Mast routes the way the server does,
including the fields it fills in, trims or rewrites. If Terraform isn't on your `PATH`, set
`TF_ACC_TERRAFORM_PATH` to the binary.

To try a local build, add a `dev_overrides` block for `tissue-systems/tissue` to your
`~/.terraformrc` pointing at the directory `go install` writes to.

## Releasing

Push a `v*` tag. The release workflow builds with goreleaser and signs the checksums with the
key in the `GPG_PRIVATE_KEY` and `PASSPHRASE` secrets; the registry checks that signature
against the public key on the `tissue-systems` namespace.
