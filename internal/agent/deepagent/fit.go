package deepagent

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"easygo-agent/internal/skill"

	"github.com/cloudwego/eino/schema"
)

const (
	heuristicLoadSkill = "load_skill"
	heuristicCallTool  = "call_tool"
	maxThemeNoteRunes  = 900
)

// FitRequest is the over-budget input the shipped rewrite consumes.
type FitRequest struct {
	Messages []*schema.AgenticMessage
	Tools    []*schema.ToolInfo
	Limit    int
}

// FitResult is the situation-aware rewrite: shrink heuristic skill/tool first,
// then extract the smallest older-dialogue prefix that still keeps theme facts.
type FitResult struct {
	Messages                []*schema.AgenticMessage
	Tools                   []*schema.ToolInfo
	Before                  Composition
	After                   Composition
	SkillShrunk             bool
	DroppedTools            []string
	KeptTools               []string
	DialogueSummarizedBytes int
	DialogueKeptBytes       int
	SummarizedMessages      int
	KeptMessages            int
	ThemeNotes              string
	Fitted                  bool
}

// Fit is the shipped keep/drop policy. It does not call a model.
func Fit(req FitRequest) (FitResult, error) {
	before, err := Measure(req.Messages, req.Tools)
	if err != nil {
		return FitResult{}, err
	}
	result := FitResult{Before: before, Messages: req.Messages, Tools: req.Tools}
	if req.Limit <= 0 {
		result.After = before
		result.Fitted = true
		result.KeptMessages = len(req.Messages)
		result.KeptTools = toolNames(req.Tools)
		return result, nil
	}
	if max(before.Total, providerUsage(req.Messages)) <= req.Limit {
		result.After = before
		result.Fitted = true
		result.KeptMessages = len(req.Messages)
		result.KeptTools = toolNames(req.Tools)
		return result, nil
	}

	messages := compactSkillMessages(req.Messages, lastUserText(req.Messages))
	result.SkillShrunk = skillBytes(messages) < before.Skill
	used := usedToolNames(req.Messages)
	situation := lastUserText(req.Messages) + "\n" + firstUserText(req.Messages)
	tools, dropped := dropHeuristicTools(req.Tools, situation, used)
	if providerUsage(req.Messages) > req.Limit {
		result.DroppedTools = dropped
		result.KeptTools = toolNames(tools)
		result.Messages = messages
		result.Tools = tools
		result.KeptMessages = len(messages)
		result.DialogueKeptBytes = sizeAll(splitKeepDialogue(messages))
		after, measErr := Measure(result.Messages, result.Tools)
		if measErr != nil {
			return FitResult{}, measErr
		}
		result.After = after
		return result, nil
	}
	fitted, err := fitDialogue(messages, tools, req.Limit, situation)
	if err != nil {
		return FitResult{}, err
	}
	ok, err := wouldFit(fitted.Messages, fitted.Tools, req.Limit)
	if err != nil {
		return FitResult{}, err
	}
	if !ok {
		var extra []string
		fitted.Tools, extra = dropUnusedTools(fitted.Tools, situation, used)
		dropped = append(dropped, extra...)
		fitted, err = fitDialogue(messages, fitted.Tools, req.Limit, situation)
		if err != nil {
			return FitResult{}, err
		}
	}
	result.DroppedTools = dropped
	result.KeptTools = toolNames(fitted.Tools)
	result.Messages = fitted.Messages
	result.Tools = fitted.Tools
	result.ThemeNotes = fitted.ThemeNotes
	result.SummarizedMessages = fitted.SummarizedMessages
	result.DialogueSummarizedBytes = fitted.DialogueSummarizedBytes
	result.DialogueKeptBytes = fitted.DialogueKeptBytes
	result.KeptMessages = len(fitted.Messages)
	after, err := Measure(result.Messages, result.Tools)
	if err != nil {
		return FitResult{}, err
	}
	result.After = after
	tokens := max(after.Total, providerUsage(result.Messages))
	result.Fitted = tokens <= req.Limit
	return result, nil
}

// FitBaseline models the old path: keep full skill/tool overhead and treat
// every non-system dialogue message as summarized. Used only for before/after.
func FitBaseline(req FitRequest) (FitResult, error) {
	before, err := Measure(req.Messages, req.Tools)
	if err != nil {
		return FitResult{}, err
	}
	var summarized int
	var summarizedBytes int
	var kept []*schema.AgenticMessage
	for _, message := range req.Messages {
		if message != nil && message.Role == schema.AgenticRoleTypeSystem {
			kept = append(kept, message)
			continue
		}
		n, err := messageBytes(message)
		if err != nil {
			return FitResult{}, err
		}
		summarized++
		summarizedBytes += n
	}
	kept = append(kept, schemaUser("Conversation history was summarized in full; heuristic skill and tools were not reduced."))
	after, err := Measure(kept, req.Tools)
	if err != nil {
		return FitResult{}, err
	}
	return FitResult{
		Messages:                kept,
		Tools:                   req.Tools,
		Before:                  before,
		After:                   after,
		DroppedTools:            nil,
		KeptTools:               toolNames(req.Tools),
		DialogueSummarizedBytes: summarizedBytes,
		DialogueKeptBytes:       after.Dialogue,
		SummarizedMessages:      summarized,
		KeptMessages:            len(kept),
		Fitted:                  after.Total <= req.Limit,
	}, nil
}

