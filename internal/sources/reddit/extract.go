package reddit

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/usrbinbryce/reap/internal/core"
)

type post struct {
	ID            string               `json:"id"`
	Title         string               `json:"title"`
	Author        string               `json:"author"`
	Subreddit     string               `json:"subreddit"`
	Permalink     string               `json:"permalink"`
	URL           string               `json:"url"`
	URLOverridden string               `json:"url_overriden_by_dest"`
	Domain        string               `json:"domain"`
	Created       float64              `json:"created_utc"`
	Over18        bool                 `json:"over_18"`
	Score         int                  `json:"score"`
	IsVideo       bool                 `json:"is_video"`
	IsSelf        bool                 `json:"is_self"`
	PostHint      string               `json:"post_hint"`
	LinkFlair     string               `json:"link_flair_text"`
	MediaMetadata map[string]mediaMeta `json:"media_metadata"`
	GalleryData   *struct {
		Items []struct {
			MediaID string `json:"media_id"`
		} `json:"items"`
	} `json:"gallery_data"`
	Preview          *preview `json:"preview"`
	CrosspostParents []post   `json:"crosspost_parent_list"`
}

type mediaMeta struct {
	Status string `json:"status"`
	E      string `json:"e"` // "Image" or "AnimatedImage
	M      string `json:"m"` // MIME type
	S      struct {
		U   string `json:"u"`
		GIF string `json:"gif"`
		MP4 string `json:"mp4"`
	} `json:"s"`
}

type sourceURL struct {
	Source struct {
		URL string `json:"url"`
	} `json:"source"`
}

type preview struct {
	Images []struct {
		sourceURL
		Variants struct {
			GIF *sourceURL `json:"gif"`
			MP4 *sourceURL `json:"mp4"`
		} `json:"variants"`
	} `json:"images"`
	RedditVideoPreview *struct {
		FallbackURL string `json:"fallback_url"`
	} `json:"reddit_video_preview"`
}

var mediaDomains = []string{"imgur.com", "gfycat.com", "giphy.com", "i.redd.it"}

func extract(p *post, previews bool) []core.Item {
	src := p
	if len(p.CrosspostParents) > 0 {
		src = &p.CrosspostParents[0]
	}
	base := core.Item{
		Source:     "reddit",
		Collection: p.Subreddit,
		ID:         src.ID,
		Title:      p.Title,
		Author:     p.Author,
		NSFW:       p.Over18,
		Score:      p.Score,
		Permalink:  "https://reddit.com" + p.Permalink,
		Extra:      map[string]any{"post_id": p.ID, "subreddit": p.Subreddit},
	}
	if p.Created > 0 {
		base.Created = time.Unix(int64(p.Created), 0).UTC()
	}
	if p.LinkFlair != "" {
		base.Extra["flair"] = p.LinkFlair
	}
	if src.ID != p.ID {
		base.Extra["crosspost_of"] = src.ID
	}

	if src.GalleryData != nil && len(src.GalleryData.Items) > 0 {
		ids := make([]string, len(src.GalleryData.Items))
		for i, gi := range src.GalleryData.Items {
			ids[i] = gi.MediaID
		}
		return fromMetadata(base, src.ID, ids, src.MediaMetadata)
	}
	if src.IsVideo || src.Domain == "v.redd.it" {
		return nil
	}
	if src.IsSelf {
		if len(src.MediaMetadata) == 0 {
			return nil
		}
		ids := make([]string, 0, len(src.MediaMetadata))
		for id := range src.MediaMetadata {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return fromMetadata(base, src.ID, ids, src.MediaMetadata)
	}

	link := src.URLOverridden
	if link == "" {
		link = src.URL
	}
	if link == "" {
		return nil
	}
	it := base
	it.URL = link
	if previews && isMediaPost(src) {
		it.Fallbacks = previewURLs(src)
	}
	return []core.Item{it}
}

func fromMetadata(base core.Item, postID string, ids []string, meta map[string]mediaMeta) []core.Item {
	var out []core.Item
	for i, id := range ids {
		mm, ok := meta[id]
		if !ok || (mm.Status != "" && mm.Status != "valid") {
			continue
		}
		it := base
		it.ID = postID + "_" + strconv.Itoa(i+1)
		switch mm.E {
		case "image":
			it.Kind = core.KindImage
			if ext := core.ExtFromMIME(mm.M); ext != "" {
				it.URL = "https://i.redd.it/" + id + "." + ext
			}
			if mm.S.U != "" {
				if it.URL == "" {
					it.URL = mm.S.U
				} else {
					it.Fallbacks = []string{mm.S.U}
				}
			}
		case "AnimatedImage":
			it.Kind = core.KindAnimated
			it.URL = mm.S.GIF
			if it.URL == "" {
				it.URL = mm.S.MP4
			} else if mm.S.MP4 != "" {
				it.Fallbacks = []string{mm.S.MP4}
			}
		default:
			continue
		}
		if it.URL != "" {
			out = append(out, it)
		}
	}
	return out
}

func isMediaPost(p *post) bool {
	if p.PostHint == "image" || p.PostHint == "rich:video" {
		return true
	}

	d := strings.ToLower(p.Domain)

	for _, m := range mediaDomains {
		if d == m || strings.HasSuffix(d, "."+m) {
			return true
		}
	}

	return false
}

func previewURLs(p *post) []string {
	if p.Preview == nil {
		return nil
	}

	var out []string

	if v := p.Preview.RedditVideoPreview; v != nil && v.FallbackURL != "" {
		out = append(out, v.FallbackURL)
	}

	if len(p.Preview.Images) > 0 {
		img := p.Preview.Images[0]
		if v := img.Variants.GIF; v != nil && v.Source.URL != "" {
			out = append(out, v.Source.URL)
		}
		if v := img.Variants.MP4; v != nil && v.Source.URL != "" {
		}
	}

	return nil
}
