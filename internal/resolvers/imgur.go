// not even sure how effective or worthwhile making an imgur resolver is
// because they nuke anyone from their API who doesn't given them their
// full information. regardless I'm adding it for future resolvers
// to copy.
package imgur

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
	"github.com/usrbinbryce/reap/internal/httpx"
)

func init() { core.RegisterResolver(&resolver{}) }

var apiBase = "https://api.imgur.com"

type resolver struct{}

func (r *resolver) Name() string { return "imgur" }

func (*resolver) Match(u *url.URL) bool { return core.HostIs(u, "imgur.com", "imgur.io") }

func (*resolver) Rates() map[string]time.Duration {
	return map[string]time.Duration{
		"i.imgur.com":   250 * time.Millisecond,
		"api.imgur.com": 500 * time.Millisecond,
	}
}

func (*resolver) Resolve(ctx context.Context, u *url.URL, env *core.Env) ([]core.Media, error) {
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) >= 2 {
		switch segs[0] {
		case "a", "gallery", "t":
			return album(ctx, env, slugID(segs[len(segs)-1]))
		}
	}
	last := segs[len(segs)-1]
	dotExt := path.Ext(last)
	id := stripThumb(strings.TrimSuffix(last, dotExt))
	ext := core.NormalizeExt(dotExt)
	if id == "" || id == "removed" {
		return nil, core.Gone("Imgur has removed it")
	}
	switch ext {
	case "gifv", "mp4", "webm":
		return []core.Media{{URL: "https://i.imgur.com/" + id + ".mp4", Ext: "mp4", Kind: core.KindAnimated}}, nil
	case "":
		if cid := env.Secret("imgur.client_id"); cid != "" {
			ms, err := single(ctx, env, cid, id)
			if err == nil || errors.Is(err, core.ErrGone) {
				return ms, err
			}
		}
		// Imgur serves the real file type at .jpg anmd we can sniff it
		return []core.Media{{URL: "https://i.imgur.com/" + id + ".jpg", Kind: core.KindImage}}, nil
	default:
		return []core.Media{{URL: "https://i.imgur.com/" + id + "." + ext, Ext: ext, Kind: core.KindOfExt(ext)}}, nil
	}
}

type apiImage struct {
	Type     string `json:"type"`
	Link     string `json:"link"`
	MP4      string `json:"mp4"`
	Animated bool   `json:"animated"`
}

func (im apiImage) media() core.Media {
	if im.Animated && im.MP4 != "" {
		m := core.Media{URL: im.MP4, Ext: "mp4", Kind: core.KindAnimated}
		if core.ExtFromURL(im.Link) == "gif" {
			m.Alternates = []core.Media{{URL: im.Link, Ext: "gif", Kind: core.KindAnimated}}
		}
		return m
	}
	ext := core.ExtFromMIME(im.Type)
	if ext == "" {
		ext = core.ExtFromURL(im.Link)
	}
	return core.Media{URL: im.Link, Ext: ext, Kind: core.KindOfExt(ext)}
}

func album(ctx context.Context, env *core.Env, id string) ([]core.Media, error) {
	cid := env.Secret("imgur.client_id")
	if cid == "" {
		return nil, core.NeedsSetup("Imgur albums need an Imgur client ID in REAP_IMGUR_CLIENT_ID")
	}
	var res struct {
		Data []apiImage `json:"data"`
	}
	err := env.HTTP.GetJSON(ctx, apiBase+"/3/album/"+id+"/images", header(cid), &res)
	if httpx.StatusCode(err) == http.StatusNotFound {
		// /gallery/<id> can be a single image rather than an album
		return []core.Media{{URL: "https://i.imgur.com/" + id + ".jpg", Kind: core.KindImage}}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(res.Data) == 0 {
		return nil, core.Gone("the Imgur album is empty")
	}
	out := make([]core.Media, 0, len(res.Data))
	for _, im := range res.Data {
		if im.Link != "" {
			out = append(out, im.media())
		}
	}
	return out, nil
}

func single(ctx context.Context, env *core.Env, cid, id string) ([]core.Media, error) {
	var res struct {
		Data apiImage `json:"data"`
	}
	err := env.HTTP.GetJSON(ctx, apiBase+"/3/image/"+id, header(cid), &res)
	switch httpx.StatusCode(err) {
	case http.StatusNotFound:
		return nil, core.Gone("Imgur has removed it")
	case 0:
		if err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	if res.Data.Link == "" {
		return nil, core.Gone("Imgur has removed it")
	}
	return []core.Media{res.Data.media()}, nil
}

func header(cid string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Client-ID "+cid)
	return h
}

// stripThumb turns a thumbnail id like abc1234m into the original abc1234
func stripThumb(id string) string {
	if len(id) == 8 && strings.ContainsRune("sbtmlhgr", rune(id[7])) {
		return id[:7]
	}
	return id
}

// slugID takes the id from new-style links like /gallery/some-title-AbC1234
func slugID(s string) string {
	if i := strings.LastIndexByte(s, '-'); i >= 0 {
		return s[i+1:]
	}
	return s
}
