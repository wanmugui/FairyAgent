---
name: 3d-model
description: Use when 需要写、改或评审 OpenSCAD (.scad) 参数化模型，或需要把 3D 模型渲染成预览图、导出 STL/3MF 做 3D 打印。触发场景：用户要一个 3D 零件/支架/外壳/卡扣/齿轮，提到「建模」「3D 打印」「STL」「scad」「渲染模型看看」「这个件怎么画」。
tags:
  - 3d
  - scad
  - openscad
  - stl
  - printing
triggers:
  - 建模
  - 3D 打印
  - STL
  - scad
  - 渲染模型看看
  - 这个件怎么画
  - 3d model
priority: 72
---

# 3D 建模（OpenSCAD）

用 `scripts/model.py` 驱动本机 OpenSCAD 跑「写模型 → 渲染 → 自己看图 → 改 → 再渲染」的闭环。

**核心不是写出 scad 文件，是每一版改动都真的看过渲染图。** 参数化模型最容易出的错（孔打空、板沉到基准面以下、加强筋捅到壳外）**编译都不报错、体积也大致对**，只有看图和量包围盒才抓得住。

## 何时用

- 用户要一个 3D 零件：支架、外壳、卡扣、连接件、盒子、旋钮
- 要把 .scad 渲染出来「看看长什么样」
- 要导出 STL/3MF 送去 3D 打印或切片
- 要评审别人写的 .scad 参数是否合理、能否打印

不用：只是描述形状让用户自己去建模；网格类造型（雕刻、布料、扫描件）——OpenSCAD 做不了。

## 本机环境事实（别自己踩一遍）

| 项 | 实际 |
|---|---|
| OpenSCAD | **2021.01**，二进制在 `/usr/bin/openscad` |
| PNG 渲染 | 必须 `xvfb-run -a`（无头机器没有 OpenGL） |
| STL 导出 | 不需要 xvfb（脚本会自动回退） |

2021.01 的三个坑，脚本已经封好了，你手写命令行时必须记得：

1. **`--render` 必须带值**：`--render=true`。不给值它会开 GUI 窗口，在无头机上直接卡死。
2. **`--camera` 只收 6 个数**（矢量相机：平移 xyz + 旋转 xyz）或 **7 个数**（云台相机，末位为距离）。给别的个数会被静默忽略。
3. PNG 渲染要走 `xvfb-run -a openscad ...`。

## 用法

```bash
# 渲染多视角预览（默认 iso,front,top）
python3 skills/3d-model/scripts/model.py render part.scad --views iso,front,top --size 600
python3 skills/3d-model/scripts/model.py render part.scad --views all

# 带参数覆盖渲染
python3 skills/3d-model/scripts/model.py render part.scad --set width=140

# 导出（stl / 3mf / off / csg / amf 都支持）
python3 skills/3d-model/scripts/model.py export part.scad --format stl --out out/bracket.stl

# 体检：参数范围 + 体积 + 三角面数 + 包围盒
python3 skills/3d-model/scripts/model.py check part.scad --max-volume 50000 --max-facets 20000
```

视角可选：`iso` `front` `back` `left` `right` `top` `bottom`。
`--outdir` 默认写 `<模型目录>/preview/`。

### 退出码

| 码 | 含义 |
|---|---|
| 0 | 通过 |
| 2 | 用法错（视角名非法、k=v 格式错） |
| 3 | 参数越界 |
| 4 | 体积/面数超上限 |
| 5 | OpenSCAD 本身失败（看 stderr 尾部） |

## 看图闭环（不要跳）

```
1. 写 .scad，把能算的都算成参数
2. model.py check          → 先排掉参数越界、几何退化
3. model.py render         → 至少 3 个视角
4. 真的把图打开看          → 见下面「怎么算看过」
5. 对不上就改，改完回到 3
6. model.py export         → 交 STL
```

### 怎么算「看过」

- 用 `image_vqa` 描述图里的结构：主体是什么、孔有几个、筋在哪、有没有突兀的凸起。
- **同时量包围盒**。`check` 会打印 `包围盒 X x Y x Z`，**把它和设计尺寸对一遍**。对不上说明有东西长到壳外面了。
- **数一数孔**。正视图里数背景色斑的个数和位置，两个孔应该左右对称于画面中心。
- 只跑 `check` 看到体积「差不多」不算数。`examples/bracket.scad` 那个真实 bug 里，
  理论 40510 mm³ vs 实测 39472 mm³，看着只差 2.5%，纯数字校验完全放行——**是看图抓到的**。

## 参数区间声明

在 scad 里用注释声明合法区间，`check` 会读取并强制校验（`--set` 覆盖越界直接退出码 3）：

```openscad
// @param width 40 140
// @param thick 3 25
```

模型内部再加 `assert()` 把几何前提写死，违反时 OpenSCAD 直接报错，而不是给你渲一个废件：

```openscad
assert(fillet < min(width, depth) / 2 - 1, "圆角太大");
assert(width / 2 - hole_inset + hole_d / 2 < width / 2 - fillet, "孔啃到侧边圆角");
```

## 快速参考

| 想干什么 | 做法 |
|---|---|
| 圆角板 | `hull()` 四个角上 `cylinder(r=圆角)` |
| 通孔 | `cylinder(h=板厚+2*eps)`，`eps=0.01` 避免共面 |
| 抽壳 | `difference(){ 实体; translate([-1000,-1000,-1000]) cube([2000,2000,厚度-2*eps]); }` |
| 加强筋 | YZ 平面直角三角形 `linear_extrude` 沿 X 拉伸 |
| 阵列 | `for (sx=[-1,1]) translate([sx*间距,0,0]) ...` |

## 常见错误（全部真实踩过）

**1. 模块以原点为中心，直接摆会沉下去**
`hull()` 拼的板默认以 z=0 为中心。立板本该坐在底板上，不抬 `height/2` 就有一半沉到基准面以下——包围盒 Z 会多出一截，板顶的孔直接打空。

**2. 孔从板中面起切 → 变盲孔**
`translate([..., depth/2 - thick/2, ...]) cylinder(h=thick)` 只切掉后半块板。
从背面看有孔，从正面看是实心的。**起点要用内表面 `depth/2 - thick`。**

**3. 加强筋朝反方向，从壳体外侧捅出去**
YZ 三角形的 `+ty` 是往后长。写成 `[-ty]` 才朝前长。包围盒 Y 会比设计深一截。

**4. 圆角把孔啃成残缺**
`hull()` 的圆角是切掉角，所以孔的外缘必须小于圆角起点：
`width/2 - hole_inset + hole_d/2 < width/2 - fillet`。不等号写反了 assert 会把模型判死。

## 参考

- `examples/bracket.scad` —— 完整示例：圆角板 + 两个安装孔 + 角撑加强筋 + assert 自检 + 区间声明
- 上游：`fairy-reference-analysis`（从现有程序/游戏里逆向 UI 时用）
