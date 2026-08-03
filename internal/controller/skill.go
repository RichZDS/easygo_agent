package controller

import (
	"errors"
	"fmt"

	"easygo-agent/internal/platform/errorcode"
	"easygo-agent/internal/platform/logger"
	"easygo-agent/internal/platform/middleware"
	"easygo-agent/internal/platform/response"
	skillstore "easygo-agent/internal/skill/store"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// SkillController projects authenticated user skill operations onto HTTP.
type SkillController struct {
	store *skillstore.Store
}

// NewSkillController creates a thin HTTP adapter for the user skill Store.
func NewSkillController(store *skillstore.Store) *SkillController {
	return &SkillController{store: store}
}

// Upload validates multipart fields and publishes one private skill.
func (ctl *SkillController) Upload(c *gin.Context) {
	if ctl == nil || ctl.store == nil {
		err := errorcode.New(errorcode.Internal, "skill store is unavailable")
		logger.ErrorContext(c.Request.Context(), "upload skill request failed", zap.Error(err))
		response.Fail(c, err)
		return
	}
	skillID := c.PostForm("skill_id")
	fileHeader, err := c.FormFile("file")
	if err != nil {
		appErr := errorcode.New(errorcode.InvalidParameter, "skill_id and ZIP file are required")
		logger.ErrorContext(c.Request.Context(), "upload skill request failed", zap.String("skill_id", skillID), zap.Error(err))
		response.Fail(c, appErr)
		return
	}
	file, err := fileHeader.Open()
	if err != nil {
		appErr := errorcode.Wrap(errorcode.InvalidParameter, fmt.Errorf("open uploaded ZIP: %w", err))
		logger.ErrorContext(c.Request.Context(), "upload skill request failed", zap.String("skill_id", skillID), zap.Error(appErr))
		response.Fail(c, appErr)
		return
	}
	info, uploadErr := ctl.store.Upload(c.Request.Context(), middleware.UserID(c), skillID, file)
	if closeErr := file.Close(); closeErr != nil {
		logger.ErrorContext(c.Request.Context(), "close uploaded skill ZIP failed", zap.String("skill_id", skillID), zap.Error(closeErr))
		if uploadErr == nil {
			uploadErr = fmt.Errorf("close uploaded ZIP: %w", closeErr)
		}
	}
	if uploadErr != nil {
		appErr := skillStoreError(uploadErr)
		logger.ErrorContext(c.Request.Context(), "upload skill request failed", zap.String("skill_id", skillID), zap.Error(appErr))
		response.Fail(c, appErr)
		return
	}
	response.Created(c, info)
}

// List returns all builtin and private skills visible to the authenticated user.
func (ctl *SkillController) List(c *gin.Context) {
	if ctl == nil || ctl.store == nil {
		err := errorcode.New(errorcode.Internal, "skill store is unavailable")
		logger.ErrorContext(c.Request.Context(), "list skill request failed", zap.Error(err))
		response.Fail(c, err)
		return
	}
	items, err := ctl.store.List(c.Request.Context(), middleware.UserID(c))
	if err != nil {
		appErr := skillStoreError(err)
		logger.ErrorContext(c.Request.Context(), "list skill request failed", zap.Error(appErr))
		response.Fail(c, appErr)
		return
	}
	response.Success(c, items)
}

// Delete removes one private skill owned by the authenticated user.
func (ctl *SkillController) Delete(c *gin.Context) {
	if ctl == nil || ctl.store == nil {
		err := errorcode.New(errorcode.Internal, "skill store is unavailable")
		logger.ErrorContext(c.Request.Context(), "delete skill request failed", zap.Error(err))
		response.Fail(c, err)
		return
	}
	skillID := c.Param("skill_id")
	if err := ctl.store.Delete(c.Request.Context(), middleware.UserID(c), skillID); err != nil {
		appErr := skillStoreError(err)
		logger.ErrorContext(c.Request.Context(), "delete skill request failed", zap.String("skill_id", skillID), zap.Error(appErr))
		response.Fail(c, appErr)
		return
	}
	response.Success(c, gin.H{"skill_id": skillID})
}

// skillStoreError maps filesystem-domain sentinels to stable public error codes.
func skillStoreError(err error) error {
	var appErr error
	switch {
	case errors.Is(err, skillstore.ErrArchiveTooLarge):
		appErr = errorcode.Wrap(errorcode.PayloadTooLarge, err)
	case errors.Is(err, skillstore.ErrInvalidSkill):
		appErr = errorcode.Wrap(errorcode.InvalidParameter, err)
	case errors.Is(err, skillstore.ErrConflict):
		appErr = errorcode.Wrap(errorcode.Conflict, err)
	case errors.Is(err, skillstore.ErrBuiltinReadOnly):
		appErr = errorcode.Wrap(errorcode.Forbidden, err)
	case errors.Is(err, skillstore.ErrNotFound):
		appErr = errorcode.Wrap(errorcode.NotFound, err)
	default:
		appErr = errorcode.Wrap(errorcode.Internal, err)
	}
	logger.Error("map skill store error failed", zap.Error(appErr))
	return appErr
}
