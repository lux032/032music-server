package enrichment

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/lux032/032music-server/internal/appmeta"
	"github.com/lux032/032music-server/internal/storage"
)

// defaultContactURL is the User-Agent contact fallback (see appmeta.RepoURL).
const defaultContactURL = appmeta.RepoURL

// userAgent builds "<ApplicationName>/<ApplicationVersion> (<Contact>)" with
// reachable defaults for every empty part. Whitespace inside the name and
// version is replaced with "-" (the default "032 Music Server" becomes
// "032-Music-Server"); control characters inside the contact become spaces so
// a stored contact can never inject extra header lines.
func userAgent(setting storage.MetadataSourceSetting) string {
	name := strings.Join(strings.Fields(setting.ApplicationName), "-")
	if name == "" {
		name = "032-Music-Server"
	}
	version := strings.Join(strings.Fields(setting.ApplicationVersion), "-")
	if version == "" {
		version = "dev"
	}
	contact := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, setting.Contact))
	if contact == "" {
		contact = defaultContactURL
	}
	return fmt.Sprintf("%s/%s (%s)", name, version, contact)
}

// metadataUserAgent builds the UA for requests that have no source setting of
// their own (Wikidata, Wikipedia, Spotify, image downloads). It uses the
// MusicBrainz source settings because the admin page collects the contact
// identity there, and MusicBrainz is the identity source these auxiliary
// requests resolve relations for. Unreadable settings fall back to defaults.
func (m *Manager) metadataUserAgent(ctx context.Context) string {
	setting, err := m.store.MetadataSourceSetting(ctx, "musicbrainz")
	if err != nil {
		return userAgent(storage.MetadataSourceSetting{})
	}
	return userAgent(setting)
}
