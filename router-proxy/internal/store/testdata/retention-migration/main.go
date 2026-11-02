// This E2E fixture uses the real migration runner to create a pre-123 database.
// In particular, older table rebuilds cannot be reproduced by replaying SQL
// files alone. Keeping this under testdata excludes it from production builds.
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
		if migration.ID != "123_workflow_evidence_retention_guards" {
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
	log.Fatal("retention guard migration not found")
}
