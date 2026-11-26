-- Persist manual and automatic web search outcomes on pending avatar review
-- cards so administrators can distinguish unsearched items from already-searched
-- no-result / failed items across reloads and queue runs.

ALTER TABLE artist_image_candidates ADD COLUMN web_search_state TEXT NOT NULL DEFAULT '' CHECK (web_search_state IN ('', 'not_found_auto', 'not_found_manual', 'failed'));
ALTER TABLE artist_image_candidates ADD COLUMN web_search_note TEXT NOT NULL DEFAULT '';
ALTER TABLE artist_image_candidates ADD COLUMN web_searched_at TEXT;
