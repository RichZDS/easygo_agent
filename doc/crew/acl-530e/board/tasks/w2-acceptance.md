# W-2 · 验收检查、证据、假绿

assignee: workshop · 状态: done（aa41e65；工头已签 51dadc9）· 契约：`board/contract-p1.md` §3、§4 的 evidence 部分、§5

## 现场 / 锚点
- **容器参数**：`docker.go` 的 `containerOptions`/`taskContainerOptions`，注意现有的资源、只读、UID 等参数。清理逻辑按 owner 标签回收容器，检查容器要沿用同一套标签。
- **执行收尾**：`service.go` 的 `execute`，在 CLI 结束后、写终态之前。
- **重启**：`service.go` 启动恢复时把 running 转成 interrupted 的逻辑。
- **类型**：`types.go` 的 `Workflow`、`Config`、`Run`。

## 修法
1. **配置**：`Workflow.acceptance` 的校验按契约 §5。只允许 Docker 模式，host 模式配了 acceptance 就启动失败。
2. **pack 检查脚本**：启动时把 `pack_dir/checks` 按内容哈希复制进 `<root>/packs/<sha>/checks`，权限按契约。复制前后都不能跟随符号链接，可以复用 `noSymlinks` 这一类工具函数。
3. **检查容器**：参数严格按契约 §5：工作区只读、`/pack/checks` 只读、不挂 relay、没有凭证、无网络、同样的限额、超时就杀。容器要有 owner 标签，能被现有清理逻辑回收。
4. **执行顺序**：
   1. CLI 成功、产物收集成功后，任务保持 running；
   2. 计算工作区树哈希；
   3. 依次跑检查，写 evidence 和 `acceptance` 事件；
   4. 最后才写终态。

   检查期间取消、重启的处理按契约 §5。
5. **证据落盘**：1 MiB 上限，超出截断并标记。新增 RPC `workshop.evidence`，列表和输出分页都按契约 §4。
6. **假绿**：判定按契约 §5，填写视图里的 `acceptance_state`、`false_green`、`evidence_count`。
7. **夹具**：给夹具镜像加一个静态二进制 `fixture-check`，它能按参数做这些事：
   - 检查某个文件的内容；
   - 故意失败；
   - 睡眠超时；
   - 尝试网络连接，并把结果写进输出；
   - 尝试写 `/workspace` 和 `/pack/checks`，并把结果写进输出。
8. **base 包**：在 `packs/base/checks/` 放一个通用检查 `npm-test.sh`，给真实 runtime 镜像用：`package.json` 有 test 脚本就离线跑 `npm test`，否则失败并说明原因。

## 明令不做
- 终审任务，以及跨任务挂载别人的工作区，都是 P2 的事。
- 不做检查的重跑接口。
- 不在检查里给模型转发。

## 测试
- **单测**：acceptance 配置校验；树哈希对内容、路径、类型的敏感性，以及 `.workshop-home` 被排除；证据截断；假绿的真值表；取消和重启时的状态；`workshop.evidence` 的分页和越权（其他 namespace 返回 not found）。
- **真实容器测试**，用夹具镜像：
  - 好产物 → passed；
  - 坏产物加上 submit 自报 pass → failed 且 false_green；
  - 检查超时 → failed 且 timed_out；
  - 检查里写 `/workspace` 和 `/pack/checks` 都失败；
  - 检查里连网失败；
  - 施工容器里改不到检查脚本，同时证明检查脚本来自 pack 副本，不在工作区里；
  - 检查期间取消 → cancelled；
  - 检查容器都被回收，没有残留。
- **全量**：`cd services/workshop && go test -race ./... && go vet ./...`。

## 交付
- 在 `feat/acl-workshop` 上单独提交一笔。
- worklog 报审，证据放在 `/tmp/crew/acl-530e/workshop/`。
- 然后 herdr-msg 通知工头。
