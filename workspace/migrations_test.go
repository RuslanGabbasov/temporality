package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListMigrationsOrdersAndFloors(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"000018_observation_events.up.sql", // below the floor: another store's schema
		"000021_workspace.up.sql",
		"000044_skill_evolution_phase2.up.sql", // digits inside the name
		"000075_profile_docker_socket.up.sql",
		"000019_kernel_event_outbox.up.sql", // below the floor
		"000010_down_ignored.down.sql",      // down files never apply
		"README.md",                         // non-sql noise
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "000030_subdir.up.sql"), 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := ListMigrations(dir, "000021")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"000021_workspace.up.sql",
		"000044_skill_evolution_phase2.up.sql",
		"000075_profile_docker_socket.up.sql",
	}
	if len(files) != len(want) {
		t.Fatalf("files = %v, want %v", files, want)
	}
	for i, name := range want {
		if files[i] != name {
			t.Fatalf("files[%d] = %s, want %s", i, files[i], name)
		}
	}
}

func TestListMigrationsMissingDirectoryFails(t *testing.T) {
	if _, err := ListMigrations(filepath.Join(t.TempDir(), "absent"), "000021"); err == nil {
		t.Fatal("expected an error for a missing migrations directory")
	}
}
