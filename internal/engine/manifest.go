package engine

import (
	"encoding/json"
	"os"
	"sync"
)

// a record is one line of <dataset>/.reap/manifest.jsonl, and
// it represents one single file saved. It's the metadata of the
// dataset and can be loaded with something like pandas
type record struct {
	Path       string         `json:"path"`
	SHA256     string         `json:"sha256"`
	PHash      string         `json:"phash,omitempty"`
	NearDupOf  string         `json:"near_dup_of,omitempty"`
	Bytes      int64          `json:"bytes"`
	Ext        string         `json:"ext"`
	Kind       string         `json:"kind"`
	Source     string         `json:"source"`
	Collection string         `json:"collection,omitempty"`
	ID         string         `json:"id"`
	PageURL    string         `json:"page_url,omitempty"`
	MediaURL   string         `json:"media_url"`
	Variant    string         `json:"variant,omitempty"`
	Title      string         `json:"title,omitempty"`
	Author     string         `json:"author,omitempty"`
	Created    string         `json:"created,omitempty"`
	NSFW       bool           `json:"nsfw"`
	Score      int            `json:"score"`
	Permalink  string         `json:"permalink,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
	Fetched    string         `json:"fetched"`
}

type manifest struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

func openManifest(path string) (*manifest, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return &manifest{f: f, enc: enc}, nil
}

func (m *manifest) write(r record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.enc.Encode(r)
}

func (m *manifest) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.f.Close()
}
