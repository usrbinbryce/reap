// Package config keeps the last job's settings and credentials in
// <user config dir>/reap/config.json (e.g. ~/.config/reap/config.json).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/engine"
)

type Config struct {
	Last      Job               `json:"last"`
	Secrets   map[string]string `json:"secrets,omitempty"`
	UserAgent string            `json:"user_agent,omitempty"`

	path string
}

// Job mirrors engine.Job, plus option values remembered per source.
type Job struct {
	Source  string                       `json:"source"`
	Target  string                       `json:"target"`
	List    string                       `json:"list,omitempty"` // a .txt of targets, one per line; used instead of Target
	OutDir  string                       `json:"out_dir"`
	Folder  string                       `json:"folder"`
	Name    string                       `json:"name"`
	Prefix  string                       `json:"prefix"`
	Types   []string                     `json:"types"`
	Limit   int                          `json:"limit"`
	Workers int                          `json:"workers"`
	Dedupe  string                       `json:"dedupe"`
	Options map[string]map[string]string `json:"options,omitempty"`
}

func Path() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "reap", "config.json")
}

func Default() *Config {
	out := "reap"
	if h, err := os.UserHomeDir(); err == nil {
		out = filepath.Join(h, "reap")
	}
	return &Config{
		path: Path(),
		Last: Job{
			Source:  "reddit",
			OutDir:  out,
			Folder:  engine.DefaultFolder,
			Name:    engine.DefaultName,
			Types:   []string{"jpg", "png", "webp", "gif", "mp4"},
			Limit:   5000,
			Workers: 6,
			Dedupe:  string(engine.DedupeFlag),
			Options: map[string]map[string]string{},
		},
		Secrets: map[string]string{},
	}
}

// Load reads the config file. A missing file is not an error; a broken one
// returns the defaults along with the error.
func Load() (*Config, error) {
	c := Default()
	b, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, c); err != nil {
		return Default(), fmt.Errorf("%s: %w", c.path, err)
	}
	d := Default().Last
	if c.Last.Source == "" {
		c.Last.Source = d.Source
	}
	if c.Last.OutDir == "" {
		c.Last.OutDir = d.OutDir
	}
	if len(c.Last.Types) == 0 {
		c.Last.Types = d.Types
	}
	if c.Last.Workers <= 0 {
		c.Last.Workers = d.Workers
	}
	if c.Last.Options == nil {
		c.Last.Options = map[string]map[string]string{}
	}
	if c.Secrets == nil {
		c.Secrets = map[string]string{}
	}
	return c, nil
}

// Save writes the config with owner-only permissions, since it may hold secrets.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) File() string { return c.path }

// Secret returns REAP_<KEY> from the environment if set, else the config
// value. "reddit.client_id" reads REAP_REDDIT_CLIENT_ID.
func (c *Config) Secret(key string) string {
	env := "REAP_" + strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(key))
	if v := os.Getenv(env); v != "" {
		return v
	}
	return c.Secrets[key]
}

// Job builds an engine job from the remembered settings.
func (c *Config) Job() engine.Job {
	l := c.Last
	opts := map[string]string{}
	for k, v := range l.Options[l.Source] {
		opts[k] = v
	}
	return engine.Job{
		Source: l.Source, Target: l.Target, Options: opts,
		OutDir: l.OutDir, Folder: l.Folder, Name: l.Name, Prefix: l.Prefix,
		Types: append([]string(nil), l.Types...), Limit: l.Limit, Workers: l.Workers,
		Dedupe: engine.Dedupe(l.Dedupe),
	}
}

// Remember stores j as the last job, keeping other sources' options and the
// list file, which isn't part of a job.
func (c *Config) Remember(j engine.Job) {
	opts := c.Last.Options
	if opts == nil {
		opts = map[string]map[string]string{}
	}
	if len(j.Options) > 0 {
		m := map[string]string{}
		for k, v := range j.Options {
			m[k] = v
		}
		opts[j.Source] = m
	}
	c.Last = Job{
		Source: j.Source, Target: j.Target, OutDir: j.OutDir, Folder: j.Folder, Name: j.Name,
		Prefix: j.Prefix, Types: j.Types, Limit: j.Limit, Workers: j.Workers,
		Dedupe: string(j.Dedupe), Options: opts, List: c.Last.List,
	}
}

// SourceOptions returns remembered options for one source.
func (c *Config) SourceOptions(source string) map[string]string {
	s, ok := core.GetSource(source)
	if !ok {
		return map[string]string{}
	}
	return core.WithDefaults(s.Info(), c.Last.Options[source])
}
