-- EasyGo Agent database initialization for MySQL 8.x (big-bang schema).

-- =============================================================================
-- Drop legacy chat / model tables (keep `user`)
-- =============================================================================

DROP TABLE IF EXISTS `chat_outbox`;
DROP TABLE IF EXISTS `chat_summary`;
DROP TABLE IF EXISTS `chat_message`;
DROP TABLE IF EXISTS `chat_turn`;
DROP TABLE IF EXISTS `chat_session`;
DROP TABLE IF EXISTS `user_model_config`;
DROP TABLE IF EXISTS `user_provider_credential`;
DROP TABLE IF EXISTS `agent_run`;
DROP TABLE IF EXISTS `ai_model`;
DROP TABLE IF EXISTS `provider`;
DROP TABLE IF EXISTS `session`;

-- =============================================================================
-- user
-- =============================================================================

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
-- provider / ai_model
-- =============================================================================

CREATE TABLE IF NOT EXISTS `provider` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `name` VARCHAR(128) NOT NULL,
  `type` VARCHAR(64) NOT NULL COMMENT '适配器类型：deepseek/openai/minimax',
  `api_key` TEXT NOT NULL COMMENT '加密后的 API Key JSON',
  `base_url` VARCHAR(512) DEFAULT NULL,
  `test_model` VARCHAR(128) NOT NULL DEFAULT '' COMMENT '连通性检测模型名',
  `status` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_type` (`user_id`, `type`),
  KEY `idx_provider_user_status` (`user_id`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `ai_model` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `provider_id` BIGINT UNSIGNED NOT NULL,
  `name` VARCHAR(128) NOT NULL,
  `model_id` VARCHAR(128) NOT NULL COMMENT '实际调用的模型标识',
  `params` JSON DEFAULT NULL COMMENT 'Eino model 相关配置',
  `status` TINYINT UNSIGNED NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_provider_model` (`provider_id`, `model_id`),
  KEY `idx_ai_model_user_status` (`user_id`, `status`),
  CONSTRAINT `fk_ai_model_provider` FOREIGN KEY (`provider_id`) REFERENCES `provider` (`id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- =============================================================================
-- session / chat_message / agent_run
-- =============================================================================

CREATE TABLE IF NOT EXISTS `session` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `title` VARCHAR(255) NOT NULL DEFAULT '新对话',
  `status` TINYINT NOT NULL DEFAULT 1 COMMENT '1 正常、2 归档、3 禁用',
  `current_ai_model_id` BIGINT UNSIGNED DEFAULT NULL,
  `active_run_id` BIGINT UNSIGNED DEFAULT NULL,
  `next_message_seq` BIGINT UNSIGNED NOT NULL DEFAULT 1,
  `message_count` INT UNSIGNED NOT NULL DEFAULT 0,
  `last_message_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  `deleted_at` DATETIME(3) DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_session_user_last` (`user_id`, `last_message_at`),
  KEY `idx_session_user_deleted` (`user_id`, `deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `agent_run` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `session_id` BIGINT UNSIGNED NOT NULL,
  `user_id` BIGINT UNSIGNED NOT NULL,
  `request_id` VARCHAR(64) NOT NULL,
  `ai_model_id` BIGINT UNSIGNED NOT NULL,
  `status` TINYINT UNSIGNED NOT NULL COMMENT '1 running 2 succeeded 3 failed 4 cancelled',
  `prompt_tokens` INT UNSIGNED DEFAULT NULL,
  `completion_tokens` INT UNSIGNED DEFAULT NULL,
  `total_tokens` INT UNSIGNED DEFAULT NULL,
  `latency_ms` INT UNSIGNED DEFAULT NULL,
  `error_code` VARCHAR(64) DEFAULT NULL,
  `error_message` VARCHAR(1000) DEFAULT NULL,
  `config_snapshot` JSON DEFAULT NULL,
  `started_at` DATETIME(3) DEFAULT NULL,
  `finished_at` DATETIME(3) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_request` (`user_id`, `request_id`),
  KEY `idx_run_session_created` (`session_id`, `created_at`),
  KEY `idx_run_user_created` (`user_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `chat_message` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `session_id` BIGINT UNSIGNED NOT NULL,
  `agent_run_id` BIGINT UNSIGNED DEFAULT NULL,
  `seq` BIGINT UNSIGNED NOT NULL,
  `role` VARCHAR(16) NOT NULL,
  `text` MEDIUMTEXT NOT NULL,
  `metadata` JSON DEFAULT NULL,
  `raw` JSON NOT NULL,
  `schema_version` INT UNSIGNED NOT NULL DEFAULT 1,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_session_seq` (`session_id`, `seq`),
  KEY `idx_message_run` (`agent_run_id`),
  KEY `idx_message_session_created` (`session_id`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- =============================================================================
-- cron_job_run / cron_job_log
-- =============================================================================

CREATE TABLE IF NOT EXISTS `cron_job_run` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `job_name` VARCHAR(128) NOT NULL,
  `status` TINYINT NOT NULL COMMENT '1 running 2 success 3 failed 4 skipped',
  `started_at` DATETIME(3) NOT NULL,
  `finished_at` DATETIME(3) DEFAULT NULL,
  `error_message` VARCHAR(1024) DEFAULT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_cron_job_run_name_started` (`job_name`, `started_at`),
  KEY `idx_cron_job_run_status_started` (`status`, `started_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `cron_job_log` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `run_id` BIGINT UNSIGNED NOT NULL,
  `level` VARCHAR(16) NOT NULL,
  `message` TEXT NOT NULL,
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  KEY `idx_cron_job_log_run_id` (`run_id`, `id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
