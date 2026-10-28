# Poly Haven HDRI 取法（本机实测 2026-10-16）

**必须走代理**，否则海外站连不上：

```bash
export HTTPS_PROXY=http://127.0.0.1:7897 HTTP_PROXY=http://127.0.0.1:7897
```

## 三步（结构是嵌套的，别按老文档猜）

```bash
# 1. 资产清单（实测 200，约 1 MB JSON，997 个）
curl -s "https://api.polyhaven.com/assets?t=hdris&l=high" -o /tmp/hdri_list.json

# 2. 某个资产的真实下载地址：**hdri → 档位 → 格式 → url**（四层嵌套）
curl -s "https://api.polyhaven.com/files/aarfontein_dawn_2" -o /tmp/hdri_files.json
#   files.hdri['1k'].hdr.url
#   files.hdri['1k'].hdr.md5
# exr 是同结构：files.hdri['1k'].exr.url

# 3. 下载
curl -sL "https://dl.polyhaven.org/file/ph-assets/HDRIs/hdr/1k/aarfontein_dawn_2_1k.hdr" -o out.hdr
```

## 本次实测结果

```
字节数   1,486,826
file -b  Radiance HDR image data
md5      9ab1420c62b957d31111b1c5b1fc488e   ← 与 API 给的 md5 完全一致
```

已下载到 `workspace/result/hdri/aarfontein_dawn_2_1k.hdr`。

## 两个坑

1. **结构是 `hdri → 1k → hdr → url`**，不是 `hdri → 1k.hdr`。
   按后者猜会拿到空串，然后表现为「URL 返回 404」，很容易误判成资源不存在。
2. **下载必须带 `-L`**，CDN 会重定向；只写 `curl -s` 会拿到一个跳转页（几十字节的 HTML）。
