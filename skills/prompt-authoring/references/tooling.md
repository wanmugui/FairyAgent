# 工具契约与硬限制

扩写 prompt 前先确认目标工具的实际约束。**写进 prompt 之前不知道的约束，等于没写。**

## 各工具契约速查

| 工具 | 契约要点 |
|---|---|
| `image_generate` | **不扩写、不翻译、不润色**。prompt 完全由调用方写，工具只管生成。纯 T2I，不接受参考图（参考图要用 image_search）。maxImages 4，aspect_ratio 枚举固定。 |
| `bpy_*` | 通过场景 JSON 建模。参数化，非自然语言。 |
| `html_to_png` | file 模式或 stateless 模式（html_file_content 必须是自包含的 data URL 图片）。 |
| `skill_search` | **打分只读 name/description/location，不读 tags/triggers**。所以靠 tags 命中的 skill，在这里搜不到。 |
| `web_search` | 查询要短（2-6 关键词），或用 queries 批量 2-6 条。recent=true 限定 24 小时。 |

## 硬限制（实测，不可绕过）

### ES module 在 file:// 下被 CORS 全盘拦截

```
Cross origin requests are only supported for protocol schemes:
chrome, chrome-extension, chrome-untrusted, data, http, https
```

**与文件是否在本地无关。** 以下做法全部无效：

| 做法 | 有效？ |
|---|---|
| importmap 指向相对路径 `./vendor/three.module.js` | ❌ |
| CDN 换成本地文件 | ❌ |
| 资源内联成 base64 | ❌ 单独无效 |
| 改用 `loader.parse()` 代替 `loader.load()` | ❌ 单独无效 |

可行：走 HTTP（`python3 -m http.server`），或用 vite/rollup 打包成单文件 HTML。

**教训：交付网页必须用最终形态实测。** 只在一种协议下测过就下结论，同一页面两种协议结果可以完全相反。

### Blender 渲染引擎枚举随版本改名

| 版本 | 枚举值 |
|---|---|
| ≤ 4.1 | `BLENDER_EEVEE` |
| ≥ 4.2 | `BLENDER_EEVEE_NEXT`（4.5 实测：写旧值报枚举错误） |
| 通用 | `CYCLES` |

写 prompt 说「使用 EEVEE」是**不够的**——必须写明版本对应的完整枚举。报错信息是 `enum "BLENDER_EEVEE" not found in (...)`。

### Blender 4.x 属性改名的连带坑

若对某个 object 设了非均匀 scale（描边外推、压扁的积水盘等），`bound_box` 与包围球会变成**陈旧值**。此时 `obj.dimensions` 反映的是缩放后的值，但直接用 mesh 顶点算的包围盒不会更新——两者不一致会导致取景/相机定位错误。

### 灯光类型影响穿透

AREA（面光源）贴在窗口只会照亮外墙；**要照亮室内陈设必须用 POINT**，并把光源放在室内、位置对准货架。

### 卡通材质 ShaderToRGB 不在 glTF 规范内

用 Diffuse → ShaderToRGB → ColorRamp → Emission 做三渲二时，导出 GLB 会：
- `baseColorFactor` 全变 `(0,0,0)`
- `emissiveFactor` 全变 `(1,1,1)`
- **所有材质塌成同一个白模，且不报任何错**

**必须在导出前手工兜底**：从 ColorRamp 抓出每个材质的三段色存成 JSON，网页端按材质名还原。

### 盒包围盒 expandByObject 的陷阱

`Box3.expandByObject()` 展开子树时，会读到**手工外推过、包围球从未重算**的描边壳的陈旧值。

实测：真实尺寸 9.9 × 4.4 × 10.05，`expandByObject` 算出 48 × 8.65 × 48（差 5 倍），相机被推到 82 单位外，场景缩成一小团。

**修法**：自己遍历几何角点算包围盒，别用 `expandByObject`。

### 反向壳描边会产生黑块

BackSide + 法线外推做描边时，壳会挂在**非凸物体的凹处**，判定覆盖不到 → 大片纯黑矩形。

以下三种补救**全部无效**：按几何类型跳过、厚度随尺寸自适应、法线同向检测。

**正解**：用 `OutlineEffect`（屏幕空间后处理），描边质量更好，且不会产生几何遮挡问题。

## 写 prompt 前的工具确认清单

- [ ] 目标工具是否接受自然语言？还是只接受结构化参数？
- [ ] 工具会不会替我扩写？（多数不会，要自己写全）
- [ ] 有哪些**必填**参数/枚举？（画幅、语言、格式）
- [ ] 有哪些**输出侧硬限制**？（glTF 丢材质、file:// 拦 module）
- [ ] prompt 里说的效果，工具链真的能产出吗？