type dialogueFit struct {
	Messages                []*schema.AgenticMessage
	Tools                   []*schema.ToolInfo
	ThemeNotes              string
	SummarizedMessages      int
	DialogueSummarizedBytes int
	DialogueKeptBytes       int
}

func fitDialogue(messages []*schema.AgenticMessage, tools []*schema.ToolInfo, limit int, situation string) (dialogueFit, error) {
	sys, dialogue := splitSysDialogue(messages)
	out := dialogueFit{Tools: tools}
	if under, err := wouldFit(append(sys, dialogue...), tools, limit); err != nil {
		return dialogueFit{}, err
	} else if under {
		out.Messages = append(sys, dialogue...)
		out.DialogueKeptBytes = sizeAll(dialogue)
		return out, nil
	}
	for tail := len(dialogue); tail >= 1; tail-- {
		prefix := dialogue[:len(dialogue)-tail]
		keep := dialogue[len(dialogue)-tail:]
		notes := extractThemeNotes(prefix, situation)
		candidate := append([]*schema.AgenticMessage{}, sys...)
		if notes != "" {
			candidate = append(candidate, schemaUser(notes))
		}
		candidate = append(candidate, keep...)
		ok, err := wouldFit(candidate, tools, limit)
		if err != nil {
			return dialogueFit{}, err
		}
		if !ok {
			continue
		}
		out.Messages = candidate
		out.ThemeNotes = notes
		out.SummarizedMessages = len(prefix)
		out.DialogueSummarizedBytes = sizeAll(prefix)
		out.DialogueKeptBytes = sizeAll(keep)
		return out, nil
	}
	notes := extractThemeNotes(dialogue[:len(dialogue)-1], situation)
	last := dialogue[len(dialogue)-1]
	candidate := append([]*schema.AgenticMessage{}, sys...)
	if notes != "" {
		candidate = append(candidate, schemaUser(notes))
	}
	candidate = append(candidate, last)
	out.Messages = candidate
	out.ThemeNotes = notes
	out.SummarizedMessages = len(dialogue) - 1
	out.DialogueSummarizedBytes = sizeAll(dialogue[:len(dialogue)-1])
	out.DialogueKeptBytes = sizeAll([]*schema.AgenticMessage{last})
	return out, nil
}

func compactSkillMessages(messages []*schema.AgenticMessage, situation string) []*schema.AgenticMessage {
	out := make([]*schema.AgenticMessage, 0, len(messages))
	for _, message := range messages {
		if !isSkillCatalogMessage(message) {
			out = append(out, message)
			continue
		}
		compacted, changed := skill.CompactInstruction(messageText(message), situation)
		if !changed {
			out = append(out, message)
			continue
		}
		out = append(out, schemaSystem(compacted))
	}
	return out
}

func dropUnusedTools(tools []*schema.ToolInfo, situation string, used map[string]bool) (kept []*schema.ToolInfo, dropped []string) {
	hasDispatcher := false
	for _, info := range tools {
		if info != nil && info.Name == "call_tool" {
			hasDispatcher = true
		}
	}
	if !hasDispatcher {
		return tools, nil
	}
	sit := foldRunes(situation)
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if persistentCapability(tool.Name) || used[tool.Name] || toolMentioned(tool.Name, sit) {
			kept = append(kept, tool)
			continue
		}
		dropped = append(dropped, tool.Name)
	}
	return kept, dropped
}

func toolMentioned(name, foldedSituation string) bool {
	if strings.Contains(foldedSituation, foldRunes(name)) {
		return true
	}
	if name == "calculator" {
		return containsAny(foldedSituation, "计算", "加法", "乘法", "除法", "subtract", "multiply", "divide")
	}
	return false
}

func providerUsage(messages []*schema.AgenticMessage) int {
	usage := 0
	for _, message := range messages {
		if message == nil || message.ResponseMeta == nil || message.ResponseMeta.TokenUsage == nil {
			continue
		}
		if message.ResponseMeta.TokenUsage.TotalTokens > usage {
			usage = message.ResponseMeta.TokenUsage.TotalTokens
		}
	}
	return usage
}

func splitKeepDialogue(messages []*schema.AgenticMessage) []*schema.AgenticMessage {
	_, dialogue := splitSysDialogue(messages)
	return dialogue
}

