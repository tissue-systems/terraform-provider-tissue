data "tissue_mast_probe" "shop" {
  name = "shop"
}

output "shop_probe_target" {
  value = data.tissue_mast_probe.shop.target
}
