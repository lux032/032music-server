package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// Migration 063 rebuilds credential_overrides to allow admin_totp_secret
// without losing existing overrides, and still rejects unknown keys.
func TestMigration063PreservesCredentialOverrides(t *testing.T) {
	c := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.MigrateUpTo(c, 62); e != nil {
		t.Fatal(e)
	}
	if e = s.UpdateCredentialOverrides(c, map[string]string{CredentialAdminUsername: "boss", CredentialMediaToken: "media-token-0123456789abcdef"}, nil, nil); e != nil {
		t.Fatal(e)
	}
	if e = s.UpdateCredentialOverrides(c, map[string]string{CredentialAdminTOTPSecret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"}, nil, nil); e == nil {
		t.Fatal("version 62 must still reject the TOTP key")
	}
	if e = s.Migrate(c); e != nil {
		t.Fatal(e)
	}
	if e = s.UpdateCredentialOverrides(c, map[string]string{CredentialAdminTOTPSecret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"}, nil, nil); e != nil {
		t.Fatal(e)
	}
	rows, e := s.CredentialOverrides(c)
	if e != nil {
		t.Fatal(e)
	}
	if rows[CredentialAdminUsername] != "boss" || rows[CredentialMediaToken] != "media-token-0123456789abcdef" || rows[CredentialAdminTOTPSecret] == "" || len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	if e = s.UpdateCredentialOverrides(c, map[string]string{"unknown_key": "x"}, nil, nil); e == nil {
		t.Fatal("unknown keys must still be rejected")
	}
}
