import React from 'react'
import TimelineEditor from '../features/timeline/TimelineEditor'
import { appBase } from '../utils/appBase'

/**
 * 视频时间轴的独立页面（/timeline）。
 *
 * 为什么独立成路由而不是留在聊天顶栏：这是个四轨全屏编辑器（缩放条、轨道
 * 切换、素材预览），放进 42px 的图标按钮里既放不下也不该放；而且在手机上
 * 那个按钮的裸文字「时间轴」会被压成竖排三行。它也不依赖对话——草稿自己
 * 从 storage 读，所以拆出去没有副作用。
 *
 * 入口挂在根域名的导航页（landing/index.html）上，和 /agent、/filemanage
 * 同一套 session。
 */
export default function TimelinePage() {
  // 关闭时回到 Agent 控制台，而不是空白页：独立路由最怕的就是关掉之后
  // 停在没有去处的 URL 上，浏览器返回键还得按两下。
  const leave = () => {
    if (window.history.length > 1 && document.referrer) window.history.back()
    // 保留线上 Caddy 挂载的 /agent 前缀：不带前缀直接跳根路径会被打回域名根。
    else window.location.href = appBase() + '/'
  }
  return (
    <div className="timeline-page">
      <TimelineEditor onClose={leave} />
    </div>
  )
}
