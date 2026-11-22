data "tissue_mast_channel" "deploys" {
  name = "deploys"
}

# Hand the URL to whatever sends to it.
resource "local_sensitive_file" "mast_url" {
  filename = "${path.module}/.mast-url"
  content  = data.tissue_mast_channel.deploys.url
}
