# Ben covers Ana from Christmas Eve through Boxing Day.
resource "tissue_mast_override" "holiday" {
  rotation_id    = tissue_mast_rotation.primary.id
  user_id        = data.tissue_mast_person.ben.user_id
  covers_user_id = data.tissue_mast_person.ana.user_id
  start          = "2026-12-24"
  end            = "2026-12-26"
}
