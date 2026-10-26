package models

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Logo is what a template or project shows as its logo, in one of three forms: an icon id from
// a set the dashboard bundles (logos:postgresql), an http(s) link, or the path of a PNG, WebP or
// SVG file relative to the directory of the YAML file that names it (logo.svg).
type Logo string

var logoIconID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*:[a-z0-9]+(-[a-z0-9]+)*$`)

var logoFileExtensions = []string{".svg", ".png", ".webp"}

func (l Logo) IsLink() bool {
	return strings.HasPrefix(string(l), "https://") || strings.HasPrefix(string(l), "http://")
}

func (l Logo) IsIcon() bool {
	return logoIconID.MatchString(string(l))
}

// File returns the path the logo names when it is a file.
func (l Logo) File() (string, bool) {
	if l == "" || l.IsLink() || strings.Contains(string(l), ":") {
		return "", false
	}
	return string(l), true
}

func (l Logo) Validate() error {
	if l == "" || l.IsIcon() {
		return nil
	}
	if l.IsLink() {
		if u, err := url.Parse(string(l)); err != nil || u.Host == "" {
			return fmt.Errorf("logo %q is not a valid link", l)
		}
		return nil
	}
	file, ok := l.File()
	if !ok {
		return fmt.Errorf("logo %q must be an icon id such as logos:postgresql, an http(s) link, or an image file such as logo.svg", l)
	}
	if path.IsAbs(file) || path.Clean(file) != file || strings.HasPrefix(file, "../") || strings.Contains(file, "\\") {
		return fmt.Errorf("logo %q must be a file inside the project", l)
	}
	for _, ext := range logoFileExtensions {
		if strings.EqualFold(path.Ext(file), ext) {
			return nil
		}
	}
	return fmt.Errorf("logo %q must be a PNG, WebP or SVG file", l)
}
