# 数据驱动的场景设计

参考实现 ProceduralParadise 最值得学的不是代码，是 `scene.json` 的结构。

## 核心原则

**引擎写 Python，场景参数写 JSON。换场景不改代码。**

```
Core/          引擎层：nodes.py（GN 封装）/ gn.py / render.py / camera.py
场景目录/       内容层：materials.py / architecture.py / props.py
  scene.json   数据层：布局、尺寸、配色、机位全在这里
  shots/*.json 机位：相机参数 + 渲染设置
```

## scene.json 的四个必备字段

```json
{
  "id": "SceneName",
  "name": "人类可读的名字",
  "fps": 24,
  "defaults": { "shot": "Hero" },
  "notes": ["每个决策的推理", "..."],
  "materials": { "brick": { "color": [0.55, 0.30, 0.25, 1.0], "roughness": 0.85 } },
  "objects": [ { "type": "wall", "material": "brick", "size": [4.0, 0.1, 2.4] } ]
}
```

## `notes` 是最重要的字段

ProceduralParadise 每个 `notes` 条目都在回答"**这个数字是怎么来的**"。照抄这个习惯：

**好的 notes**（每个数字可追溯）：

> "Stair (dog-leg): 14 treads (0.12308 m risers, 0.433 m goings) rise from the garden
> at y = 0.32 to a landing at z = -1.354, then 11 treads turn west up to the gallery
> floor at x = -9.383; **solved from the painted nosings (16 px rms)**."

> "Sun: 55 deg high from azimuth 31 deg (to the right of and behind the atrium),
> so its shafts run down and to the left through the air as the painted ones do."

> "Side table: the painting draws its top as an ellipse rounder (aspect 0.57) than a
> horizontal disc can project to at that place (every point on that view ray gives
> sin 26 deg = 0.44); the disc matches the painted width and centre."

**坏的 notes**：

> "楼梯在 y=0.32，尺寸 0.12308"
> "太阳角度 55 度"

区别在于：前者能让人**校正**（16px rms 意味着还有误差，知道往哪调），后者只能让人接受。

## 机位标定：怎么从图里解出相机

参考实现的 `shots/*.json` 揭示了一套可复用流程：

```json
"camera": {
  "resolution": [2166, 3000],
  "focal_px": 2798.348,
  "principal_px": [710.432, 1001.701],
  "location": [0.0, 0.0, 2.5],
  "yaw_deg": 23.528,
  "pitch_deg": -25.633,
  "roll_deg": -0.06
}
```

| 字段 | 含义 | 怎么来 |
|---|---|---|
| `focal_px` | 焦距（像素） | 从图的透视收缩率反推 |
| `principal_px` | 主点 | 原画是移轴镜头，主点不在画面中心 |
| `yaw/pitch/roll` | 朝向 | 对齐图里的地平线和消失点 |
| `roll_deg: -0.06` | 几乎为 0 | 说明作者认真量过，水平线是准的 |

**`principal_px` 偏移是关键**——它区分了"普通照片"和"移轴/鱼眼"，不做这一步渲染出来透视就不对。

## 单图还原的现实边界

参考实现作者的原话：

> "ai 画的图一般也不是 3D 结构正确的，所以换个角度就出问题了"
> "轮廓在设定图上逐个描到亚像素，厚度取自端视图，凹凸照一张对齐到设定图的法线图读出"

**三张图才有解**：正视图（轮廓）+ 侧视图/端视图（厚度）+ 材质特写（表面处理）。

只有一张图时：

1. 结构是猜的，**明说**
2. 尺寸优先用已知信息（标准砖 240×115×53mm、门宽 900mm、层高 3000mm），别让模型瞎猜
3. 只在**画面内**成立，换角度穿帮是必然的

## 参数区间的做法

参考实现在 shots 里给渲染参数设区间，而不是固定值：

```json
"render": {
  "engine": "EEVEE",
  "samples": 64,
  "view": "Standard",
  "exposure": 0.0
}
```

配合 notes 说明**为什么是这个值**（比如 64 采样是因为 CPU 渲染的耗时预算），换机器时才知道该往哪调。

## 检查清单

写完 scene.json 问自己：

- [ ] 每个魔数在 `notes` 里有来源说明吗？
- [ ] 机位的 `principal_px` 量过了吗（不是默认画面中心）？
- [ ] 尺度比例和真实世界一致吗（1 Blender 单位 = 1 米）？
- [ ] 结构之间摆开了吗（原点不要全堆在 0,0,0）？
- [ ] 地面/环境的尺寸没有大到淹没主体？
