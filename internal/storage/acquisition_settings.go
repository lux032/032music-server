package storage

import (
	"context"
	"database/sql"
)

// AcquisitionSettings is safe for presentation: secrets are write-only.
type AcquisitionSettings struct {
	Enabled                                                   bool
	JackettURL, JackettIndexer, QBURL, QBUsername, QBCategory string
	KeySet, HasPassword                                       bool
	UpdatedAt                                                 string
}

// AcquisitionSettingsUpdate contains secrets; never use it as a request DTO.
type AcquisitionSettingsUpdate struct {
	Enabled                                                   bool
	JackettURL, JackettIndexer, QBURL, QBUsername, QBCategory string
	JackettAPIKey                                             string `json:"-"`
	QBPassword                                                string `json:"-"`
	ClearKey, ClearPassword                                   bool
}

// AcquisitionCredentials is only for constructing backend clients, never a view.
// JSON and formatting intentionally omit the secret values.
type AcquisitionCredentials struct {
	JackettAPIKey string `json:"-"`
	QBPassword    string `json:"-"`
}

func (AcquisitionCredentials) String() string { return "[acquisition credentials]" }
func (s *Store) AcquisitionSettings(ctx context.Context) (AcquisitionSettings, error) {
	var v AcquisitionSettings
	e := s.db.QueryRowContext(ctx, `SELECT enabled,jackett_url,jackett_indexer,qb_url,qb_username,qb_category,jackett_api_key<>'',qb_password<>'',updated_at FROM acquisition_settings WHERE id=1`).Scan(&v.Enabled, &v.JackettURL, &v.JackettIndexer, &v.QBURL, &v.QBUsername, &v.QBCategory, &v.KeySet, &v.HasPassword, &v.UpdatedAt)
	return v, e
}
func (s *Store) AcquisitionCredentials(ctx context.Context) (AcquisitionCredentials, error) {
	var v AcquisitionCredentials
	e := s.db.QueryRowContext(ctx, `SELECT jackett_api_key,qb_password FROM acquisition_settings WHERE id=1`).Scan(&v.JackettAPIKey, &v.QBPassword)
	return v, e
}
func (s *Store) UpdateAcquisitionSettings(ctx context.Context, v AcquisitionSettingsUpdate) error {
	if v.JackettIndexer == "" {
		v.JackettIndexer = "jpopsuki"
	}
	_, e := s.db.ExecContext(ctx, `UPDATE acquisition_settings SET enabled=?,jackett_url=?,jackett_indexer=?,qb_url=?,qb_username=?,qb_category=?,jackett_api_key=CASE WHEN ? THEN '' WHEN ?<>'' THEN ? ELSE jackett_api_key END,qb_password=CASE WHEN ? THEN '' WHEN ?<>'' THEN ? ELSE qb_password END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, v.Enabled, v.JackettURL, v.JackettIndexer, v.QBURL, v.QBUsername, v.QBCategory, v.ClearKey, v.JackettAPIKey, v.JackettAPIKey, v.ClearPassword, v.QBPassword, v.QBPassword)
	return e
}
func nullableAcquisitionString(v string) sql.NullString {
	return sql.NullString{String: v, Valid: v != ""}
}

func (AcquisitionSettingsUpdate) String() string   { return "[acquisition settings update]" }
func (AcquisitionSettingsUpdate) GoString() string { return "[acquisition settings update]" }
func (AcquisitionCredentials) GoString() string    { return "[acquisition credentials]" }
