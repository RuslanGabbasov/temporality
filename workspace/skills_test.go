package workspace

import "testing"

func TestNextFreePatch(t *testing.T) {
	cases := []struct {
		name    string
		current string
		taken   map[string]bool
		want    string
	}{
		{"simple bump", "1.2.0", map[string]bool{}, "1.2.1"},
		{"skips occupied patch", "1.2.0", map[string]bool{"1.2.1": true}, "1.2.2"},
		{"skips a draft chain", "1.2.0", map[string]bool{"1.2.1": true, "1.2.2": true, "1.2.3": true}, "1.2.4"},
		{"empty current starts at 0.1", "", map[string]bool{}, "0.1.1"},
		{"unparsable current", "latest", map[string]bool{}, "0.1.1"},
		{"prerelease suffix ignored", "1.2.0-rc.1", map[string]bool{}, "1.2.1"},
	}
	for _, tc := range cases {
		if got := nextFreePatch(tc.current, tc.taken); got != tc.want {
			t.Errorf("%s: nextFreePatch(%q) = %q, want %q", tc.name, tc.current, got, tc.want)
		}
	}
}
