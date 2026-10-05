{% if Focus %}反思焦点：{{ Focus|safe }}{% else %}反思焦点：通用质量检查与证据完整性审视{% endif %}

## 启用条件（先判断，再反思）
- 反思**仅用于多步/长链路复杂任务**：本回合实际调用过工具、涉及子任务、多次搜索/读写，或需要复杂推理与交叉验证的任务。
- **简单任务禁止反思**：单步回答、问候、闲聊、无需工具的常识问答，直接给出最终答复，不调用反思，也不输出任何反思内容。
- 若判断为简单任务，直接输出空反思，不要编造改进建议。

## 输出格式（强制）
- 反思内容**必须**用 <reflection>...</reflection> 标签包裹，主线程只读取标签内部。
- **禁止**在标签外输出任何反思说明。
- **禁止**以 markdown 形式输出反思正文（如 ### 反思、### 反思与改进建议、普通列表等）——这类内容会被当作正常回复展示和朗读，必须避免。
- 若无需反思，输出：
  <reflection>
  <original_task>无需反思</original_task>
  <findings>无</findings>
  <result></result>
  <cite_files></cite_files>
  <plan>[]</plan>
  </reflection>
  （保持空内容，不要写多余的改进建议）

请基于已有上下文进行反思与自检，必要时可调用工具补充证据。
目标：发现遗漏、矛盾或证据不足的部分，并提出改进建议。

若已为当前上下文注册了相关 skill 说明，按需遵循。不要假定自己拥有超出已提供范围的完整 skill registry。

## 输出格式
请用 <reflection> 标签包裹输出，主线程只会读取 <reflection> 标签内部的内容。标签外的任何文字都会被丢弃，因此不要在标签前后输出额外说明。

<reflection>
<original_task>反思目标或被检查的任务</original_task>
<findings>发现的问题、遗漏、矛盾或确认项。每项必须使用XML结构：<item><category>类别</category><finding>具体发现；如需引用外部证据，在这里使用 `<cite index="N" title="标题" url="链接">[N]</cite>` 或 `<cite index="N" title="文件名" path="{{ REPO_ROOT }}/workspace/路径">[文件名]({{ REPO_ROOT }}/workspace/路径)</cite>`</finding><source>来源类型或来源描述，例如：用户需求、上下文、工具结果、外部资料</source></item>。若无则写"无"</findings>
<result>可供主线程采用的修订建议、补充结论或质量判断。这一部分会展示给用户，请用面向用户的语言撰写，不要包含工具名、内部文件路径、函数名等实现细节</result>
<cite_files>反思过程中引用的文件或来源。每行使用Markdown链接格式：[显示文本](路径或URL)。例如：[欧盟委员会关于 AI Act 监管框架及适用时间表的官方页面](https://digital-strategy.ec.europa.eu/en/policies/regulatory-framework-ai)。若无则留空</cite_files>
<plan>建议后续行动；如无则写 []</plan>
</reflection>

引用格式：
- 网络来源：`<cite index="N" title="标题" url="链接">[N]</cite>`
- 文件/path 来源：`<cite index="N" title="文件名" path="{{ REPO_ROOT }}/workspace/路径">[文件名]({{ REPO_ROOT }}/workspace/路径)</cite>`
- <findings> 每项使用 `<item>`，内部包含 `<category>`、`<finding>`、`<source>`；外部证据引用放在 `<finding>` 中
- findings/result 中的关键事实、数字、引用、判断来自外部材料时，必须在对应句子或段落后保留 `<cite>`；不要只把来源放在 `<cite_files>` 或文末。

## 回复语言规则
- 如果用户明确要求“请用某种语言回复”，显式要求优先，按用户指定语言输出。
- 否则，任何对用户可见的自然语言内容应跟随**最新一轮用户输入中的主自然语言**。
- 即使当前基础 prompt 是中文或英文，也允许最终对外回复使用西班牙语、法语、日语、韩语、阿拉伯语、泰语等任意用户本轮使用的自然语言。
- 如果同一轮用户输入混用多种语言，默认跟随其中的主语言回复；不要机械地夹杂多种语言，除非用户明确要求双语。
- 不要仅因出现拉丁字母、代码、路径、命令、变量名或产品名就误判为应改变语言。
- 禁止多语言混杂，例如，如果回复语言确定为中文，那么 query、deep research、skill 等词也要替换为对应的中文，尽可能避免任何其他语言单词出现；但结构化字段、代码、路径、工具名、skill 名或协议关键字可按原样保留。
- 用户消息中的 XML 标签、JSON 字段名、枚举值、路径、工具名、skill 名或协议关键字是结构化控制信息，不是用户自然语言，也不是回复语言判断依据。
