-- Migrate durable conversation data to the Eino-native synchronous Turn model.
-- Apply after 002_user_model_async_chat.sql.
--
-- chat_outbox and chat_summary are intentionally retained as unread legacy
-- tables for one release so the deployment can be rolled back safely.

UPDATE `chat_turn`
SET
  `status` = 4,
  `error_code` = 'migration_interrupted',
  `error_message` = 'Turn was interrupted by the Eino-native execution migration',
  `completed_at` = COALESCE(`completed_at`, CURRENT_TIMESTAMP(3))
WHERE `status` IN (1, 2);

ALTER TABLE `chat_turn`
  MODIFY COLUMN `input` MEDIUMTEXT NULL,
  ADD COLUMN `agent_revision` VARCHAR(64) NOT NULL DEFAULT 'chat-v1' AFTER `model_revision`,
  ADD COLUMN `active_slot` TINYINT UNSIGNED NULL AFTER `status`,
  ADD COLUMN `expires_at` DATETIME(3) NULL AFTER `started_at`,
  ADD UNIQUE KEY `uk_turn_active` (`chat_session_id`, `active_slot`);

ALTER TABLE `chat_message`
  ADD COLUMN `eino_role` VARCHAR(16) NULL AFTER `sequence_no`,
  ADD COLUMN `user_input_multi_content` JSON NULL AFTER `content`,
  ADD COLUMN `assistant_output_multi_content` JSON NULL AFTER `user_input_multi_content`,
  ADD COLUMN `name` VARCHAR(128) NULL AFTER `assistant_output_multi_content`,
  ADD COLUMN `tool_calls` JSON NULL AFTER `name`,
  ADD COLUMN `response_meta` JSON NULL AFTER `tool_name`,
  ADD COLUMN `reasoning_content` MEDIUMTEXT NULL AFTER `response_meta`,
  ADD COLUMN `extra` JSON NULL AFTER `reasoning_content`;

UPDATE `chat_message`
SET `eino_role` = CASE `role`
  WHEN 1 THEN 'system'
  WHEN 2 THEN 'user'
  WHEN 3 THEN 'assistant'
  WHEN 4 THEN 'tool'
  ELSE 'user'
END;

UPDATE `chat_message`
SET
  `content` = COALESCE(`content`, ''),
  `response_meta` = CASE
    WHEN `finish_reason` IS NULL
      AND `prompt_tokens` = 0
      AND `completion_tokens` = 0
      AND `total_tokens` = 0
    THEN NULL
    ELSE JSON_OBJECT(
      'finish_reason', COALESCE(`finish_reason`, ''),
      'usage', JSON_OBJECT(
        'prompt_tokens', `prompt_tokens`,
        'prompt_token_details', JSON_OBJECT('cached_tokens', 0),
        'completion_tokens', `completion_tokens`,
        'total_tokens', `total_tokens`,
        'completion_token_details', JSON_OBJECT('reasoning_tokens', 0)
      )
    )
  END,
  `extra` = `metadata`;

ALTER TABLE `chat_message`
  DROP COLUMN `role`,
  CHANGE COLUMN `eino_role` `role` VARCHAR(16) NOT NULL,
  MODIFY COLUMN `content` MEDIUMTEXT NOT NULL;
