# A rotation made in the app.
data "tissue_mast_rotation" "primary" {
  name = "primary"
}

output "on_call" {
  value = data.tissue_mast_rotation.primary.on_call_user_id
}
