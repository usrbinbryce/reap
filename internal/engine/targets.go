package engine

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/usrbinbryce/reap/internal/core"
)

func ReadTargets(path string) ([]string, error) {
	path = core.ExpandHome(path)
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("Couldn't find the list file %s.", path)
		}
		return nil, err
	}
	defer f.Close()

	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), bom))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key := strings.ToLower(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("The list file %s has no targets in it.", path)
	}
	return out, nil
}

// bom is the byte order mark Windows editors like to put at the top of a file.
const bom = string(rune(0xFEFF))
