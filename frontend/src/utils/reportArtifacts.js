const SAFE_INTERNAL_PREFIXES = ['/api/', '#'];

export function normalizeArtifactPath(value) {
  let pathValue = String(value || '').trim();
  if (!pathValue) return '';
  pathValue = pathValue.replace(/\\/g, '/');
  // URL pathnames often prepend "/" to Windows drive paths: /D:/Fairy.
  pathValue = pathValue.replace(/^\/+([A-Za-z]:\/)/, '$1');
  try {
    if (/^sandbox:/i.test(pathValue)) pathValue = pathValue.replace(/^sandbox:/i, '');
    if (/^file:/i.test(pathValue)) {
      pathValue = decodeURIComponent(new URL(pathValue).pathname);
      if (/^\/[A-Za-z]:\//.test(pathValue)) pathValue = pathValue.slice(1);
    }
  } catch {
    return '';
  }
  if (/^(?:https?:)?\/\//i.test(pathValue)) return '';
  if (pathValue.startsWith('//mnt/data/')) pathValue = pathValue.slice(1);
  if (pathValue.includes('\0')) return '';
  // Keep the logical /mnt/data paths used by older skills, plus real paths
  // emitted by the local agent. Relative paths are accepted only inside the
  // three roots the backing file-content route is allowed to serve.
  if (pathValue === '/mnt/data' || pathValue.startsWith('/mnt/data/')) return pathValue;
  if (/^[A-Za-z]:\//.test(pathValue)) return pathValue;
  if (pathValue.startsWith('/')) return pathValue;
  const relative = pathValue.replace(/^\.\//, '');
  if (/^(?:workspace|skills|frontend)\//i.test(relative)) return relative;
  return '';
}

export function artifactUrl(value, options = {}) {
  const pathValue = normalizeArtifactPath(value);
  if (!pathValue) return '';
  const query = new URLSearchParams({ path: pathValue });
  if (options.download) query.set('download', '1');
  return '/api/file-content?' + query.toString();
}

export function reportHref(value) {
  const raw = String(value || '').trim();
  if (!raw) return '';
  if (/^https?:\/\//i.test(raw)) return raw.replace(/["'<> ]/g, '');
  if (SAFE_INTERNAL_PREFIXES.some(prefix => raw.startsWith(prefix))) return raw;
  return artifactUrl(raw);
}

export function isMarkdownArtifactPath(value) {
  const pathValue = normalizeArtifactPath(value);
  return /\.(md|markdown)$/i.test(pathValue);
}
