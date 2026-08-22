# eino agent框架
                    adk.TypedAgent[M]
                    Agent 基础接口
                           │
          ┌────────────────┼────────────────┐
          │                │                │
          ▼                ▼                ▼
 ChatModelAgent      Workflow Agent      Custom Agent
  核心自主 Agent       编排 Agent          自定义实现
          │                │
          │          ┌─────┼─────┐
          │          ▼     ▼     ▼
          │        Seq   Parallel Loop
          │
          │
          └──────────────┐
                         │
                         ▼
                    Prebuilt Agent
                ┌────────┼──────────┐
                ▼        ▼          ▼
              Deep   Supervisor  PlanExecute
              
TypedAgent[M]
│
├── TypedChatModelAgent[M]               ← 最核心
│      │
│      ├── ChatModelAgent
│      │
│      └── DeepAgent                     ← 基于它增强
│
├── WorkflowAgent                        ← 内部统一实现
│      │
│      ├── SequentialAgent
│      ├── ParallelAgent
│      └── LoopAgent
│             │
│             └── PlanExecute 会使用
│
├── Supervisor                           ← Transfer 模式包装
│
├── PlanExecute
│      └── Sequential(
│             Planner,
│             Loop(
│                 Executor,
│                 Replanner
│             )
│          )
│
└── 你自己实现 TypedAgent / Agent
# Agnet 的简介
ChatModelAgent = 基础自主 Agent

DeepAgent = 加了规划、文件、Shell、SubAgent 的 ChatModelAgent

Sequential / Parallel / Loop = Agent 编排器

Plan-Execute = Sequential + Loop 组合出的规划型 Agent

Supervisor = Transfer 型中央调度，多 Agent，但现在不推荐

AgentAsTool = 当前推荐的多 Agent 协作方式

AgenticModel = Model，不是 Agent

flow/react = 老 ReAct 实现，新项目优先 ADK ChatModelAgent