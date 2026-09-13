package cli

import "testing"

// The linker stamp has to win over the module version the toolchain records,
// and a build told nothing at all has to fall back to that module version.
// Getting this backwards is invisible in a normal build — both sources usually
// agree — and it is what made the "-X" flags dead before 0.4.1.
func TestResolveVersionPrefersTheStampedValue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stamped string
		build   string
		want    string
	}{
		{name: "stamp wins over the build info", stamped: "v1.2.3", build: "v0.0.1", want: "v1.2.3"},
		{name: "stamp alone is reported", stamped: "v1.2.3", build: "", want: "v1.2.3"},
		{name: "build info fills in for an unstamped build", stamped: unstampedVersion, build: "v0.4.1", want: "v0.4.1"},
		{name: "neither source leaves the placeholder", stamped: unstampedVersion, build: "", want: unstampedVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveVersion(tc.stamped, tc.build); got != tc.want {
				t.Fatalf("resolveVersion(%q, %q) = %q, want %q", tc.stamped, tc.build, got, tc.want)
			}
		})
	}
}

// Only a usable module version may come out of here. Both placeholders would be
// read as a stamp by resolveVersion and would shadow a real one: "dev" directly,
// and "(devel)" — what the toolchain records for a build with no version — by
// being reported to the user as if it were the version.
func TestVersionFromBuildReportsNoPlaceholder(t *testing.T) {
	got := versionFromBuild()
	for _, placeholder := range []string{unstampedVersion, "(devel)"} {
		if got == placeholder {
			t.Fatalf("versionFromBuild() = %q, want an empty string or a module version", got)
		}
	}
}
