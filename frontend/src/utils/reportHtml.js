import { reportHref } from './reportArtifacts.js';

// Convert report links, images and <cite> tags into browser-safe anchors.
// Local artifacts are served through the frontend /api/file-content bridge.
export function renderReportHtml(text) {
  let html = String(text || '').replace(/\r\n/g, '\n').replace(/\r/g, '\n');
  const esc = value => String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  const attr = (source, name) => {
    const match = source.match(new RegExp(name + '="([^"]*)"', 'i'));
    return match ? match[1] : '';
  };
  const placeholders = [];
  const hold = value => {
    const token = '\u0000REPORT' + placeholders.length + '\u0000';
    placeholders.push(value);
    return token;
  };

  html = html.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g, (match, alt, dest, title) => {
    const src = reportHref(dest);
    if (!src) return match;
    const titleAttr = title ? ' title="' + esc(title) + '"' : '';
    return hold('<img class="report-local-image" src="' + esc(src) + '" alt="' + esc(alt) + '"' + titleAttr + '/>');
  });

  html = html.replace(/\[([^\]]+)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)/g, (match, label, dest, title) => {
    const href = reportHref(dest);
    if (!href) return match;
    const titleAttr = title ? ' title="' + esc(title) + '"' : '';
    return hold('<a class="report-file-link" href="' + esc(href) + '" target="_blank" rel="noopener noreferrer"' + titleAttr + '>' + esc(label) + '</a>');
  });

  html = html.replace(/<cite\b[^>]*>[\s\S]*?<\/cite>/gi, match => {
    const index = attr(match, 'index');
    const title = attr(match, 'title');
    const inner = (match.match(/>([\s\S]*?)<\/cite>/i) || [,''])[1].replace(/<[^>]*>/g, '').trim();
    const markdown = inner.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
    const href = reportHref(attr(match, 'url') || attr(match, 'path') || (markdown && markdown[2]));
    const label = (markdown && markdown[1]) || inner || ('[' + index + ']');
    if (!href) return hold('<span class="cite-link">' + esc(label) + '</span>');
    return hold('<a class="cite-link report-file-link" href="' + esc(href) + '" target="_blank" rel="noopener noreferrer" title="' + esc(title) + '">' + esc(label) + '</a>');
  });

  html = html.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
  html = html.replace(/^### (.*)$/gm, '<h4>$1</h4>');
  html = html.replace(/^## (.*)$/gm, '<h3>$1</h3>');
  html = html.replace(/^# (.*)$/gm, '<h2>$1</h2>');
  html = html.replace(/^-{3,}$/gm, '<hr/>');
  html = html.replace(/((?:^\|.*\|\s*$\n?)+)/gm, block => {
    const lines = block.trim().split('\n').filter(Boolean);
    if (lines.length < 2) return block;
    const body = lines.filter(line => !/^\|[\s:|-]+\|$/.test(line));
    if (!body.length) return block;
    const rows = body.map(line => line.replace(/^\s*\|/, '').replace(/\|\s*$/, '').split('|').map(cell => cell.trim()));
    let table = '<table><thead><tr>' + rows[0].map(cell => '<th>' + cell + '</th>').join('') + '</tr></thead><tbody>';
    for (const row of rows.slice(1)) table += '<tr>' + row.map(cell => '<td>' + cell + '</td>').join('') + '</tr>';
    return table + '</tbody></table>';
  });
  html = html.replace(/^\s*[-*] (.*)$/gm, '<li>$1</li>');
  html = html.replace(/((?:<li>.*?<\/li>\n?)+)/g, (match, list) => '<ul>' + list + '</ul>');
  const parts = html.split(/\n{2,}/);
  html = parts.map(part => {
    const segment = part.trim();
    if (!segment) return '';
    if (/^<(h\d|table|ul|hr)/.test(segment)) return segment;
    return '<p>' + segment.replace(/\n/g, '<br/>') + '</p>';
  }).join('\n');

  for (let index = placeholders.length - 1; index >= 0; index -= 1) {
    html = html.split('\u0000REPORT' + index + '\u0000').join(placeholders[index]);
  }
  return html;
}
