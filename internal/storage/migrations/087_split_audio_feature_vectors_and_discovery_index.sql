ALTER TABLE track_audio_features RENAME TO track_audio_features_old;

CREATE TABLE track_audio_features (
    track_id INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    file_fingerprint TEXT NOT NULL,
    analysis_version TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('fast', 'full')),
    duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
    windows INTEGER NOT NULL CHECK (windows >= 0),
    patches INTEGER NOT NULL CHECK (patches >= 0),
    heads_json TEXT NOT NULL,
    musicnn_valence REAL,
    musicnn_arousal REAL,
    valence REAL,
    arousal REAL,
    av_source TEXT CHECK (av_source IS NULL OR av_source IN ('musicnn', 'regression')),
    av_model_id INTEGER REFERENCES audio_av_models(id) ON DELETE SET NULL,
    analyze_ms INTEGER NOT NULL DEFAULT 0 CHECK (analyze_ms >= 0),
    analyzed_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO track_audio_features(
    track_id, file_fingerprint, analysis_version, mode, duration_ms, windows, patches,
    heads_json, musicnn_valence, musicnn_arousal, valence, arousal, av_source, av_model_id, analyze_ms, analyzed_at
)
SELECT
    track_id, file_fingerprint, analysis_version, mode, duration_ms, windows, patches,
    heads_json, musicnn_valence, musicnn_arousal, valence, arousal, av_source, av_model_id, analyze_ms, analyzed_at
FROM track_audio_features_old;

CREATE TABLE track_audio_vectors (
    track_id INTEGER PRIMARY KEY REFERENCES track_audio_features(track_id) ON DELETE CASCADE,
    genre BLOB NOT NULL,
    moodtheme BLOB NOT NULL,
    instrument BLOB NOT NULL,
    embedding BLOB NOT NULL
) STRICT;

INSERT INTO track_audio_vectors(track_id, genre, moodtheme, instrument, embedding)
SELECT track_id, genre, moodtheme, instrument, embedding
FROM track_audio_features_old;

DROP TABLE track_audio_features_old;

CREATE INDEX idx_track_audio_features_version ON track_audio_features(analysis_version, av_source);
CREATE INDEX idx_track_audio_features_analyzed ON track_audio_features(analyzed_at);
CREATE INDEX IF NOT EXISTS idx_track_audio_features_version_analyzed ON track_audio_features(analysis_version, analyzed_at);

CREATE INDEX IF NOT EXISTS idx_discovery_mappings_status_subject ON discovery_bangumi_mappings(status, subject_id, rgid);
