-- Scheduled discovery refresh and how long fetched artist/work catalogs are
-- reused by an ordinary (incremental) refresh. run_hour is a UTC hour; the
-- page converts it to and from the browser's local time.
CREATE TABLE discovery_schedule (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 interval_days INTEGER NOT NULL DEFAULT 7 CHECK(interval_days BETWEEN 1 AND 30),
 run_hour INTEGER NOT NULL DEFAULT 20 CHECK(run_hour BETWEEN 0 AND 23),
 cache_days INTEGER NOT NULL DEFAULT 7 CHECK(cache_days BETWEEN 1 AND 30),
 last_auto_at TEXT
) STRICT;
INSERT INTO discovery_schedule(id) VALUES(1);
