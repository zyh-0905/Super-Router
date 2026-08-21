-- 订阅者 chat_id 从 BIGINT 改为 TEXT：保留用户输入的前导零（如 00123456789）。
-- 应用层负责校验/解析（API 写入前校验整数格式），发送与匹配时按数值比较。
-- chat_id_num 生成列承载数值唯一约束：Chat ID 数值相同的订阅者仍互斥（唯一冲突由 API 报 409）。
ALTER TABLE telegram_subscribers DROP CONSTRAINT telegram_subscribers_chat_id_key;
ALTER TABLE telegram_subscribers
    ALTER COLUMN chat_id TYPE TEXT USING chat_id::text,
    ADD COLUMN chat_id_num BIGINT GENERATED ALWAYS AS (chat_id::bigint) STORED;
CREATE UNIQUE INDEX telegram_subscribers_chat_id_num_key
    ON telegram_subscribers (chat_id_num)
    WHERE chat_id_num IS NOT NULL;
