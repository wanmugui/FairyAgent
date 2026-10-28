// 应用挂载前缀。
//
// 线上是 Caddy 把 /agent/* 剥掉前缀再交给 Vite（见 infra/cloudflared/Caddyfile
// 的 `handle /agent/*` + `uri strip_prefix /agent`），但浏览器里的
// location.pathname 仍然带着 /agent 前缀。任何拼绝对路径的地方（登录页跳转、
// 静态资源直链）都要先拼上这个前缀，否则会被打回根域名。
export function appBase(pathname) {
  const p = pathname != null ? String(pathname) : (typeof window !== 'undefined' ? window.location.pathname : '/');
  return /^\/agent(\/|$)/.test(p) ? '/agent' : '';
}

// 剥掉挂载前缀后的应用内路径。/agent/voice -> /voice，/agent -> /。
export function appPathname(pathname) {
  const p = String(pathname != null ? pathname : (typeof window !== 'undefined' ? window.location.pathname : '/')).replace(/\/+$/, '');
  const stripped = p.replace(/^\/agent(\/|$)/, '/').replace(/\/+$/, '');
  return stripped || '/';
}
