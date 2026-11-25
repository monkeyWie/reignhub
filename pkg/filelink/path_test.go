package filelink

import "testing"

func TestSourceLocation(t *testing.T) {
	for _, tc := range []struct{ href, cwd, label, hint string }{
		{"/repo/src/main.go:156", "", "main.go (line 156)", "/repo/src/main.go (line 156)"},
		{"src/main.go#L3C2-L8C5", `C:\repo`, "main.go (line 3:2-8:5)", `C:\repo\src\main.go (line 3:2-8:5)`},
		{"file:///C:/src/a%20b.go:3:4", "", "a b.go (line 3:4)", `C:\src\a b.go (line 3:4)`},
		{"../main.go#L4", "/repo/src", "main.go (line 4)", "/repo/main.go (line 4)"},
		{"https://example.org/main.go#L3", "/repo", "", ""},
		{"#L3", "/repo", "", ""},
		{"archive.zip", "/repo", "", ""},
	} {
		if label, hint := SourceLocation(tc.href, tc.cwd); label != tc.label || hint != tc.hint {
			t.Errorf("%q in %q: got %q, %q", tc.href, tc.cwd, label, hint)
		}
	}
}
