package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/lux032/032music-server/internal/metadata"
)

// creatorNamesForImport remembers a conversion only for the same source tags.
// Changed/new tags follow normal parsing unless the user explicitly converts again.
func creatorNamesForImport(ctx context.Context, tx *sql.Tx, trackID int64, role, fallback string, raw map[string][]string, legacy bool) ([]string, error) {
	normal := metadata.CreatorNames(raw, role, fallback, false)
	converted := metadata.CreatorNames(raw, role, fallback, true)
	// Fingerprint this role's interpretation only. Unrelated album/lyric tags
	// must not undo a conversion; a new explicit list must invalidate it.
	source, _ := json.Marshal(struct{ Normal, Legacy []string }{normal, converted})
	hash := fmt.Sprintf("%x", sha256.Sum256(source))
	if legacy {
		if !reflect.DeepEqual(normal, converted) {
			names, _ := json.Marshal(converted)
			_, err := tx.ExecContext(ctx, `INSERT INTO legacy_creator_conversions(track_id,role,source_hash,names_json) VALUES(?,?,?,?) ON CONFLICT(track_id,role) DO UPDATE SET source_hash=excluded.source_hash,names_json=excluded.names_json,converted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, trackID, role, hash, string(names))
			return converted, err
		}
	}
	var savedHash, names string
	err := tx.QueryRowContext(ctx, `SELECT source_hash,names_json FROM legacy_creator_conversions WHERE track_id=? AND role=?`, trackID, role).Scan(&savedHash, &names)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && hash == savedHash {
		var result []string
		if err = json.Unmarshal([]byte(names), &result); err != nil {
			return nil, err
		}
		return result, nil
	}
	if err == nil {
		if _, err = tx.ExecContext(ctx, `DELETE FROM legacy_creator_conversions WHERE track_id=? AND role=?`, trackID, role); err != nil {
			return nil, err
		}
	}
	return normal, nil
}
