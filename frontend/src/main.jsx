import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import VoicePage from './pages/VoicePage'
import './styles/App.css'
import './styles/themes.css'
import './styles/base-skin.css'
import './styles/app-shell.css'
import './styles/zzz-skin.css'
import './styles/voice.css'
import './styles/motion.css'
import './styles/typography.css'
// 必须放在所有皮肤之后：trace 卡片/时间轴层靠"同特异性 + 更后出现"覆盖主题样式
import './styles/trace-cards.css'
import './styles/trace-console.css'
import { initTheme } from './theme'

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
    window.location.replace('/login.html?next=' + encodeURIComponent(next))
  }
  return response
}

function getRoute() {
  if (window.location.hash.startsWith('#/voice')) return 'voice';
  if (window.location.pathname.replace(/\/+$/, '') === '/voice') return 'voice';
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
  return route === 'voice' ? <VoicePage /> : <App />;
}

ReactDOM.createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <Root />
  </React.StrictMode>,
)
