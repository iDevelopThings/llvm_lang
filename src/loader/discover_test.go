package loader

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/afero"
)

func TestDiscoverPackages_FindsEveryPackageDirectory(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "ws")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(root, "app", "main.llx"):         "// main",
		filepath.Join(root, "std", "strings", "s.llx"): "// strings",
		filepath.Join(root, "std", "time", "t.llx"):    "// time",
	})

	var names []string
	for c := range DiscoverPackages(fs, root) {
		names = append(names, c.Name)
		want := filepath.Base(c.Dir)
		if c.Name != want {
			t.Errorf("candidate %+v: Name = %q, want %q", c, c.Name, want)
		}
	}
	slices.Sort(names)
	if got, want := names, []string{"app", "strings", "time"}; !slices.Equal(got, want) {
		t.Errorf("discovered package names = %v, want %v", got, want)
	}
}

func TestDiscoverPackages_SkipsDotDirectories(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "ws")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(root, "app", "main.llx"):               "// main",
		filepath.Join(root, ".git", "hooks", "pre.llx"):      "// not a package",
		filepath.Join(root, ".claude", "worktrees", "w.llx"): "// not a package",
	})

	var names []string
	for c := range DiscoverPackages(fs, root) {
		names = append(names, c.Name)
	}
	if got, want := names, []string{"app"}; !slices.Equal(got, want) {
		t.Errorf("discovered package names = %v, want %v (dot-directories must be skipped)", got, want)
	}
}

func TestDiscoverPackages_SkipsDirectoriesWithNoSourceFiles(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "ws")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(root, "app", "main.llx"):   "// main",
		filepath.Join(root, "docs", "README.md"): "# not a package",
	})

	var names []string
	for c := range DiscoverPackages(fs, root) {
		names = append(names, c.Name)
	}
	if got, want := names, []string{"app"}; !slices.Equal(got, want) {
		t.Errorf("discovered package names = %v, want %v (a dir with no .llx files must be skipped)", got, want)
	}
}

func TestDiscoverPackages_StopsEarlyWhenYieldReturnsFalse(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "ws")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(root, "a", "a.llx"): "// a",
		filepath.Join(root, "b", "b.llx"): "// b",
	})

	count := 0
	for range DiscoverPackages(fs, root) {
		count++
		break
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 - the walk must stop once yield returns false", count)
	}
}
