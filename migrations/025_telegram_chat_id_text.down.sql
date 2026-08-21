-- 回滚：数值重复的行保留最早 id，其余删除后恢复 BIGINT 唯一约束。
ALTER TABLE telegram_subscribers DROP COLUMN chat_id_num;
DELETE FROM telegram_subscribers a USING telegram_subscribers b
    WHERE a.id > b.id AND a.chat_id::bigint = b.chat_id::bigint;
ALTER TABLE telegram_subscribers
    ALTER COLUMN chat_id TYPE BIGINT USING chat_id::bigint;
ALTER TABLE telegram_subscribers ADD CONSTRAINT telegram_subscribers_chat_id_key UNIQUE (chat_id);
