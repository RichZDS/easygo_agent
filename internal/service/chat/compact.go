package chat

import "easygo-agent/internal/model"

// CompactHistory 压缩历史消息
func CompactHistory(history []model.ChatMessage, maxContextLength int) ([]model.ChatMessage, error) {
	// 根据maxContextLength压缩历史消息
	// 1. 从历史消息中获取最近maxContextLength条消息
	// 2. 将最近maxContextLength条消息转换为Eino AgenticMessage
	// 3. 将Eino AgenticMessage转换为ChatMessage ?
	// 4. 将压缩过后的历史信息写入数据库
	// 4. 返回压缩后的历史消息

	return nil, nil
}
