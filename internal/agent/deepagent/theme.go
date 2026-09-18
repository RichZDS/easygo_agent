package deepagent

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"easygo-agent/internal/prompt"
	"easygo-agent/internal/skill"

	"github.com/cloudwego/eino/schema"
)

// ThemedTurn is one side of the 玄枢台账 release-window dialogue.
type ThemedTurn struct {
	User      string
	Assistant string
}

const (
	themeToolName = "玄枢台账"
	themeStaffID  = "EG-7741"
	themeFriday   = "周五"
)

// ThemedReleaseDialogue is a single real theme: 玄枢台账 Friday production freeze.
// Each turn is independently long and unique. It is not one paragraph repeated.
func ThemedReleaseDialogue() []ThemedTurn {
	return []ThemedTurn{
		{
			User: "先把发布约束钉死，后面每轮都按这个执行。" +
				"我们内部唯一的变更台账叫「玄枢台账」，不是 Jira，也不是 Excel。" +
				"我的工号是 EG-7741，所有变更单的申请人栏必须填这个工号，不能写成我的中文名。" +
				"绝对禁止在周五把变更推到生产：周五 00:00 到周六 08:00 生产窗口锁死，热修也要先改成变更单状态「冻结」，等周一早会解冻。" +
				"变更单模板固定六段：背景、影响面、回滚、观察指标、值班人、玄枢台账编号。" +
				"值班人本周是林可（主）和赵衡（副）；观察指标至少包含错误率、P99、登录成功率、台账写入延迟。" +
				"回滚要求五分钟内能切回上一版本镜像，禁止只写「再发一版」。" +
				"影响面要写清楚读路径、写路径、缓存和下游账单服务，不能只写「内部工具」。" +
				"背景里必须点名这次是为了玄枢台账 v2.3 的审计字段上线，而不是日常杂项。" +
				"如果有人在周五提交生产流水线，请直接拒绝并引用这条冻结令，不要商量例外。",
			Assistant: "已确认三条硬约束：内部工具全称是玄枢台账；申请人工号 EG-7741；周五至周六早八生产冻结，热修也要先冻结。" +
				"变更单六段模板、值班林可/赵衡、回滚五分钟、观察四项指标，我都会在后续讨论里沿用。" +
				"不会把台账写成别的系统名，也不会把工号改成中文名。" +
				"背景只写 v2.3 审计字段，影响面要覆盖读写路径、缓存和账单，回滚必须是切镜像而不是再发一版。" +
				"如果周五有人提交生产流水线，我会按冻结令直接拒绝，不讨论例外。",
		},
		{
			User: "接下来把玄枢台账 v2.3 的表结构讲清楚，这一轮只谈字段，不谈发布窗口。" +
				"主表 ledger_entry 需要：entry_id（ULID）、staff_id、change_title、env、status、freeze_flag、rollback_ref、observer_json、created_at、updated_at。" +
				"staff_id 必须能存 EG-7741 这种字母数字工号，长度 16，建普通 btree，不要哈希索引。" +
				"status 枚举：draft、review、frozen、shipped、rolled_back；周五如果有人想 shipped，应用层要先看 freeze_flag。" +
				"observer_json 里预置 error_rate、p99_ms、login_ok、ledger_write_ms 四个键，缺一不可。" +
				"另加 audit_event 表：event_id、entry_id、actor_staff_id、action、before_json、after_json、at。" +
				"actor_staff_id 同样是工号，写入时校验格式，避免再出现用昵称审计的情况。" +
				"缓存键建议 ledger:entry:{entry_id} 和 ledger:open:{staff_id}，TTL 90 秒，失效要双删。" +
				"账单下游消费的是 shipped 事件，字段只要 entry_id、env、staff_id，不要把 freeze_flag 泄漏到账单。" +
				"请按上面的字段给出建表要点，并标明哪一列承载工号。",
			Assistant: "ledger_entry.staff_id 和 audit_event.actor_staff_id 两列承载工号，格式按 EG-7741 这类值校验。" +
				"freeze_flag 与 status=frozen 一起挡住周五误发。observer_json 四个键我会写进检查清单。" +
				"账单事件只留 entry_id/env/staff_id。缓存双删和 ULID 主键按你的方案记下来了。" +
				"主表其余列按 ULID、标题、环境、状态、回滚引用、时间戳落地；audit_event 保存前后 JSON。" +
				"这张表仍然属于玄枢台账，不会改名到其他系统。",
		},
		{
			User: "解释一下为什么会有周五冻结。上周五年夜把一版只改文案的台账前端推上了生产，结果把 observer_json 的默认值覆盖成空对象。" +
				"错误率没动，但 ledger_write_ms 从 40ms 跳到 900ms，登录成功率短暂掉到 97.2%。" +
				"当时值班是实习同学代班，变更单背景写成「修错别字」，影响面填「无」，回滚写「不明」。" +
				"玄枢台账里这条记录甚至没有工号，申请人是空的，事后才补成 EG-7741。" +
				"根因有三条：前端把 JSON 默认值写成 {}；网关没校验 observer 四键；周五没有冻结开关。" +
				"时间线：18:07 合并，18:11 生产变红，18:19 人工回滚镜像，18:31 延迟恢复。" +
				"用户投诉主要来自账单页打开慢，不是登录失败本身。" +
				"复盘结论必须写进台账：周五禁发、工号必填、observer 四键校验、代班不得跳过副值班确认。" +
				"请用事故叙述的方式复述，不要改工号，也不要把内部工具换成别的名字。",
			Assistant: "复盘记下了：周五夜的文案发布清空了 observer_json，写入延迟到 900ms，18:19 回滚。" +
				"申请人当时缺失，事后补 EG-7741。冻结令、工号必填、四键校验、副值班确认，四条都会进玄枢台账的事故页。" +
				"不会把这件事写成别的系统故障。" +
				"时间线 18:07 合并、18:11 变红、18:19 回滚镜像、18:31 延迟恢复；投诉来自账单页变慢。" +
				"代班跳过副值班确认是流程缺口，后续清单会把它写成硬步骤。",
		},
		{
			User: "现在排玄枢台账 v2.3 的发布清单，目标窗口是下周三 10:00，明确避开周五。" +
				"清单顺序：1) 迁移脚本在预发跑两遍；2) 工号格式校验的单元测试；3) observer 四键的契约测试；4) 冻结开关的集成测试，模拟周五 23:50 的请求应返回 423；" +
				"5) 回滚演练，切回 v2.2 镜像并核对 ledger_write_ms；6) 账单消费者兼容性，确认收不到 freeze_flag；" +
				"7) 林可签字、赵衡副签；8) 变更单申请人填 EG-7741；9) 玄枢台账编号预占 XT-2409-17。" +
				"观察期 90 分钟，指标阈值：错误率 < 0.3%，P99 < 250ms，登录成功率 > 99.5%，ledger_write_ms < 80。" +
				"如果预发迁移第二遍有 drift，停止发布，不要自行修数据。" +
				"冻结开关的测试必须用真实时钟翻到周五，而不是只改配置项注释。" +
				"请把这份清单整理成可执行步骤，并再次声明周五不能用。",
			Assistant: "v2.3 放在下周三 10:00，清单九步按你的顺序执行，编号 XT-2409-17，申请人 EG-7741。" +
				"周五 423 测试、回滚演练、账单不收 freeze_flag 都会做。预发 drift 就停。周五窗口仍然禁用。" +
				"观察期 90 分钟，错误率、P99、登录成功率、ledger_write_ms 按你给的阈值盯盘。" +
				"冻结开关测试用真实周五时钟，不靠改注释。林可主签、赵衡副签。",
		},
		{
			User: "这一轮只谈索引和 SQL，主题仍是玄枢台账，不要跑题到迷宫或计算器。" +
				"ledger_entry 计划加 (staff_id, created_at desc) 复合索引 idx_ledger_staff_created，给「我的工单」页用。" +
				"audit_event 加 (entry_id, at desc) 叫 idx_audit_entry_at。" +
				"部分索引：WHERE freeze_flag = true 的 idx_ledger_frozen，方便周五值班把冻结单拉出来。" +
				"不要在 status 上建哈希索引，枚举基数太低。" +
				"迁移用 CONCURRENTLY，禁止在事务里建索引；预发先建，生产窗口同样避开周五。" +
				"统计信息要在索引后 ANALYZE ledger_entry, audit_event。" +
				"解释器如果选了 seq scan，把 random_page_cost 从 4 调到 1.2 再看，不要先加 hint。" +
				"我担心 staff_id 用 bytea；保持 varchar(16) 即可，EG-7741 这种值已经够用。" +
				"请给出迁移步骤和回滚（DROP INDEX CONCURRENTLY），并提醒申请人仍是 EG-7741。",
			Assistant: "索引三套按复合、时间倒序、冻结部分索引来。CONCURRENTLY 建和删，不进事务，ANALYZE 随后。" +
				"staff_id 维持 varchar(16) 以保存 EG-7741。生产迁移同样不排周五。申请人还是 EG-7741，系统还是玄枢台账。" +
				"seq scan 时先把 random_page_cost 调到 1.2，不加 hint。回滚用 DROP INDEX CONCURRENTLY。" +
				"冻结部分索引只为值班拉出 freeze_flag=true 的单，不改变周五禁发本身。",
		},
		{
			User: "根据前面关于玄枢台账发布窗口、事故复盘、v2.3 清单和索引迁移的全部讨论，请回答三个必须来自本会话的事实：" +
				"1) 我的工号是什么？2) 内部工具的全称是什么？3) 周五能不能把变更推到生产？" +
				"回答时不要引入新的工号，不要把台账改成其他产品名，不要把冻结令改成「看情况」。" +
				"可以顺便点一下 XT-2409-17 和下周三窗口，但三个事实必须原样保留。" +
				"如果上下文被压缩过，也优先保留这三条，而不是保留 SQL 细节或缓存键。" +
				"这是当前这一轮真正要办的事：核对身份、工具名和周五冻结，再决定能不能签字。" +
				"签字前再核一遍：申请人栏只能是 EG-7741，系统名只能是玄枢台账，周五到周六早八的生产窗口保持锁死，热修也要先改成冻结。" +
				"观察指标、缓存键、复合索引都可以丢，但这三条约束丢了我就无法在变更单上签名。" +
				"请按会话里已经确认的原文回答，不要概括成「内部系统」或「某工号」。",
			Assistant: "",
		},
	}
}

