# web_search 端点限制与查询策略

本文件记录本机 web_search 的实测行为。**每次调研前先读这里**，不要重复用同一个查询撞同一堵墙。

最后实测：2026-10-04，全部结论来自本机 `go test -v` 真实调用，非推测。

## 一、两个硬限制

### 1. DuckDuckGo 通道在网络层不可用

DNS 解析结果异常：

```
html.duckduckgo.com → 2a03:2880:...face:b00c:0:25de
```

`2a03:2880::/32` 是 AS32934（Facebook），`face:b00c` 是 Facebook 段位标记。**这是 DNS 污染，不是代码问题，改代码无解。**

`web_search` 内部会按 provider 列表回退（`budget.providers`），所以 DDG 失败时通常会自动落到 Bing，不影响可用性——但不要指望它能兜底。

### 2. Bing 公共 HTML 端点不解析多词 AND 组合

**这是本机搜索质量的主要瓶颈。**

它对 `site:`、`intitle:`、多词布尔组合**不解析**，而是按最前面的主导词做松散匹配。后果是：长查询的召回结果几乎全是产品官网首页。

同一 provider、同一时刻的对照实测：

| 查询 | 结果 |
|---|---|
| `Blender 教程`（2 词） | ✅ beets3d.cn 建模教程、docs.blender.org 中文手册 |
| `Blender 角色建模 教程`（3 词 CJK） | ❌ 全是 blender.org 落地页 / 下载页 |
| `blender character modeling tutorial`（英文长词） | ❌ 全是 blender.org 落地页 |

**规律：两词查询能用，三词以上基本报废；中文长尾尤其严重。**

## 二、已落地的自动化对策

`websearch` 包内已实现查询放宽（`relax.go`），宿主重启后自动生效：

1. `answerIsWeak(sources, query)` — 判断这轮召回是否只命中了查询里最泛的词（要求"存在某条结果覆盖 ≥2 个 query term"）
2. 判定为弱召回时，用 `relaxedQueries(query)` 生成更短的变体重问
3. 变体 = 拉丁词 + 单个最短 CJK 段，按长度升序，最多 2 个
4. 变体结果**按原查询排序**（`selectSources(results, originalQuery, ...)`）——变体只改变"怎么问"，不改变"怎么判"
5. 重试结果**排在前面**（`mergeSearchSources(retrySources, sources, ...)`）

对 `Blender 角色建模 教程` 的实测效果：首轮 7 条官网落地页 → 触发重试 → 捞回 beets3d.cn 建模博客 + 官方中文手册。

## 三、手工查询策略（拿不到代码时的兜底）

如果结果仍然不相关，按顺序试：

1. **砍词**：把查询砍到两词。`Blender 角色建模 教程` → `Blender 教程`。这是实测最有效的一招。
2. **拆成多次**：主词一次、限定词一次，分别搜再合并。
3. **换语言试**：中文搜不到时试英文，或反过来。**注意这招对长尾词无效**（见上表第三行）。
4. **换固定入口**：技术类问题直接抓已知来源，不走搜索：
   - Blender 官方 manual：`docs.blender.org`
   - 中文社区：`beets3d.cn`（有建模实战长文）、`blendercn.org`
   - 美术参考：ArtStation、Behance
5. **换带 key 的服务**：本机端点确实不行时，这是唯一的根治方案（Brave / Serper / Tavily）。需要用户提供 key。

## 四、别做的事

- **不要靠反复调过滤阈值来救长查询。** 实测：任何严格到能滤掉官网落地页的阈值，同时也会把结果集清空，然后兜底逻辑又把全部结果放回来。过滤对"上游返回杂项"有效，对"上游只返回官网"无效。
- **不要把这归因成排序问题。** 这是召回问题：相关性排序再好，也排不出上游根本没返回的内容。
- **不要用测试夹具验证网络行为。** 单测里 mock 出来的结果集不代表真实端点行为。判断网络层改动是否有效，必须真实调用一次（`go test -v` 打一个 live 测试，或直接 `web_search`）。

## 五、代码位置

```
agent/internal/biz/tool/websearch/
├── relax.go          查询放宽与弱召回判定（本文件第二节）
├── relax_test.go     10 个纯函数用例
├── bing.go           区域/语言参数（已按查询内容自适应 CJK）
├── duckduckgo.go     同上（kl 参数曾硬编码 us-en）
└── tool.go           接线：Execute → retryWithRelaxedQuery
```

改动后必须跑：`go test ./... -count=1`（全量 9 包），然后重新编译并替换 `.tools/agent-loop-linux-x64`。

**注意：替换二进制 ≠ 生效。** 运行中的进程仍持有旧 inode（`/proc/<pid>/exe` 显示 `(deleted)`），必须重启宿主。`dev.mjs` 没有 watch，kill 掉不会自动拉起。
