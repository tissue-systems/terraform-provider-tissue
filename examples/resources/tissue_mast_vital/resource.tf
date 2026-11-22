# Moving a tissue_mast_vital to tissue_mast_heartbeat: change the type, keep
# the arguments, and tell Terraform where the state went.
moved {
  from = tissue_mast_vital.backup
  to   = tissue_mast_heartbeat.backup
}

resource "tissue_mast_heartbeat" "backup" {
  name        = "nightly-backup"
  schedule    = "0 3 * * *"
  schedule_tz = "America/Los_Angeles"
  grace_secs  = 1800
}
