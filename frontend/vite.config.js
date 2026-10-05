import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const apiPort = process.env.AGENT_API_PORT || '8081'
const voicePort = process.env.FAIRY_VOICE_PORT || '8787'
const voiceWsPort = process.env.FAIRY_VOICE_WS_PORT || '8788'
const frontendPort = Number(process.env.FAIRY_FRONTEND_PORT || '5173')
const instanceId = process.env.FAIRY_INSTANCE_ID || ''
const repoRoot = process.env.FAIRY_REPO_ROOT || ''

// Network binding is deliberately opt-in.
//
// Fairy's frontend is not a read-only dashboard: from it the agent runs shell
// commands, drives the mouse and keyboard, and reads or writes files. There is
// no authentication anywhere in the stack, so binding to every interface hands
// that capability to anyone who can reach this machine.
//
//   (unset)                 -> loopback only, the safe default
//   FAIRY_LAN_BIND=1        -> every interface, reachable at http://<lan-ip>:5173
//   FAIRY_LAN_BIND=0.0.0.0  -> same thing spelled out
//   FAIRY_LAN_BIND=<addr>   -> one specific interface
//   FAIRY_LAN_HOSTS=a,b     -> extra Host header names to accept (needed when
//                              reaching it by hostname rather than by IP)
const lanBind = (process.env.FAIRY_LAN_BIND || '').trim()
const lanHosts = (process.env.FAIRY_LAN_HOSTS || '')
    .split(',')
    .map(value => value.trim())
    .filter(Boolean)
let bindHost
if (lanBind) {
    bindHost = lanBind === '1' || lanBind === '0.0.0.0' ? true : lanBind
}

// Hostnames that reach this dev server through the Cloudflare tunnel.
// cloudflared forwards the public hostname untouched, so Vite sees it verbatim
// as the Host header and answers 403 "Blocked request" unless the name is on
// the allow-list. LAN addresses are covered by FAIRY_LAN_HOSTS above; these are
// the names that arrive from the public internet.
//
//   (unset)                  -> the tunnel's own public hostname
//   FAIRY_PUBLIC_HOSTS=a,b   -> override with a comma separated list
//
// This only relaxes Vite's Host allow-list. It adds no authentication: the dev
// server drives the agent, so whoever can reach the tunnel reaches the machine.
const publicHosts = (process.env.FAIRY_PUBLIC_HOSTS || 'example.com')
    .split(',')
    .map(value => value.trim())
    .filter(Boolean)
const allowedHosts = [...new Set([...lanHosts, ...publicHosts])]

function fairyIdentityPlugin() {
  return {
    name: 'fairy-instance-identity',
    configureServer(server) {
      server.middlewares.use('/__fairy_identity', (req, res) => {
        const requestUrl = new URL(req.url || '', 'http://127.0.0.1')
        if (!instanceId || requestUrl.searchParams.get('token') !== instanceId) {
          res.statusCode = 404
          res.end('not found')
          return
        }
        const body = JSON.stringify({
          service: 'fairy-frontend',
          instance_id: instanceId,
          repository_root: repoRoot,
        })
        res.statusCode = 200
        res.setHeader('Content-Type', 'application/json; charset=utf-8')
        res.setHeader('Cache-Control', 'no-store')
        res.setHeader('Content-Length', Buffer.byteLength(body))
        res.setHeader('X-Fairy-Instance', instanceId)
        res.end(body)
      })
    },
  }
}

export default defineConfig({
  plugins: [react(), fairyIdentityPlugin()],
  server: {
    port: frontendPort,
    strictPort: true,
    ...(bindHost ? { host: bindHost } : {}),
    ...(allowedHosts.length ? { allowedHosts } : {}),
    proxy: {
      '/api': {
        target: `http://127.0.0.1:${apiPort}`,
        changeOrigin: true,
        xfwd: true,
      },
      // The auth endpoints live on the API server, not in the SPA. Without this
      // entry Vite answers /auth/login from the static handler and returns 404,
      // so the login form renders but can never submit - the failure only shows
      // up when the flow is exercised through the dev server, not when the API is
      // called directly.
      // 文件管理器：与 Caddy 的 /filemanage、/fm-assets 规则保持一致。
      // 少了这两条，走 5173（局域网/ZeroTier）时 /filemanage 会落到 SPA 回退页，
      // 看到的是前端首页而不是文件管理器。
      '/filemanage': {
        target: `http://127.0.0.1:${apiPort}`,
        changeOrigin: true,
        xfwd: true,
        rewrite: path => path.replace(/^\/filemanage/, '') || '/',
      },
      '/fm-assets': {
        target: `http://127.0.0.1:${apiPort}`,
        changeOrigin: true,
        xfwd: true,
      },
      '/auth': {
        target: `http://127.0.0.1:${apiPort}`,
        changeOrigin: true,
        xfwd: true,
      },
      '/vendor': {
        target: `http://127.0.0.1:${apiPort}`,
        changeOrigin: true,
        xfwd: true,
      },
      '/viewer.html': {
        target: `http://127.0.0.1:${apiPort}`,
        changeOrigin: true,
        xfwd: true,
      },
      '/viewer': {
        target: `http://127.0.0.1:${apiPort}`,
        changeOrigin: true,
        xfwd: true,
      },
      '/voice/api': {
        target: `http://127.0.0.1:${voicePort}`,
        changeOrigin: true,
        rewrite: path => path.replace(/^\/voice/, ''),
      },
      '/voice-ws': {
        target: `ws://127.0.0.1:${voiceWsPort}`,
        ws: true,
        rewrite: () => '/ws/stt',
      },
    },
  },
})
