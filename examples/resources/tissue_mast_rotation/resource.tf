data "tissue_mast_person" "ana" {
  email = "ana@example.com"
}

data "tissue_mast_person" "ben" {
  email = "ben@example.com"
}

resource "tissue_mast_rotation" "primary" {
  name         = "primary"
  handoff      = "weekly"
  handoff_day  = 0
  handoff_time = "09:00"
  tz           = "Europe/Berlin"
  order        = [data.tissue_mast_person.ana.user_id, data.tissue_mast_person.ben.user_id]
}
