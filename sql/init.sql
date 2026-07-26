-- EasyGo Agent database initialization for MySQL 8.x.
-- Consolidated from user.sql, 002_user_model_async_chat.sql, and
-- 003_eino_native_turns.sql for fresh installs. Migration-only UPDATE/ALTER
-- statements from 003 are folded into the final CREATE TABLE definitions below.

-- =============================================================================
-- user.sql
-- =============================================================================

-- Password values below mean an already-derived password hash, never plaintext.

CREATE TABLE IF NOT EXISTS `user` (
    `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '用户唯一ID',
    `name` VARCHAR(64) NOT NULL COMMENT '用户名',
    `password` VARCHAR(255) NOT NULL COMMENT '密码哈希',
    `salt` VARCHAR(64) NOT NULL COMMENT '密码盐值',
    `email` VARCHAR(128) DEFAULT NULL COMMENT '邮箱',
    `phone` VARCHAR(32) DEFAULT NULL COMMENT '手机号',
    `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
        ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    `deleted_at` DATETIME DEFAULT NULL COMMENT '删除时间',
    PRIMARY KEY (`id`),
    UNIQUE KEY `uk_user_name` (`name`),
    UNIQUE KEY `uk_user_email` (`email`),
    UNIQUE KEY `uk_user_phone` (`phone`),
    KEY `idx_user_deleted_created` (`deleted_at`, `created_at`)
) ENGINE=InnoDB
  DEFAULT CHARSET=utf8mb4
  COLLATE=utf8mb4_unicode_ci
  COMMENT='用户表';

-- =============================================================================
-- 002_user_model_async_chat.sql (+ chat_session / chat_message base tables)
-- =============================================================================

