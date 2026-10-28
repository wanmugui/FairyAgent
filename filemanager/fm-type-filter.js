/* fm-type-filter.js — file-type taxonomy for the file manager.
 *
 * Mirrors tools/fm_type_catalog.py so the folder names on disk and the
 * filter categories in the UI can never drift apart. The same
 * "known folders" idea as Windows: categories are named after what you
 * do with a file, not after its extension.
 *
 * Exposes window.FMTypeFilter:
 *   options()      -> [{key, label}] for the dropdown
 *   categoryOf(name) -> category key or null
 *   matches(name, key) -> boolean
 */
(function () {
  'use strict';

  // key -> [display label, [extensions]]
  var CATEGORIES = {
    doc:     ['文档', ['.md', '.txt', '.pdf', '.docx', '.doc', '.rtf', '.odt', '.epub', '.pages']],
    sheet:   ['表格', ['.xlsx', '.xls', '.csv', '.tsv', '.ods', '.numbers']],
    slide:   ['演示', ['.pptx', '.ppt', '.odp', '.key']],
    image:   ['图片', ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.svg', '.bmp', '.ico',
                       '.avif', '.tif', '.tiff', '.heic', '.psd', '.exr', '.hdr']],
    video:   ['视频', ['.mp4', '.webm', '.mov', '.mkv', '.avi', '.m4v', '.ogv', '.flv',
                       '.wmv', '.mpg', '.mpeg']],
    audio:   ['音频', ['.mp3', '.wav', '.m4a', '.aac', '.ogg', '.oga', '.opus', '.flac',
                       '.wma', '.weba', '.aiff']],
    code:    ['代码', ['.py', '.js', '.ts', '.tsx', '.jsx', '.go', '.rs', '.java', '.rb',
                       '.sh', '.bash', '.ps1', '.bat', '.c', '.cpp', '.h', '.hpp', '.css',
                       '.html', '.htm', '.vue', '.svelte', '.sql', '.lua']],
    data:    ['数据', ['.json', '.yaml', '.yml', '.xml', '.toml', '.ini', '.env', '.ndjson', '.log']],
    model3d: ['三维', ['.blend', '.blend1', '.glb', '.gltf', '.fbx', '.obj', '.dae', '.stl',
                       '.usdz', '.abc', '.usd', '.usda', '.ply', '.wrl']],
    archive: ['压缩包', ['.zip', '.tar', '.gz', '.bz2', '.xz', '.7z', '.rar', '.tgz', '.zst']],
  };

  var EXT_MAP = {};
  Object.keys(CATEGORIES).forEach(function (k) {
    CATEGORIES[k][1].forEach(function (e) { EXT_MAP[e] = k; });
  });

  function extOf(name) {
    var s = String(name || '');
    var i = s.lastIndexOf('.');
    if (i < 1) return '';
    return s.slice(i).toLowerCase();
  }

  function categoryOf(name) {
    return EXT_MAP[extOf(name)] || null;
  }

  function matches(name, key) {
    if (!key || key === 'all') return true;
    return categoryOf(name) === key;
  }

  function options() {
    var out = [{ key: 'all', label: '全部类型' }];
    Object.keys(CATEGORIES).forEach(function (k) {
      out.push({ key: k, label: CATEGORIES[k][0] });
    });
    return out;
  }

  window.FMTypeFilter = {
    options: options,
    categoryOf: categoryOf,
    matches: matches,
    labelOf: function (key) {
      return CATEGORIES[key] ? CATEGORIES[key][0] : '全部类型';
    },
  };
})();
