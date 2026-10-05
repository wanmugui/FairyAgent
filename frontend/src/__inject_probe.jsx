// 浏览器验收探针：只做一件事——把「注入回显」的真实报文，喂给真实的
// splitFileContext + 真实的 MessageBubble，看用户到底看到什么。
// 组件、CSS、解析函数全部来自仓库源码，没有副本、没有 mock。
import React from 'react'
import { createRoot } from 'react-dom/client'
import MessageBubble from './features/chat/MessageBubble.jsx'
import { toInjectedUserMessage } from './api/chat.js'

// 1x1 红色 PNG，肉眼可见
const IMG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='

// App.jsx 注入时拼的原样报文
const files = [{ name: 'shot.png', path: '/tmp/shot.png', kind: 'image', image_url: IMG, size: 1 }]
const wire = '<file_context>\n' + JSON.stringify(files, null, 2) + '\n</file_context>\n' + '顺便看下这张图'

// 调 App.jsx injected_user 处理里真正用的那个函数（同一个，不是抄一遍）
const msg = toInjectedUserMessage(wire, { injected: true, injectId: 'probe-1', pending: false })
const text = msg.content
const parsed = msg.files

createRoot(document.getElementById('root')).render(
  <MessageBubble msg={msg} mode="full" />
)

// 给验收脚本留可断言的探针
window.__probe = {
  text,
  files: parsed,
  wireHadRawJson: wire.includes('"image_url"')
}
