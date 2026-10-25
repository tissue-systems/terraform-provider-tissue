data "tissue_mast_people" "all" {}

# Everyone on the account, in one rotation.
resource "tissue_mast_rotation" "everyone" {
  name  = "everyone"
  order = data.tissue_mast_people.all.people[*].user_id
}
