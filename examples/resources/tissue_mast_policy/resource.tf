resource "tissue_mast_policy" "default" {
  name              = "default"
  repeat_every_secs = 1800

  steps = [
    { target = "oncall", rotation_id = tissue_mast_rotation.primary.id, priority = "page" },
    { target = "next", rotation_id = tissue_mast_rotation.primary.id, priority = "page", after_secs = 300 },
    { target = "everyone", priority = "page", after_secs = 900 },
  ]
}
