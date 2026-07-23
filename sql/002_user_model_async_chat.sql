-- User-owned model credentials, configurations, asynchronous turns and
-- durable summaries. Apply after the existing user/chat tables.

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

ALTER TABLE `chat_session`
  ADD COLUMN `current_model_config_id` BIGINT UNSIGNED DEFAULT NULL,
  ADD COLUMN `current_model_revision` INT UNSIGNED DEFAULT NULL,
  ADD KEY `idx_session_model_config` (`current_model_config_id`);

CREATE TABLE IF NOT EXISTS `chat_turn` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `turn_id` VARCHAR(64) NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `chat_session_id` BIGINT UNSIGNED NOT NULL,
  `model_config_id` BIGINT UNSIGNED NOT NULL,
  `model_revision` INT UNSIGNED NOT NULL,
  `request_id` VARCHAR(64) NOT NULL,
  `input` MEDIUMTEXT NOT NULL,
  `status` TINYINT UNSIGNED NOT NULL,
  `stream_id` VARCHAR(64) DEFAULT NULL,
  `error_code` VARCHAR(64) DEFAULT NULL,
  `error_message` VARCHAR(1000) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `started_at` DATETIME(3) DEFAULT NULL,
  `completed_at` DATETIME(3) DEFAULT NULL,
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_turn_id` (`turn_id`),
  UNIQUE KEY `uk_turn_request` (`chat_session_id`, `request_id`),
  KEY `idx_turn_session_status` (`chat_session_id`, `status`, `created_at`),
  KEY `idx_turn_user_created` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

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
