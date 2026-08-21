-- 026_request_history_tokens: 回滚
DROP INDEX IF EXISTS idx_request_history_tokens;
ALTER TABLE request_history
    DROP COLUMN IF EXISTS prompt_tokens,
    DROP COLUMN IF EXISTS completion_tokens;
