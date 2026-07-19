-- User table and common statements for MySQL 8.x.
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
