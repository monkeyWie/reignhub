// Package filelink interprets message references without opening their targets.
package filelink

import (
	"net/url"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"
)

var lineSuffix = regexp.MustCompile(`:[0-9]+(?::[0-9]+)?$`)
var lineFragment = regexp.MustCompile(`^L[0-9]+(?:C[0-9]+)?(?:-L?[0-9]+(?:C[0-9]+)?)?$`)

func drivePath(p string) bool {
	return len(p) > 2 && (p[0] >= 'a' && p[0] <= 'z' || p[0] >= 'A' && p[0] <= 'Z') && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// Path returns a decoded file reference and whether it specifies a source line.
// Resolve relative paths only on the device that owns the message's session.
func Path(href string) (path string, location bool) {
	base, fragment, _ := strings.Cut(href, "#")
	location = lineSuffix.MatchString(base) || lineFragment.MatchString(fragment)
	base = lineSuffix.ReplaceAllString(base, "")
	if drivePath(base) {
		return base, location
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "" && u.Scheme != "file" || u.Host != "" && u.Host != "localhost" || u.Path == "" || u.RawQuery != "" {
		return "", false
	}
	path = u.Path
	if len(path) > 1 && path[0] == '/' && drivePath(path[1:]) {
		path = path[1:]
	}
	return path, location
}

// SourceLocation formats a reference using the execution device's path syntax.
// It only joins strings and never resolves a path on the controller filesystem.
func SourceLocation(href, cwd string) (label, hint string) {
	p, location := Path(href)
	if !location {
		return "", ""
	}
	base, fragment, _ := strings.Cut(href, "#")
	position := strings.TrimPrefix(lineSuffix.FindString(base), ":")
	if lineFragment.MatchString(fragment) {
		position = strings.NewReplacer("L", "", "C", ":").Replace(fragment)
	}
	suffix := " (line " + position + ")"
	windows := drivePath(p) || strings.HasPrefix(p, `\\`) || !strings.HasPrefix(p, "/") && (drivePath(cwd) || strings.HasPrefix(cwd, `\\`))
	if windows {
		p = strings.ReplaceAll(p, `\`, "/")
		cwd = strings.ReplaceAll(cwd, `\`, "/")
	}
	label = pathpkg.Base(p) + suffix
	if !drivePath(p) && !strings.HasPrefix(p, "/") && (drivePath(cwd) || strings.HasPrefix(cwd, "/")) {
		unc := strings.HasPrefix(cwd, "//")
		p = pathpkg.Join(cwd, p)
		if unc {
			p = "/" + p
		}
	}
	if windows {
		p = strings.ReplaceAll(p, "/", `\`)
	}
	return label, p + suffix
}

func Resolve(href, cwd string) string {
	path, _ := Path(href)
	if path == "" {
		return ""
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) {
		if drivePath(path) || !filepath.IsAbs(cwd) {
			return ""
		}
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}
