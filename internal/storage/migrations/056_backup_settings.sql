CREATE TABLE backup_settings (
 id INTEGER PRIMARY KEY CHECK (id=1),
 enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
 time TEXT NOT NULL,
 keep_daily INTEGER NOT NULL CHECK (keep_daily BETWEEN 1 AND 365),
 keep_preupgrade INTEGER NOT NULL CHECK (keep_preupgrade BETWEEN 1 AND 365)
) STRICT;
