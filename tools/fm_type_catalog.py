"""File-type taxonomy for the Fairy delivery area (workspace/result).

The layout follows the Windows "known folders" idea: a handful of
top-level folders named after what you DO with a file, not after its
technical format. Fairy keeps the Windows names people already know
(Documents / Pictures / Media / Code) and adds a few project-specific
ones (Models3D / Archives / Data) for things a desktop user never has.

One source of truth, shared by:
  - tools/organize_result.py        (moves loose files into the folders)
  - filemanager/fm-type-filter.js   (the client-side filter dropdown)

Keeping them in one file means the folders on disk and the filter
categories in the UI can never drift apart.
"""

# key -> (display name, top-level folder, extensions)
CATEGORIES = {
    # --- Windows-style known folders ---------------------------------
    "doc":     ("文档",   "Documents", [".md", ".txt", ".pdf", ".docx", ".doc", ".rtf",
                                        ".odt", ".epub", ".pages"]),
    "sheet":   ("表格",   "Documents", [".xlsx", ".xls", ".csv", ".tsv", ".ods", ".numbers"]),
    "slide":   ("演示",   "Documents", [".pptx", ".ppt", ".odp", ".key"]),
    "image":   ("图片",   "Pictures",  [".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg",
                                        ".bmp", ".ico", ".avif", ".tif", ".tiff", ".heic",
                                        ".psd", ".exr", ".hdr"]),
    "video":   ("视频",   "Media",     [".mp4", ".webm", ".mov", ".mkv", ".avi", ".m4v",
                                        ".ogv", ".flv", ".wmv", ".mpg", ".mpeg"]),
    "audio":   ("音频",   "Media",     [".mp3", ".wav", ".m4a", ".aac", ".ogg", ".oga",
                                        ".opus", ".flac", ".wma", ".weba", ".aiff"]),
    # --- project-specific additions ----------------------------------
    "code":    ("代码",   "Code",      [".py", ".js", ".ts", ".tsx", ".jsx", ".go", ".rs",
                                        ".java", ".rb", ".sh", ".bash", ".ps1", ".bat",
                                        ".c", ".cpp", ".h", ".hpp", ".css", ".html",
                                        ".htm", ".vue", ".svelte", ".sql", ".lua"]),
    "data":    ("数据",   "Data",      [".json", ".yaml", ".yml", ".xml", ".toml", ".ini",
                                        ".env", ".ndjson", ".log"]),
    "model3d": ("三维",   "Models3D",  [".blend", ".blend1", ".glb", ".gltf", ".fbx", ".obj",
                                        ".dae", ".stl", ".usdz", ".abc", ".usd", ".usda",
                                        ".ply", ".wrl"]),
    "archive": ("压缩包", "Archives",  [".zip", ".tar", ".gz", ".bz2", ".xz", ".7z", ".rar",
                                        ".tgz", ".bz2", ".zst"]),
}

# Top-level folders, in the order they should appear in the file browser.
FOLDER_ORDER = [
    "Documents", "Pictures", "Media", "Code", "Data", "Models3D", "Archives",
]

# ext -> category key
EXT_MAP = {}
for _k, (_name, _folder, _exts) in CATEGORIES.items():
    for _e in _exts:
        EXT_MAP[_e] = _k


def category_of(filename):
    """Category key for a filename, or None when the extension is unknown."""
    import os
    ext = os.path.splitext(filename)[1].lower()
    return EXT_MAP.get(ext)


def folder_of(filename):
    """Top-level folder for a filename, or None when unknown."""
    cat = category_of(filename)
    if cat is None:
        return None
    return CATEGORIES[cat][1]


def ui_options():
    """(key, label) pairs for the file-manager filter dropdown."""
    return [("all", "全部类型")] + [(k, CATEGORIES[k][0]) for k in CATEGORIES]
