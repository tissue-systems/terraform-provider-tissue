# Expects a beat after the 03:00 backup, and pages if none arrives by 03:30.
resource "tissue_mast_vital" "backup" {
  name        = "nightly-backup"
  schedule    = "0 3 * * *"
  schedule_tz = "America/Los_Angeles"
  grace_secs  = 1800
  host        = "db1"
}

# A job that should check in every five minutes.
resource "tissue_mast_vital" "queue" {
  name           = "queue-worker"
  period_secs    = 300
  grace_secs     = 120
  fail_threshold = 2
}