// ThemedReleaseMessages builds the over-budget conversation: system catalog +
// alternating user/assistant turns, ending on the current user probe.
func ThemedReleaseMessages(instruction string) []*schema.AgenticMessage {
	dialogue := ThemedReleaseDialogue()
	out := make([]*schema.AgenticMessage, 0, 2+len(dialogue)*2)
	if strings.TrimSpace(instruction) != "" {
		out = append(out, schemaSystem(instruction))
	}
	for i, turn := range dialogue {
		if strings.TrimSpace(turn.User) != "" {
			out = append(out, schemaUser(turn.User))
		}
		if i == len(dialogue)-1 {
			break
		}
		if strings.TrimSpace(turn.Assistant) != "" {
			out = append(out, schemaAssistant(turn.Assistant))
		}
	}
	return out
}

// ThemedUserTurns returns only the user sides, for live multi-turn eval.
func ThemedUserTurns() []string {
	dialogue := ThemedReleaseDialogue()
	out := make([]string, 0, len(dialogue))
	for _, turn := range dialogue {
		out = append(out, turn.User)
	}
	return out
}

// RejectRepeatedFiller fails if any turn is 一段话*n padding.
func RejectRepeatedFiller(texts []string) error {
	if len(texts) < 2 {
		return fmt.Errorf("themed corpus needs multiple turns, got %d", len(texts))
	}
	seen := map[string]int{}
	for i, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" {
			return fmt.Errorf("turn %d is empty", i)
		}
		if utf8.RuneCountInString(text) < 360 {
			return fmt.Errorf("turn %d is too short to be a long themed turn (%d runes)", i, utf8.RuneCountInString(text))
		}
		if n := repeatedRuneBlock(text, 24, 6); n > 0 {
			return fmt.Errorf("turn %d looks like 一段话*n (block repeated %d times)", i, n)
		}
		if prev, ok := seen[text]; ok {
			return fmt.Errorf("turn %d duplicates turn %d", i, prev)
		}
		seen[text] = i
	}
	return nil
}

