# Moving a tissue_mast_channel to tissue_mast_alert_source: change the type,
# keep the arguments, and tell Terraform where the state went.
moved {
  from = tissue_mast_channel.deploys
  to   = tissue_mast_alert_source.deploys
}

resource "tissue_mast_alert_source" "deploys" {
  name             = "deploys"
  default_priority = "loud"
}
