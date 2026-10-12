package core

import (
	"os"
	"path/filepath"
	"strings"
)

// Turn leading ~ into home dir
func ExpandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `-\`) {
		if h, err := os.UserHomeDir(); err != nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}
