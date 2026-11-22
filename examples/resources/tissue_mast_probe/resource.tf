# Checks the health endpoint every minute and pages the ops alert source when it stops answering 200.
resource "tissue_mast_probe" "shop" {
  name            = "shop"
  target          = "https://shop.example.com/health"
  interval_secs   = 60
  expect_status   = 200
  body_contains   = "ok"
  max_ms          = 2000
  host            = "web1"
  alert_source_id = tissue_mast_alert_source.ops.id
}

# The MX records have to be exactly these two.
resource "tissue_mast_probe" "mx" {
  kind       = "dns"
  target     = "example.com"
  method     = "MX"
  expect_set = "mx1.example.com,mx2.example.com"
}

# Postgres must not be reachable from outside. This pages when the port opens.
resource "tissue_mast_probe" "db_closed" {
  kind   = "tcp"
  target = "db.example.com:5432"
  expect = "down"
}

# An endpoint behind a token. Keep the token in a variable, not in the file.
resource "tissue_mast_probe" "api" {
  target = "https://api.example.com/v1/status"
  auth = {
    type  = "bearer"
    token = var.status_token
  }
}
