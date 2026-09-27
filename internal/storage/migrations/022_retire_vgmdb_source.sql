-- VGMdb album enrichment has been retired: the unofficial vgmdb.info JSON
-- mirror is unreliable and vgmdb.net blocks automated clients. Disable the
-- source so it no longer triggers automatic runs. The row itself and any
-- previously stored VGMdb album profiles/credits are kept for history.
UPDATE metadata_source_settings SET enabled = 0, auto_match = 0 WHERE source = 'vgmdb';
