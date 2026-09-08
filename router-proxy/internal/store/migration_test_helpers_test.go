package store

import "testing"

func migrationIndex(t *testing.T, migrations []Migration, id string) int {
	t.Helper()
	for index, migration := range migrations {
		if migration.ID == id {
			return index
		}
	}
	t.Fatalf("migration %q is missing", id)
	return 0
}

func migrationByID(t *testing.T, migrations []Migration, id string) Migration {
	t.Helper()
	return migrations[migrationIndex(t, migrations, id)]
}
