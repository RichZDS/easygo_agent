package chat

import (
	"context"
	"easygo-agent/internal/agent"
	deepagent "easygo-agent/internal/agent/deep_agent"
	"easygo-agent/internal/errorcode"
	"easygo-agent/internal/model"

	"github.com/bytedance/gopkg/util/logger"
	"gorm.io/gorm"
)

type AgentChatService struct {
	db *gorm.DB
}

func NewAgentChatService(db *gorm.DB) *AgentChatService {
	return &AgentChatService{db: db}
}

// 聊天框架入口函数
// v1 版本先只支持文本消息
func (s *AgentChatService) Chat(ctx context.Context, userID uint64, sessionID string, message string) (string, error) {
	// 先去校验用户和会话是否存在
	user, err := model.FindUserByID(ctx, s.db, userID)
	if err != nil {
		logger.Debugf("find user by id failed: %v", err)
		return "", errorcode.NewError(errorcode.Database, "find user by id failed")
	}
	session, err := model.FindChatSessionBySessionID(ctx, s.db, sessionID)
	if err != nil {
		return "", errorcode.NewError(errorcode.Database, "find chat session by session id failed")
	}
	logger.Debugf("find chat session user %v, sessionid %s, title %s success", user, sessionID, session.Title)

	// todo: 从reids中获取会话的上下文50条消息

	//获取到信息了以后 创建llm
	deepAgent, err := deepagent.NewDeepAgentDeepSeek(ctx, agent.Config{
		Model: "deepseek-r1"})

	return "", nil
}
