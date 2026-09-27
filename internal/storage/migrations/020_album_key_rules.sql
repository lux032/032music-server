-- Album merge/delete survive rescans. Albums are rebuilt from files keyed by
-- (library_id, grouping_key); a rule for that key tells ImportTrack either to
-- attach the files to another album ('merge') or to skip them ('delete').
-- Files on disk are never touched. Deleting the merge target drops its rules,
-- so the source files regroup normally on the next scan.
CREATE TABLE album_key_rules (
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    grouping_key TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('merge', 'delete')),
    target_album_id INTEGER REFERENCES albums(id) ON DELETE CASCADE,
    source_title TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (library_id, grouping_key),
    CHECK ((action = 'merge') = (target_album_id IS NOT NULL))
) STRICT;

CREATE INDEX idx_album_key_rules_target ON album_key_rules(target_album_id);
