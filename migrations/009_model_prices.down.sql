-- 009_model_prices: 回滚
ALTER TABLE probe_results DROP COLUMN IF EXISTS official_output_per_m;
ALTER TABLE probe_results DROP COLUMN IF EXISTS official_input_per_m;
ALTER TABLE probe_results DROP COLUMN IF EXISTS basis;
ALTER TABLE probe_results DROP COLUMN IF EXISTS completion_tokens;
ALTER TABLE probe_results DROP COLUMN IF EXISTS prompt_tokens;
DROP TABLE IF EXISTS model_prices;
