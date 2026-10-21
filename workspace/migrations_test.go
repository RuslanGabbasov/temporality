package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListMigrationsOrdersByFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"000021_workspace.up.sql",
		"000075_profile_docker_socket.up.sql",
		"000044_skill_evolution_phase2.up.sql", // digits inside the name
		"000010_down_ignored.down.sql",         // down files never apply
		"README.md",                            // non-sql noise
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "000030_subdir.up.sql"), 0o755); err != nil {
		t.Fatal(err)
	}

	files, err := ListMigrations(dir)
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
	if _, err := ListMigrations(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("expected an error for a missing migrations directory")
	}
}

// The repository layout must match the ownership split the boot code relies
// on: migrations/workspace holds the baseline plus the increments after it,
// the single-file stores keep exactly their file in their directory, and the
// historical runtime chain is gone (the journal carries its schema as an
// embedded constant).
func TestMigrationDirectoriesOwnTheirChains(t *testing.T) {
	assertHas := func(dir string, want ...string) {
		t.Helper()
		files, err := ListMigrations(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if len(files) < len(want) {
			t.Fatalf("%s contains %v, want at least %v", dir, files, want)
		}
		for i, name := range want {
			if files[i] != name {
				t.Fatalf("%s[%d] = %s, want %s", dir, i, files[i], name)
			}
		}
	}
	// The workspace chain starts at the release baseline; increments follow.
	assertHas("../migrations/workspace", "0001_baseline_v2026.2.21.up.sql")
	assertHas("../migrations/outbox", "000019_kernel_event_outbox.up.sql")
	assertHas("../migrations/quota", "000020_kernel_run_quota.up.sql")
	if _, err := ListMigrations("../migrations/runtime"); err == nil {
		t.Error("migrations/runtime must not exist: nothing applies it anymore")
	}
}
