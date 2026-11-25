package messagecontent

import "testing"

func TestUserMarkdownLineBreaks(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"说明\nhttps://example.com/a\nhttps://example.com/b", "说明  \nhttps://example.com/a  \nhttps://example.com/b"},
		{"one\r\ntwo", "one  \r\ntwo"},
		{"one  \ntwo\n\nthree", "one  \ntwo\n\nthree"},
		{"```go\na\nb\n```", "```go\na\nb\n```"},
		{"| A | B |\n|---|---|\n| 1 | 2 |", "| A | B |\n|---|---|\n| 1 | 2 |"},
		{"[^1]: foot\n    note\n\nmain[^1]\nbody", "[^1]: foot  \n    note\n\nmain[^1]  \nbody"},
	} {
		if got := UserMarkdown(tc.source); got != tc.want {
			t.Errorf("source %q: got %q, want %q", tc.source, got, tc.want)
		}
	}
}