CREATE TABLE IF NOT EXISTS `user_provider_credential` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `provider_name` VARCHAR(64) NOT NULL,
  `ciphertext` TEXT NOT NULL,
  `nonce` VARCHAR(32) NOT NULL,
  `algorithm` VARCHAR(32) NOT NULL,
  `key_version` VARCHAR(32) NOT NULL,
  `status` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_provider` (`user_id`, `provider_name`),
  KEY `idx_credential_user_status` (`user_id`, `status`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `user_model_config` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `credential_id` BIGINT UNSIGNED NOT NULL,
  `provider_name` VARCHAR(64) NOT NULL,
  `model_name` VARCHAR(128) NOT NULL,
  `base_url` VARCHAR(512) DEFAULT NULL,
  `max_context_tokens` INT UNSIGNED NOT NULL,
  `max_output_tokens` INT UNSIGNED NOT NULL,
  `settings` JSON DEFAULT NULL,
  `revision` INT UNSIGNED NOT NULL DEFAULT 1,
  `enabled` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_model_user_enabled` (`user_id`, `enabled`, `deleted_at`),
  CONSTRAINT `fk_model_credential` FOREIGN KEY (`credential_id`) REFERENCES `user_provider_credential` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `chat_session` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `session_id` VARCHAR(64) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `title` VARCHAR(255) NOT NULL DEFAULT '新对话',
  `status` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `last_message_at` DATETIME(3) DEFAULT NULL,
  `message_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `current_model_config_id` BIGINT UNSIGNED DEFAULT NULL,
  `current_model_revision` INT UNSIGNED DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_session_id` (`session_id`),
  KEY `idx_user_id` (`user_id`),
  KEY `idx_user_last_message` (`user_id`, `last_message_at`),
  KEY `idx_user_deleted` (`user_id`, `deleted_at`),
  KEY `idx_session_model_config` (`current_model_config_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Final chat_message schema after 003_eino_native_turns.sql.
CREATE TABLE IF NOT EXISTS `chat_message` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `message_id` VARCHAR(64) NOT NULL,
  `chat_session_id` BIGINT UNSIGNED NOT NULL,
  `turn_id` VARCHAR(64) DEFAULT NULL,
  `parent_message_id` VARCHAR(64) DEFAULT NULL,
  `sequence_no` INT UNSIGNED NOT NULL,
  `role` VARCHAR(16) NOT NULL,
  `content` MEDIUMTEXT NOT NULL,
  `user_input_multi_content` JSON DEFAULT NULL,
  `assistant_output_multi_content` JSON DEFAULT NULL,
  `name` VARCHAR(128) DEFAULT NULL,
  `tool_calls` JSON DEFAULT NULL,
  `tool_call_id` VARCHAR(128) DEFAULT NULL,
  `tool_name` VARCHAR(128) DEFAULT NULL,
  `response_meta` JSON DEFAULT NULL,
  `reasoning_content` MEDIUMTEXT DEFAULT NULL,
  `extra` JSON DEFAULT NULL,
  `message_type` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `model_name` VARCHAR(128) DEFAULT NULL,
  `provider_name` VARCHAR(64) DEFAULT NULL,
  `status` TINYINT UNSIGNED NOT NULL DEFAULT 2,
  `request_id` VARCHAR(64) DEFAULT NULL,
  `error_code` VARCHAR(64) DEFAULT NULL,
  `error_message` VARCHAR(1000) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_message_id` (`message_id`),
  UNIQUE KEY `uk_session_sequence` (`chat_session_id`, `sequence_no`),
  KEY `idx_session_created` (`chat_session_id`, `created_at`),
  KEY `idx_session_turn` (`chat_session_id`, `turn_id`),
  KEY `idx_parent_message` (`parent_message_id`),
  KEY `idx_request_id` (`request_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Final chat_turn schema after 003_eino_native_turns.sql.
CREATE TABLE IF NOT EXISTS `chat_turn` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `turn_id` VARCHAR(64) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `chat_session_id` BIGINT UNSIGNED NOT NULL,
  `model_config_id` BIGINT UNSIGNED NOT NULL,
  `model_revision` INT UNSIGNED NOT NULL,
  `agent_revision` VARCHAR(64) NOT NULL DEFAULT 'chat-v1',
  `request_id` VARCHAR(64) NOT NULL,
  `input` MEDIUMTEXT NULL,
  `status` TINYINT UNSIGNED NOT NULL,
  `active_slot` TINYINT UNSIGNED NULL,
  `stream_id` VARCHAR(64) DEFAULT NULL,
  `error_code` VARCHAR(64) DEFAULT NULL,
  `error_message` VARCHAR(1000) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `started_at` DATETIME(3) DEFAULT NULL,
  `expires_at` DATETIME(3) DEFAULT NULL,
  `completed_at` DATETIME(3) DEFAULT NULL,
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_turn_id` (`turn_id`),
  UNIQUE KEY `uk_turn_request` (`chat_session_id`, `request_id`),
  UNIQUE KEY `uk_turn_active` (`chat_session_id`, `active_slot`),
  KEY `idx_turn_session_status` (`chat_session_id`, `status`, `created_at`),
  KEY `idx_turn_user_created` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Legacy tables retained for rollback safety; new code does not read or write them.
CREATE TABLE IF NOT EXISTS `chat_summary` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `chat_session_id` BIGINT UNSIGNED NOT NULL,
  `from_sequence_no` INT UNSIGNED NOT NULL,
  `to_sequence_no` INT UNSIGNED NOT NULL,
  `content` MEDIUMTEXT NOT NULL,
  `token_count` INT UNSIGNED NOT NULL,
  `model_config_id` BIGINT UNSIGNED NOT NULL,
  `model_revision` INT UNSIGNED NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_summary_range` (`chat_session_id`, `from_sequence_no`, `to_sequence_no`),
  KEY `idx_summary_session_end` (`chat_session_id`, `to_sequence_no`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `chat_outbox` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `turn_id` VARCHAR(64) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `status` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `stream_id` VARCHAR(64) DEFAULT NULL,
  `attempts` INT UNSIGNED NOT NULL DEFAULT 0,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `published_at` DATETIME(3) DEFAULT NULL,
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_outbox_turn` (`turn_id`),
  KEY `idx_outbox_status_created` (`status`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- =============================================================================
-- user.sql — common query templates
-- =============================================================================

-- 创建用户。参数顺序：name, password_hash, salt, email, phone。
INSERT INTO `user` (`name`, `password`, `salt`, `email`, `phone`)
VALUES (?, ?, ?, ?, ?);

-- 按 ID 查询未删除用户。
SELECT `id`, `name`, `email`, `phone`, `created_at`, `updated_at`
FROM `user`
WHERE `id` = ? AND `deleted_at` IS NULL
LIMIT 1;

-- 登录查询需要读取密码哈希和盐；参数为 name/email/phone 三次。
SELECT `id`, `name`, `password`, `salt`, `email`, `phone`
FROM `user`
WHERE `deleted_at` IS NULL
  AND (`name` = ? OR `email` = ? OR `phone` = ?)
LIMIT 1;

-- 使用 ID 游标分页，参数顺序：cursor_id, page_size。
SELECT `id`, `name`, `email`, `phone`, `created_at`, `updated_at`
FROM `user`
WHERE `deleted_at` IS NULL AND `id` < ?
ORDER BY `id` DESC
LIMIT ?;

-- 修改邮箱和手机号。参数顺序：email, phone, id。
UPDATE `user`
SET `email` = ?, `phone` = ?
WHERE `id` = ? AND `deleted_at` IS NULL;

-- 修改密码。参数顺序：new_password_hash, new_salt, id。
UPDATE `user`
SET `password` = ?, `salt` = ?
WHERE `id` = ? AND `deleted_at` IS NULL;

-- 软删除与恢复，参数均为 id。
UPDATE `user` SET `deleted_at` = CURRENT_TIMESTAMP
WHERE `id` = ? AND `deleted_at` IS NULL;

UPDATE `user` SET `deleted_at` = NULL
WHERE `id` = ? AND `deleted_at` IS NOT NULL;

-- 仅在明确需要物理清理时使用硬删除。
DELETE FROM `user` WHERE `id` = ?;