func dropHeuristicTools(tools []*schema.ToolInfo, situation string, used map[string]bool) (kept []*schema.ToolInfo, dropped []string) {
	sit := foldRunes(situation)
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		if persistentCapability(tool.Name) || used[tool.Name] {
			kept = append(kept, tool)
			continue
		}
		if isHeuristicTool(tool.Name) && !heuristicToolWanted(tool.Name, sit) {
			dropped = append(dropped, tool.Name)
			continue
		}
		kept = append(kept, tool)
	}
	return kept, dropped
}

func isHeuristicTool(name string) bool {
	return name == heuristicLoadSkill || name == heuristicCallTool
}

func heuristicToolWanted(name, foldedSituation string) bool {
	switch name {
	case heuristicLoadSkill:
		return containsAny(foldedSituation, "load_skill", "skill", "技能")
	case heuristicCallTool:
		return containsAny(foldedSituation, "call_tool", "maze", "迷宫", "makemaze", "墙", "listmaze", "runmaze")
	default:
		return true
	}
}

func extractThemeNotes(messages []*schema.AgenticMessage, situation string) string {
	terms := situationTerms(situation)
	if len(terms) == 0 {
		return ""
	}
	var picked []string
	seen := map[string]bool{}
	total := 0
	for _, message := range messages {
		for _, sentence := range splitSentences(messageText(message)) {
			if !sentenceHasTerm(sentence, terms) && !constraintSentence(sentence) {
				continue
			}
			sentence = strings.TrimSpace(sentence)
			if sentence == "" || seen[sentence] {
				continue
			}
			runes := utf8.RuneCountInString(sentence)
			if total+runes > maxThemeNoteRunes {
				continue
			}
			seen[sentence] = true
			picked = append(picked, sentence)
			total += runes
		}
	}
	if len(picked) == 0 {
		return ""
	}
	return "Earlier conversation notes kept for the current request:\n- " + strings.Join(picked, "\n- ")
}

func situationTerms(text string) []string {
	var terms []string
	var cjk []rune
	var ascii []rune
	flushCJK := func() {
		if len(cjk) >= 2 {
			terms = append(terms, string(cjk))
		}
		cjk = cjk[:0]
	}
	flushASCII := func() {
		if len(ascii) >= 3 {
			terms = append(terms, strings.ToLower(string(ascii)))
		}
		ascii = ascii[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushASCII()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			flushCJK()
			ascii = append(ascii, r)
		default:
			flushCJK()
			flushASCII()
		}
	}
	flushCJK()
	flushASCII()
	return terms
}

func splitSentences(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return r == '。' || r == '！' || r == '？' || r == '\n' || r == ';' || r == '；'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func sentenceHasTerm(sentence string, terms []string) bool {
	folded := foldRunes(sentence)
	for _, term := range terms {
		if utf8.RuneCountInString(term) < 2 {
			continue
		}
		if strings.Contains(folded, foldRunes(term)) {
			return true
		}
	}
	return false
}

func constraintSentence(sentence string) bool {
	return containsAny(sentence, "禁止", "必须", "工号", "冻结", "内部工具", "申请人")
}

func splitSysDialogue(messages []*schema.AgenticMessage) (sys, dialogue []*schema.AgenticMessage) {
	for _, message := range messages {
		if message != nil && message.Role == schema.AgenticRoleTypeSystem {
			sys = append(sys, message)
			continue
		}
		dialogue = append(dialogue, message)
	}
	return sys, dialogue
}

func wouldFit(messages []*schema.AgenticMessage, tools []*schema.ToolInfo, limit int) (bool, error) {
	got, err := countedTotal(context.Background(), messages, tools)
	if err != nil {
		return false, err
	}
	return got <= limit, nil
}

func sizeAll(messages []*schema.AgenticMessage) int {
	n := 0
	for _, message := range messages {
		k, err := messageBytes(message)
		if err != nil {
			continue
		}
		n += k
	}
	return n
}

func skillBytes(messages []*schema.AgenticMessage) int {
	n := 0
	for _, message := range messages {
		if !isSkillCatalogMessage(message) {
			continue
		}
		k, err := messageBytes(message)
		if err != nil {
			continue
		}
		n += k
	}
	return n
}

func toolNames(tools []*schema.ToolInfo) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			out = append(out, tool.Name)
		}
	}
	return out
}

func containsAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func foldRunes(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, text)
}

// Discovery and task control are capabilities, not topical context. Preserve
// their schemas even when this turn does not mention their names.
func persistentCapability(name string) bool {
	switch name {
	case "list_skills", "load_skill", "read_skill_resource", "call_tool", "spawn_subagent", "get_task", "list_tasks", "resume_task", "cancel_task", "update_plan":
		return true
	}
	return false
}
