# Blender 4.5 / 5.1 API 改名与实测签名

> 全部条目由 `dual-env-probe.py` 在 **Blender 4.5.14 LTS** 与 **bpy 5.1.0** 上实测产出。
> 两版**行为完全一致**，版本号是唯一差异。

## 目录

| 环境 | 入口 |
|---|---|
| 4.5.14 LTS（独立版） | `/home/user/Fairy/.tools/blender/blender-4.5.14-linux-x64/blender` |
| bpy 5.1.0（pip 模块） | `/home/user/miniforge3/envs/bpyenv/bin/python3.13` |

跑探测：`blender -b --factory-startup -P dual-env-probe.py` 或直接 `python3.13 dual-env-probe.py`。

---

## 实测通过的调用

| 操作 | 正确写法 | 实测结果 |
|---|---|---|
| Voxel Remesh | `ob.data.remesh_voxel_size = 0.005` + `bpy.ops.object.voxel_remesh()` | OK |
| Quadriflow | `bpy.ops.object.quadriflow_remesh(target_faces=200)` | OK，200 目标 → 实际面数接近，三角面 0 |
| Multires | `m = ob.modifiers.new("m","MULTIRES")`；`m.levels` / `m.render_levels` | OK |
| Displace | `bpy.data.textures.new("P", type="VORONOI")` → `DISPLACE` modifier | OK |
| UV | `bpy.ops.object.mode_set(mode="EDIT")` → `bpy.ops.uv.smart_project()` | OK |
| 模式切换 | `bpy.ops.object.mode_set(mode="OBJECT")` 后再调对象级算子 | OK，EDIT 下会报 OperatorError |

## 实测失败的调用

| 操作 | 错误写法 | 实际报错 | 正确写法 |
|---|---|---|---|
| 笔刷 | `st.sculpt.brush = ...` | `AttributeError: attribute "brush" from "Sculpt" is read-only` | **无头环境无法用笔刷**，改程序化置换 |
| Quadriflow | `quadriflow_remesh(target_number_of_faces=)` | `TypeError: keyword ... unrecognized` | `target_faces=` |
| NOISE 纹理 | `tex.noise_scale = 0.9`（type="NOISE"） | 该类型**没有** `noise_scale` 属性 | 用 `type="VORONOI"` |
| 导入顺序 | `import bmesh` 在 `import bpy` 之前 | bpy 未初始化 | **`import bpy` 必须在最前** |

## 版本间的差异

**4.5.14 与 5.1.0 的以上条目完全相同。** 网上大量 3.x / 4.0 的教程在这两个版本上会失效
（`target_number_of_faces`、`symmetrize(direction="X")`、`brush_stroke` 的旧 `stroke` 结构），
**不要照抄**。

---

## Voxel Remesh 会静默删除不连通的体块

**最高危的一条，且不报错。**

实测：25+ 个体块一起 `voxel_remesh`，结果只剩一个头——包围盒 `z[1.432..1.651]`，
躯干四肢**全部消失**，无任何警告。

**逐件 remesh，最后 join：**

```python
for ob in parts:
    bpy.ops.object.select_all(action="DESELECT")
    ob.select_set(True)
    bpy.context.view_layer.objects.active = ob
    ob.data.remesh_voxel_size = 0.005
    bpy.ops.object.mode_set(mode="OBJECT")
    bpy.ops.object.voxel_remesh()

bpy.ops.object.select_all(action="DESELECT")
for ob in parts:
    ob.select_set(True)
bpy.context.view_layer.objects.active = parts[0]
bpy.ops.object.join()
```

**渲图后必须验包围盒**，这是唯一的部件存活检查：

```python
bb = [body.matrix_world @ Vector(c) for c in body.bound_box]
print("z[%.3f..%.3f]" % (min(v.z for v in bb), max(v.z for v in bb)))
```

1.70m 人形 z 下界应接近 0（上界约 1.65）。**z 下界 1.4 左右 = 只剩头了。**

---

## headless 渲染与检查

```python
# 相机是否真的对准模型
from bpy_extras.object_utils import world_to_camera_view
v = world_to_camera_view(scene, cam, body.matrix_world.translation)
print("ndc=(%.3f,%.3f) 像素=(%.0f,%.0f)" % (v.x, v.y, v.x*W, (1-v.y)*H))
```

正确值应落在画面中心 `(0.5, 0.5)`。**先确认相机，再怀疑模型。**

Cycles 烘焙：EEVEE 不支持法线烘焙，报 `Can only be baked in Cycles`。
