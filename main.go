package main

import (
	"context"
	"log"

	"ytclone/internal/app"
	"ytclone/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	if err := app.Run(context.Background(), cfg); err != nil {
		log.Fatal(err)
	}
}
