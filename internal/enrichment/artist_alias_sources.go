package enrichment

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

var aliasWikidataID = regexp.MustCompile(`^Q[1-9][0-9]*$`)

// LoadArtistAliasSources reuses the metadata source gates and confirmed MBID
// chain; a name search never establishes identity. Locale fields are retained
// here because ordinary historical profiles flattened them to strings.
func (m *Manager) LoadArtistAliasSources(ctx context.Context, mbid string) (storage.ArtistAliasSourceData, error) {
	var out storage.ArtistAliasSourceData
	setting, e := m.store.MetadataSourceSetting(ctx, "musicbrainz")
	if e != nil {
		return out, aliasSourceRetry(e)
	}
	if !setting.Enabled {
		return out, errors.New("请先启用 MusicBrainz 数据来源")
	}
	var v struct {
		ID, Name  string
		Aliases   []struct{ Name, Locale string }
		Relations []struct {
			Type string
			URL  struct{ Resource string }
		}
	}
	endpoint := strings.TrimRight(m.musicBrainzBase, "/") + "/artist/" + url.PathEscape(mbid) + "?inc=aliases+url-rels&fmt=json"
	if e = m.mbRequest(ctx, endpoint, setting, &v); e != nil {
		return out, aliasSourceRetry(e)
	}
	if v.ID != mbid {
		return out, errors.New("MusicBrainz 返回了不同的歌手身份")
	}
	out.IdentityNames = append(out.IdentityNames, v.Name)
	page := "https://musicbrainz.org/artist/" + url.PathEscape(mbid)
	for _, a := range v.Aliases {
		out.IdentityNames = append(out.IdentityNames, a.Name)
		if strings.HasPrefix(strings.ToLower(a.Locale), "zh") {
			out.Names = append(out.Names, storage.ArtistAliasSourceName{Value: a.Name, Language: a.Locale, URL: page, Source: "musicbrainz"})
		}
	}
	var qid string
	for _, rel := range v.Relations {
		u, err := url.Parse(rel.URL.Resource)
		if err != nil || u.Scheme != "https" || u.User != nil {
			continue
		}
		if rel.Type == "official homepage" {
			out.OfficialURLs = append(out.OfficialURLs, u.String())
		}
		if rel.Type == "wikidata" && (u.Hostname() == "www.wikidata.org" || u.Hostname() == "wikidata.org") {
			id := strings.TrimPrefix(u.Path, "/wiki/")
			if aliasWikidataID.MatchString(id) {
				qid = id
			}
		}
	}
	if qid == "" {
		return out, nil
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.wikidata.org/wiki/Special:EntityData/"+qid+".json", nil)
	if e != nil {
		return out, aliasSourceRetry(e)
	}
	req.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	type text struct{ Language, Value string }
	var doc struct {
		Entities map[string]struct {
			Labels  map[string]text
			Aliases map[string][]text
		}
	}
	if e = m.doSourceJSON(m.client, req, "wikidata", &doc); e != nil {
		// An available explicit MusicBrainz Chinese name is still useful when the
		// secondary source is unavailable. Otherwise let the task record failure.
		if len(out.Names) > 0 {
			return out, nil
		}
		return out, aliasSourceRetry(e)
	}
	entity, ok := doc.Entities[qid]
	if !ok {
		return out, errors.New("Wikidata 未返回绑定的歌手实体")
	}
	for _, language := range []string{"zh-hans", "zh-cn", "zh", "zh-hant", "zh-tw", "zh-hk", "zh-sg"} {
		list := append([]text{entity.Labels[language]}, entity.Aliases[language]...)
		for _, n := range list {
			if n.Value != "" {
				out.Names = append(out.Names, storage.ArtistAliasSourceName{Value: n.Value, Language: language, URL: "https://www.wikidata.org/wiki/" + qid, Source: "wikidata"})
			}
		}
	}
	return out, nil
}

func aliasSourceRetry(err error) error {
	if rate := asRateLimited(err); rate != nil {
		return &storage.AliasSourceRetry{After: rate.RetryAfter}
	}
	return err
}