func repeatedRuneBlock(text string, blockRunes, minRepeat int) int {
	runes := []rune(text)
	if len(runes) < blockRunes*minRepeat {
		return 0
	}
	block := string(runes[:blockRunes])
	if strings.TrimSpace(block) == "" {
		return 0
	}
	count := strings.Count(text, block)
	if count >= minRepeat {
		return count
	}
	return 0
}

// NewThemedFitRequest builds the over-budget 玄枢台账 fixture with heuristic
// skill catalog text and discovery schemas. Reserve the persistent tool cost
// and half of the remaining composition so the rewrite must fire.
func NewThemedFitRequest() (FitRequest, error) {
	instruction := FatHeuristicInstruction()
	messages := ThemedReleaseMessages(instruction)
	tools := FatHeuristicTools()
	before, err := Measure(messages, tools)
	if err != nil {
		return FitRequest{}, err
	}
	limit := before.Tools + (before.Total-before.Tools)/2
	if limit < 4000 {
		limit = 4000
	}
	return FitRequest{Messages: messages, Tools: tools, Limit: limit}, nil
}

// FatHeuristicInstruction is the production catalog plus unused long rows.
func FatHeuristicInstruction() string {
	catalog := fallbackCatalog()
	for _, root := range []string{"skills", filepath.Join("..", "..", "skills"), filepath.Join("..", "..", "..", "skills")} {
		if lib, err := skill.Open(root); err == nil {
			catalog = lib.CatalogPrompt()
			break
		}
	}
	catalog += "\n| `legacy-csv-import` | Use when the user wants to import legacy CSV dumps from the 2019 billing warehouse, including delimiter guessing, header repair, duplicate customer folding, and dry-run reports before writing the warehouse. |\n"
	catalog += "| `weekly-status-mail` | Use when the user wants a weekly status mail with sprint burn-down, blocker list, customer quotes, and a screenshot checklist for the Friday demo that is unrelated to production freezes. |\n"
	catalog += "| `office-seating-chart` | Use when the user wants to redraw the office seating chart, monitor allocations, and visitor badges, none of which is the ledger release process. |\n"
	return prompt.WithSkillCatalog(prompt.SystemPrompt, catalog)
}

