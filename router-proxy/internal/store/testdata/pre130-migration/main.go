// Builds a disposable historical database using the production migration runner.
package main

import (
	"context"
	"log"
	"os"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		log.Fatal("expected database path and optional first excluded migration")
	}
	cutoff := "130_workflow_binding_predecessor"
	if len(os.Args) == 3 {
		cutoff = os.Args[2]
	}
	migrations := store.DefaultMigrations()
	for i, migration := range migrations {
		if migration.ID == cutoff {
			if i == 0 {
				log.Fatal("cannot exclude all migrations")
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
	}
	log.Fatalf("migration %s not found", cutoff)
}
