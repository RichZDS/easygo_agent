package chat

import "easygo-agent/internal/model"

// CompactHistory 按上下文条数上限裁剪历史消息。
// 当前仅做透传与上限裁剪；基于 max_context_length 的 token 级压缩尚未实现。
func CompactHistory(history []model.ChatMessage, maxContextLength int) ([]model.ChatMessage, error) {
	if maxContextLength <= 0 || len(history) <= maxContextLength {
		return history, nil
	}
	return history[len(history)-maxContextLength:], nil
}
