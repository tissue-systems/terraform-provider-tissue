resource "tissue_mast_channel" "deploys" {
  name             = "deploys"
  default_priority = "loud"
  sound            = "chirp"

  # Quiet from 22:00 to 07:00 Berlin time.
  quiet_start = 1320
  quiet_end   = 420
  quiet_tz    = "Europe/Berlin"

  policy_id = tissue_mast_policy.default.id
}

output "deploys_url" {
  value     = tissue_mast_channel.deploys.url
  sensitive = true
}
