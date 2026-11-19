package storage

import (
	"encoding/json"

	"github.com/lux032/032music-server/internal/metadata"
)

// A join phrase belongs to an exact, ordered list of artist identities.
// Display names may change without invalidating the source credit.
type artistCredit struct {
	IDs    []int64  `json:"ids"`
	Before []string `json:"before"`
}

func scannedArtistCredit(raw map[string][]string, names []string, ids []int64, album bool) any {
	if len(names) < 2 || len(names) != len(ids) {
		return nil
	}
	before := metadata.ArtistJoinPhrases(raw, names, album)
	if before == nil {
		return nil
	}
	// The default comma-space display needs no stored credit.
	for _, phrase := range before[1:] {
		if phrase != ", " {
			encoded, _ := json.Marshal(artistCredit{IDs: ids, Before: before})
			return string(encoded)
		}
	}
	return nil
}

func matchArtistCredit(encoded string, ids []int64) []string {
	if encoded == "" {
		return nil
	}
	var credit artistCredit
	if json.Unmarshal([]byte(encoded), &credit) != nil || len(credit.Before) != len(ids) || len(credit.IDs) != len(ids) {
		return nil
	}
	for i, id := range ids {
		if credit.IDs[i] != id {
			return nil
		}
	}
	return credit.Before
}

// These subqueries use the same identity check as the clickable artist refs.
// The comma default also applies to historical rows without a source credit.
const creditDisplayColumnsSQL = `
	CASE WHEN artist_credit IS NOT NULL AND count(merged_into_artist_id)=0
		AND json_group_array(id ORDER BY position,id)=json_extract(artist_credit,'$.ids')
	THEN group_concat((SELECT json_extract(artist_credit,'$.before['||key||']')
		FROM json_each(artist_credit,'$.ids') WHERE value=credit_artist.id)||name,'' ORDER BY position,id)
	ELSE group_concat(name,', ' ORDER BY position,id) END`

const albumCreditDisplaySQL = `(SELECT ` + creditDisplayColumnsSQL + ` FROM (
	SELECT ar.id,aa.position,COALESCE(ar.user_display_name,ar.display_name) AS name,a.artist_credit AS artist_credit,ar.merged_into_artist_id
	FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id
	WHERE aa.album_id=a.id) AS credit_artist)`

const trackCreditDisplaySQL = `(SELECT ` + creditDisplayColumnsSQL + ` FROM (
	SELECT ar.id,ta.position,COALESCE(ar.user_display_name,ar.display_name) AS name,t.artist_credit AS artist_credit,ar.merged_into_artist_id
	FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id
	WHERE ta.track_id=t.id AND ta.role='primary') AS credit_artist)`
