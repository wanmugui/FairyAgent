---
name: react-best-practices
description: 来自 Vercel 工程团队的 React 与 Next.js 性能优化指南。在编写、评审或重构 React/Next.js 代码以确保最佳性能模式时使用本 skill。触发于涉及 React 组件、Next.js 页面、数据获取、bundle 优化或性能改进的任务。
metadata:
  short-description: "Codex skill: react-best-practices"
  tags:
    - react
    - frontend
    - performance
  triggers:
    - react
    - 组件
    - 前端优化
    - hooks
    - 渲染性能
  priority: 65
---

# Vercel React 最佳实践

由 Vercel 维护的 React 与 Next.js 应用综合性能优化指南。涵盖 8 个类别下的 64 条规则，按影响优先级排序，用以指导自动重构与代码生成。

## 应用时机

在以下情形参考这些指南：

- 编写新的 React 组件或 Next.js 页面
- 实现数据获取（客户端或服务端）
- 评审代码中的性能问题
- 重构已有的 React/Next.js 代码
- 优化 bundle 体积或加载时间

## 规则类别与优先级

| 优先级 | 类别                  | 影响度      | 前缀       |
| ------ | --------------------- | ----------- | ---------- |
| 1      | 消除瀑布流            | CRITICAL    | `async-`   |
| 2      | Bundle 体积优化       | CRITICAL    | `bundle-`  |
| 3      | 服务端性能            | HIGH        | `server-`  |
| 4      | 客户端数据获取        | MEDIUM-HIGH | `client-`  |
| 5      | Re-render 优化        | MEDIUM      | `rerender-`|
| 6      | 渲染性能              | MEDIUM      | `rendering-`|
| 7      | JavaScript 性能       | LOW-MEDIUM  | `js-`      |
| 8      | 高级模式              | LOW         | `advanced-`|

## 速查表

### 1. 消除瀑布流（CRITICAL）

- `async-defer-await` — 把 await 移到真正用到它的分支
- `async-parallel` — 对独立操作使用 `Promise.all()`
- `async-dependencies` — 对部分依赖使用 `better-all`
- `async-api-routes` — 在 API 路由中尽早发起 promise、靠后 await
- `async-suspense-boundaries` — 使用 Suspense 流式传输内容

### 2. Bundle 体积优化（CRITICAL）

- `bundle-barrel-imports` — 直接 import，避免 barrel 文件
- `bundle-dynamic-imports` — 用 `next/dynamic` 加载重组件
- `bundle-defer-third-party` — 在 hydration 之后加载分析/日志
- `bundle-conditional` — 仅在功能启用时加载模块
- `bundle-preload` — hover/focus 时预加载以提升感知速度

### 3. 服务端性能（HIGH）

- `server-auth-actions` — 像 API 路由那样鉴权 server actions
- `server-cache-react` — 使用 `React.cache()` 做 per-request 去重
- `server-cache-lru` — 用 LRU cache 做跨请求缓存
- `server-dedup-props` — 避免在 RSC props 中重复序列化
- `server-hoist-static-io` — 把静态 I/O（字体、logo）提升到模块层
- `server-serialization` — 最小化传给 client components 的数据
- `server-parallel-fetching` — 重构组件以并行化 fetch
- `server-after-nonblocking` — 使用 `after()` 做非阻塞操作

### 4. 客户端数据获取（MEDIUM-HIGH）

- `client-swr-dedup` — 用 SWR 自动去重请求
- `client-event-listeners` — 去重全局事件监听
- `client-passive-event-listeners` — 对滚动使用 passive 监听
- `client-localstorage-schema` — 版本化并最小化 localStorage 数据

### 5. Re-render 优化（MEDIUM）

- `rerender-defer-reads` — 不要订阅只在回调里用到的 state
- `rerender-memo` — 把昂贵的工作抽到 memoized 组件
- `rerender-memo-with-default-value` — 提升默认的非 primitive props
- `rerender-dependencies` — 在 effect 中使用 primitive 依赖
- `rerender-derived-state` — 订阅派生出的 boolean，而不是原始值
- `rerender-derived-state-no-effect` — 在 render 期间派生状态，而非 effect
- `rerender-functional-setstate` — 用函数式 setState 拿到稳定的回调
- `rerender-lazy-state-init` — 给 useState 传函数处理昂贵初始值
- `rerender-simple-expression-in-memo` — 简单 primitive 不必 memo
- `rerender-split-combined-hooks` — 拆分依赖独立的 hooks
- `rerender-move-effect-to-event` — 把交互逻辑放进事件处理函数
- `rerender-transitions` — 用 `startTransition` 处理非紧急更新
- `rerender-use-deferred-value` — 延迟昂贵渲染以保持输入响应
- `rerender-use-ref-transient-values` — 用 ref 存高频瞬态值
- `rerender-no-inline-components` — 不要在组件内嵌套定义组件

### 6. 渲染性能（MEDIUM）

- `rendering-animate-svg-wrapper` — 动画 div 包装层，而不是 SVG 元素
- `rendering-content-visibility` — 用 `content-visibility` 处理长列表
- `rendering-hoist-jsx` — 把静态 JSX 抽到组件外
- `rendering-svg-precision` — 降低 SVG 坐标精度
- `rendering-hydration-no-flicker` — 用内联脚本传递 client-only 数据
- `rendering-hydration-suppress-warning` — 抑制预期的失配警告
- `rendering-activity` — 用 Activity 组件做 show/hide
- `rendering-conditional-render` — 用三元运算符而非 `&&` 做条件
- `rendering-usetransition-loading` — 优先用 `useTransition` 做 loading 状态
- `rendering-resource-hints` — 用 React DOM 资源提示做预加载
- `rendering-script-defer-async` — script 标签使用 defer 或 async

### 7. JavaScript 性能（LOW-MEDIUM）

- `js-batch-dom-css` — 通过 class 或 cssText 批量改动 CSS
- `js-index-maps` — 用 Map 加速重复查找
- `js-cache-property-access` — 循环中缓存对象属性
- `js-cache-function-results` — 在模块层 Map 缓存函数结果
- `js-cache-storage` — 缓存 localStorage/sessionStorage 读取
- `js-combine-iterations` — 把多个 filter/map 合并到一个循环
- `js-length-check-first` — 先检查数组长度再做昂贵比较
- `js-early-exit` — 提前 return
- `js-hoist-regexp` — 把 RegExp 创建提升到循环外
- `js-min-max-loop` — 用循环做 min/max，而不是 sort
- `js-set-map-lookups` — 用 Set/Map 做 O(1) 查找
- `js-tosorted-immutable` — 用 `toSorted()` 保持不可变
- `js-flatmap-filter` — 用 `flatMap` 一次完成 map 与 filter

### 8. 高级模式（LOW）

- `advanced-event-handler-refs` — 把事件处理器存到 ref
- `advanced-init-once` — 整个 app 加载只初始化一次
- `advanced-use-latest` — 用 `useLatest` 拿到稳定的回调 ref

## 使用方法

阅读单独的规则文件获取详细解释与代码示例：

```
rules/async-parallel.md
rules/bundle-barrel-imports.md
```

每个规则文件包含：

- 为什么重要的简要说明
- 错误代码示例与解释
- 正确代码示例与解释
- 附加上下文与参考

## 完整编译文档

完整指南与所有规则展开见 `AGENTS.md`。