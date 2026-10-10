package upnp

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// Item is one queue entry: the URI the renderer fetches plus the DIDL-Lite
// metadata it shows (title, artist, album, cover).
type Item struct {
	URI        string
	MIME       string
	Title      string
	Artist     string
	Album      string
	ArtURI     string
	DurationMs int64
	// ID is an opaque stable item id ("032-track-<id>").
	ID string
}

// DIDL renders the item as a single-item DIDL-Lite document.
func (it Item) DIDL() string {
	mime := it.MIME
	if mime == "" {
		mime = "audio/mpeg"
	}
	id := it.ID
	if id == "" {
		id = "032-item"
	}
	var b strings.Builder
	b.WriteString(`<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/" xmlns:r="urn:schemas-rinconnetworks-com:metadata-1-0/" xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/">`)
	b.WriteString(`<item id="` + xmlEscape(id) + `" parentID="032" restricted="true">`)
	b.WriteString(`<dc:title>` + xmlEscape(orDefault(it.Title, "Unknown")) + `</dc:title>`)
	if it.Artist != "" {
		b.WriteString(`<dc:creator>` + xmlEscape(it.Artist) + `</dc:creator>`)
		b.WriteString(`<upnp:artist>` + xmlEscape(it.Artist) + `</upnp:artist>`)
	}
	if it.Album != "" {
		b.WriteString(`<upnp:album>` + xmlEscape(it.Album) + `</upnp:album>`)
	}
	if it.ArtURI != "" {
		b.WriteString(`<upnp:albumArtURI>` + xmlEscape(it.ArtURI) + `</upnp:albumArtURI>`)
	}
	b.WriteString(`<upnp:class>object.item.audioItem.musicTrack</upnp:class>`)
	b.WriteString(`<res protocolInfo="http-get:*:` + xmlEscape(mime) + `:*"`)
	if it.DurationMs > 0 {
		b.WriteString(` duration="` + formatDuration(it.DurationMs) + `.000"`)
	}
	b.WriteString(`>` + xmlEscape(it.URI) + `</res>`)
	b.WriteString(`</item></DIDL-Lite>`)
	return b.String()
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// didlEntry is what a Browse/TrackMetaData result tells us about an item.
type didlEntry struct {
	Title string
	URI   string
}

func parseDIDL(doc string) []didlEntry {
	var parsed struct {
		Items []struct {
			Title string `xml:"title"`
			Res   []struct {
				Value string `xml:",chardata"`
			} `xml:"res"`
		} `xml:"item"`
	}
	if strings.TrimSpace(doc) == "" || xml.Unmarshal([]byte(doc), &parsed) != nil {
		return nil
	}
	entries := make([]didlEntry, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		entry := didlEntry{Title: strings.TrimSpace(item.Title)}
		if len(item.Res) > 0 {
			entry.URI = strings.TrimSpace(item.Res[0].Value)
		}
		entries = append(entries, entry)
	}
	return entries
}

func itoa(n int) string { return strconv.Itoa(n) }
