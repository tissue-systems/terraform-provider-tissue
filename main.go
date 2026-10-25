package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/tissue-systems/terraform-provider-tissue/internal/provider"
)

//go:generate go tool tfplugindocs generate --provider-name tissue

// Set by goreleaser.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/tissue-systems/tissue",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err)
	}
}
