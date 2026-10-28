import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import VoicePage from './pages/VoicePage'
import TimelinePage from './pages/TimelinePage'
import { appBase, appPathname } from './utils/appBase'
import './styles/App.css'
import './styles/themes.css'
import './styles/base-skin.css'
import './styles/app-shell.css'
import './styles/usage-panel.css'
import './styles/provider-panel.css'
import './styles/memory-panel.css'
import './styles/fonts.css'
import './styles/zzz-skin.css'
import './styles/voice.css'
import './styles/motion.css'
import './styles/typography.css'
// 必须放在所有皮肤之后：trace 卡片/时间轴层靠"同特异性 + 更后出现"覆盖主题样式
import './styles/trace-cards.css'
import './styles/trace-console.css'
// 全局缩放：必须最后引入 —— #root 的 zoom/height 规则要能覆盖前面所有样式。
import './styles/ui-zoom.css'
import { initTheme } from './theme'

/* 全局缩放：CSS 的 zoom 属性不支持 calc()，
   必须由 JS 算出缩放比再写进 --ui-zoom。
   规则：内容按 base×base 的方形基准排版，视口宽高任一不足就整体缩小。
   宽高必须同时约束：只按高度算的话，竖屏手机（宽 390、高 844）会算出
   scale = 844/1080 = 0.78，于是布局视口宽变成 390/0.78 = 500px，却要装下
   按 1080 宽排版的内容 —— 横向溢出，底部输入框被挤出屏幕。

   窄屏（≤900px）完全不缩放，直接返回 1：
     手机端有完整的响应式布局（900 / 760 / 720 / 430px 四档断点，
     侧栏会折叠成抽屉、字号和间距另有安排），
     再叠一层 zoom 会把内容压到看不清（实测 390px 宽的手机上
     算出的 scale 只有 0.361，主人反馈手机端整个页面是白的）。 */
const UI_ZOOM_MIN_WIDTH = 901

function applyUiZoom() {
  const base = parseFloat(
    getComputedStyle(document.documentElement).getPropertyValue('--ui-zoom-base')
  ) || 1080
  const vw = window.innerWidth || base
  const vh = window.innerHeight || base
  const scale = vw < UI_ZOOM_MIN_WIDTH
    ? 1
    : Math.min(1, vw / base, vh / base)
  document.documentElement.style.setProperty('--ui-zoom', String(scale))
}

applyUiZoom()
window.addEventListener('resize', applyUiZoom)

initTheme()

// The API enforces a login, but the SPA knows nothing about that: it starts up,
// every /api call comes back 401, and it renders an empty shell. On a remote
// device that is indistinguishable from the app being broken - you just get a
// black page.
//
// Intercepting 401 once here covers every caller instead of patching each module,
// and carries the current location through so the user lands back where they were.
// The pathname check keeps a failed login POST from looping.
const originalFetch = window.fetch.bind(window)
window.fetch = async (...args) => {
  const response = await originalFetch(...args)
  if (response.status === 401 && !window.location.pathname.endsWith('/login.html')) {
    const next = window.location.pathname + window.location.search + window.location.hash
    // 保留 /agent 前缀：线上是 Caddy 挂在这个前缀下的（handle /agent/*），
    // 直接跳 /login.html 会被打回根域名，登录页就找不到了。
    window.location.replace(appBase() + '/login.html?next=' + encodeURIComponent(next))
  }
  return response
}

// 线上通过 Caddy 把 /agent/* 剥掉前缀再交给 Vite（见 infra/cloudflared/Caddyfile
// 的 `handle /agent/*` + `uri strip_prefix /agent`），但浏览器里的
// location.pathname 仍然带着 /agent 前缀。路由判断和登录跳转都要先剥掉它，
// 否则 /agent/voice 会被当成普通页面渲染，/agent/login.html 也找不到。
function getRoute() {
  if (window.location.hash.startsWith('#/voice')) return 'voice';
  if (appPathname() === '/voice') return 'voice';
  // 视频时间轴是一个四轨全屏编辑器，和对话无关（TimelineEditor 只接 onClose，
  // 草稿自己从 storage 读），所以给它独立路由而不是塞在聊天顶栏里。入口挂在
  // 根域名的导航页 landing/index.html 上。
  if (window.location.hash.startsWith('#/timeline')) return 'timeline';
  if (appPathname() === '/timeline') return 'timeline';
  return 'app';
}

function Root() {
  const [route, setRoute] = React.useState(getRoute());
  React.useEffect(() => {
    const sync = () => setRoute(getRoute());
    window.addEventListener('hashchange', sync);
    return () => window.removeEventListener('hashchange', sync);
  }, []);
  // The workbench skins only apply to the chat surface; the immersive /voice
  // page keeps its own art direction.
  React.useEffect(() => {
    if (route === 'voice') document.documentElement.setAttribute('data-theme', 'voice');
    else initTheme();
  }, [route]);
  if (route === 'timeline') return <TimelinePage />;
  return route === 'voice' ? <VoicePage /> : <App />;
}

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <Root />
  </React.StrictMode>,
)