// FatHeuristicTools is load_skill + call_tool + calculator with long schemas.
func FatHeuristicTools() []*schema.ToolInfo {
	longMaze := "Hidden maze dispatcher. Call only after loading playing-maze. Arguments include name plus a JSON object for listmaze, detailmaze, runmaze, or makemaze. The grid uses 墙 路 起 终 and is irrelevant to ledger releases."
	longSkill := "Load one specialized skill body from the on-disk catalog into this run. Pass the exact Name from the Directory. This schema is large because each catalog entry used to be inlined here, which wastes context on turns that are about release freezes rather than skills."
	return []*schema.ToolInfo{
		{Name: heuristicLoadSkill, Desc: longSkill, ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {Type: schema.String, Desc: "Exact catalog skill name such as playing-maze or strict-arithmetic, never a path fragment", Required: true},
		})},
		{Name: heuristicCallTool, Desc: longMaze, ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name":      {Type: schema.String, Desc: "Hidden tool name from a loaded skill such as makemaze", Required: true},
			"arguments": {Type: schema.Object, Desc: "JSON object for that hidden maze tool, including a 2D grid of 墙 路 起 终 when creating a maze"},
		})},
		{Name: "calculator", Desc: "Perform deterministic addition, subtraction, multiplication, or division.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"operation": {Type: schema.String, Desc: "add subtract multiply divide", Required: true},
			"a":         {Type: schema.Number, Desc: "left operand", Required: true},
			"b":         {Type: schema.Number, Desc: "right operand", Required: true},
		})},
	}
}

func fallbackCatalog() string {
	return "# Skill Catalog\nCall load_skill before specialized procedures.\n\n## Directory\n\n| Name | Use when |\n| --- | --- |\n| `playing-maze` | Use when the user wants a 迷宫 of 墙 路 起 终 |\n| `strict-arithmetic` | Use when the user asks for a numeric calculation |\n"
}
