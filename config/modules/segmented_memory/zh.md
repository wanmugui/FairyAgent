# 历史 KEY

以下 JSON 数组是上一轮及更早轮次的 active KEY。KEY 是从原始用户请求、模型回复或工具操作中原样选出的关键句，不是摘要；此区域不包含后端路径。

{{ SEGMENTED_MEMORY_INDEX }}

使用规则：

- 需要历史细节时，调用 `memory_search(query="原文 KEY")`；后端负责搜索根索引和历史分卷，并返回 `session_id`、`interaction_id`、`segment_id` 与 `memory://` 路径。
- 找到命中的最终段后用 `read_file` 读取。不要只根据 KEY 猜测细节，也不要把未激活的历史当成本轮事实。
- 当前用户要求高于历史记忆；冲突时服从当前要求。
- 发现工具结果、文件路径或事实已经过期时，优先调用 `memory_invalidate_segment` 标记失效；整轮交互错误时调用 `memory_invalidate_interaction`。不要为了纠正普通错误而物理删除记忆。
- `memory_delete_segment` 和 `memory_delete_interaction` 会物理删除记忆及 KEY 索引。仅当用户明确要求删除，或内容必须彻底移除时使用；它们不会删除对话附件和生成产物。
