package reddit

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
)

// parseCookie accepts the REAP_REDDIT_COOKIE value in any of three forms:
//
//   - just the reddit_session value, copied from the browser's dev tools
//   - a whole Cookie header, e.g. "reddit_session=...; token_v2=..."
//   - a path to a cookies.txt file (Netscape format), as exported by
//     browser extensions such as "Get cookies.txt LOCALLY"
//
// It returns a Cookie header value with over18=1 added so NSFW subreddits
// skip their interstitial.
func parseCookie(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	v = strings.TrimSpace(strings.TrimPrefix(v, "Cookie:"))

	if path := core.ExpandHome(v); looksLikePath(v) {
		pairs, err := readCookiesTxt(path)
		if err != nil {
			return "", err
		}
		v = strings.Join(pairs, "; ")
	} else if !strings.Contains(v, "=") {
		v = "reddit_session=" + v
	}

	if !strings.Contains(v, "reddit_session=") && !strings.Contains(v, "token_v2=") {
		return "", errors.New("it has no reddit_session cookie; copy that one from reddit.com while signed in")
	}
	if !strings.Contains(v, "over18=") {
		v += "; over18=1"
	}
	return v, nil
}

func looksLikePath(v string) bool {
	if strings.ContainsAny(v, "=;") {
		return false
	}
	if strings.HasPrefix(v, "~") || strings.HasPrefix(v, "/") || strings.HasPrefix(v, ".") || filepath.VolumeName(v) != "" {
		return true
	}
	_, err := os.Stat(v)
	return err == nil
}

func readCookiesTxt(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("can't read the cookies file: %w", err)
	}
	defer f.Close()

	now := time.Now().Unix()
	byName := map[string]string{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// curl and most exporters mark HttpOnly cookies with this prefix.
		if strings.HasPrefix(line, "#HttpOnly_") {
			line = strings.TrimPrefix(line, "#HttpOnly_")
		} else if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain := strings.ToLower(strings.TrimPrefix(fields[0], "."))
		if domain != "reddit.com" && !strings.HasSuffix(domain, ".reddit.com") {
			continue
		}
		if exp, err := strconv.ParseInt(fields[4], 10, 64); err == nil && exp > 0 && exp < now {
			continue
		}
		name, value := fields[5], strings.TrimSpace(fields[6])
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		byName[name] = value
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("can't read the cookies file: %w", err)
	}
	if len(order) == 0 {
		return nil, errors.New("the cookies file has no unexpired reddit.com cookies")
	}
	pairs := make([]string, 0, len(order))
	for _, n := range order {
		pairs = append(pairs, n+"="+byName[n])
	}
	return pairs, nil
}
