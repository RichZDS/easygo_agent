// Package manifest validates the subset of Eino SKILL.md metadata supported by EasyGo.
package manifest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"easygo-agent/internal/platform/logger"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

// FrontMatter describes the Eino skill metadata fields understood by this service.
type FrontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Context     string `yaml:"context"`
	Agent       string `yaml:"agent"`
	Model       string `yaml:"model"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9-]{2,64}$`)

// ValidateID verifies the canonical skill identifier grammar.
func ValidateID(skillID string) error {
	if !idPattern.MatchString(skillID) {
		err := fmt.Errorf("skill ID must match %s", idPattern.String())
		logger.Error("validate skill ID failed", zap.String("skill_id", skillID), zap.Error(err))
		return err
	}
	return nil
}

// Parse validates and decodes one supported SKILL.md document.
func Parse(content []byte) (FrontMatter, error) {
	normalized := strings.ReplaceAll(strings.TrimSpace(string(content)), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		err := fmt.Errorf("SKILL.md must start with a YAML frontmatter delimiter")
		logger.Error("parse skill manifest failed", zap.Error(err))
		return FrontMatter{}, err
	}

	closingLine := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			closingLine = index
			break
		}
	}
	if closingLine < 0 {
		err := fmt.Errorf("SKILL.md frontmatter closing delimiter is missing")
		logger.Error("parse skill manifest failed", zap.Error(err))
		return FrontMatter{}, err
	}

	var frontMatter FrontMatter
	decoder := yaml.NewDecoder(bytes.NewBufferString(strings.Join(lines[1:closingLine], "\n")))
	decoder.KnownFields(true)
	if err := decoder.Decode(&frontMatter); err != nil {
		wrappedErr := fmt.Errorf("decode SKILL.md frontmatter: %w", err)
		logger.Error("parse skill manifest failed", zap.Error(wrappedErr))
		return FrontMatter{}, wrappedErr
	}
	frontMatter.Name = strings.TrimSpace(frontMatter.Name)
	frontMatter.Description = strings.TrimSpace(frontMatter.Description)
	frontMatter.Context = strings.TrimSpace(frontMatter.Context)
	frontMatter.Agent = strings.TrimSpace(frontMatter.Agent)
	frontMatter.Model = strings.TrimSpace(frontMatter.Model)
	if err := ValidateID(frontMatter.Name); err != nil {
		wrappedErr := fmt.Errorf("validate manifest name: %w", err)
		logger.Error("parse skill manifest failed", zap.String("skill_id", frontMatter.Name), zap.Error(wrappedErr))
		return FrontMatter{}, wrappedErr
	}
	if frontMatter.Description == "" {
		err := fmt.Errorf("skill description cannot be empty")
		logger.Error("parse skill manifest failed", zap.String("skill_id", frontMatter.Name), zap.Error(err))
		return FrontMatter{}, err
	}
	if frontMatter.Context != "" || frontMatter.Agent != "" || frontMatter.Model != "" {
		err := fmt.Errorf("only inline skills without agent or model overrides are supported")
		logger.Error("parse skill manifest failed", zap.String("skill_id", frontMatter.Name), zap.Error(err))
		return FrontMatter{}, err
	}
	return frontMatter, nil
}

// Read loads and parses one SKILL.md file while honoring cancellation.
func Read(ctx context.Context, filePath string) (FrontMatter, error) {
	if err := ctx.Err(); err != nil {
		logger.ErrorContext(ctx, "read skill manifest failed", zap.String("path", filePath), zap.Error(err))
		return FrontMatter{}, err
	}
	content, err := os.ReadFile(filePath)
	if err != nil {
		wrappedErr := fmt.Errorf("read skill manifest %q: %w", filePath, err)
		logger.ErrorContext(ctx, "read skill manifest failed", zap.String("path", filePath), zap.Error(wrappedErr))
		return FrontMatter{}, wrappedErr
	}
	frontMatter, err := Parse(content)
	if err != nil {
		wrappedErr := fmt.Errorf("parse skill manifest %q: %w", filePath, err)
		logger.ErrorContext(ctx, "read skill manifest failed", zap.String("path", filePath), zap.Error(wrappedErr))
		return FrontMatter{}, wrappedErr
	}
	return frontMatter, nil
}
