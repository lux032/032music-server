-- Search hits supported only by type agreement are not identity candidates.
-- Remove pending noise only; preserve every accepted/rejected human decision.
-- A future search may reconsider these subjects if genuine title evidence exists.
DELETE FROM work_match_candidates
WHERE source='bangumi' AND status='candidate' AND score<=10
  AND evidence_json IN ('["类型一致"]', '[]', 'null');
