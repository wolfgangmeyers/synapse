// Build a real pre-132 database for the router startup E2E test.
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
		if migration.ID == "132_workflow_journal" {
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
	log.Fatal("journal migration not found")
}
