// Build an actual pre-131 database, including historical Go table rebuilds.
// testdata keeps this fixture out of production builds.
package main

import (
	"context"
	"log"
	"os"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("expected a disposable database path")
	}
	migrations := store.DefaultMigrations()
	for i, migration := range migrations {
		if migration.ID != "131_optional_retired_rpc_recipe" {
			continue
		}
		s, err := store.Open(context.Background(), os.Args[1], store.Options{Migrations: migrations[:i]})
		if err != nil {
			log.Fatal(err)
		}
		if err := s.Close(); err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Fatal("launch recipe migration not found")
}
