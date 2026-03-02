package main

import (
	"flag"
	"log"

	"edgemesh/internal/gateway"
)

func main() {
	cfg := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	if err := gateway.Run(*cfg); err != nil {
		log.Fatal(err)
	}
}
