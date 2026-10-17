ALTER TABLE ai_settings ADD COLUMN fallback_model TEXT NOT NULL DEFAULT '';
ALTER TABLE ai_settings ADD COLUMN translation_fallback_model TEXT NOT NULL DEFAULT '';
