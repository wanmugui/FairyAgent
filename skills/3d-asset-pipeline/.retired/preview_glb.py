#!/usr/bin/env python3
"""把 to3d.py 产出的 GLB 渲成一张预览图，用于人眼验收建模质量。"""
import sys
import bpy

src = sys.argv[1]
dst = sys.argv[2]

bpy.ops.wm.read_factory_settings(use_empty=True)
bpy.ops.import_scene.gltf(filepath=src)

# 选中模型并归一化到 Z 轴朝上、居中
objs = [o for o in bpy.context.scene.objects if o.type == 'MESH']
if not objs:
    raise SystemExit('no mesh imported')
for o in bpy.context.scene.objects:
    o.select_set(False)
for o in objs:
    o.select_set(True)
bpy.context.view_layer.objects.active = objs[0]
bpy.ops.object.transform_apply(location=True, rotation=True, scale=True)

mins = [1e9] * 3
maxs = [-1e9] * 3
for o in objs:
    for c in o.bound_box:
        w = o.matrix_world @ __import__('mathutils').Vector(c)
        for i in range(3):
            mins[i] = min(mins[i], w[i])
            maxs[i] = max(maxs[i], w[i])
center = [(mins[i] + maxs[i]) / 2 for i in range(3)]
h = max(maxs[i] - mins[i] for i in range(3)) or 1.0
for o in objs:
    o.location = [o.location[i] - center[i] for i in range(3)]
    o.location[2] += h / 2

bpy.ops.object.select_all(action='DESELECT')

world = bpy.data.worlds.get('World') or bpy.data.worlds.new('World')
bpy.context.scene.world = world
world.use_nodes = True
world.node_tree.nodes['Background'].inputs[0].default_value = (0.09, 0.10, 0.12, 1)
world.node_tree.nodes['Background'].inputs[1].default_value = 1.0

cam_data = bpy.data.cameras.new('C')
cam = bpy.data.objects.new('C', cam_data)
bpy.context.scene.collection.objects.link(cam)
bpy.context.scene.camera = cam
d = h * 2.4
cam.location = (d * 0.42, -d * 0.72, h * 0.55)
cam.rotation_euler = (__import__('mathutils').Vector((0, 0, h * 0.52)) - cam.location).to_track_quat('-Z', 'Y').to_euler()
cam_data.lens = 62

for name, loc, energy in (('key', (2.2, -2.6, 3.4), 900), ('fill', (-3.0, -1.4, 1.6), 320), ('rim', (0.4, 3.2, 2.8), 600)):
    ld = bpy.data.lights.new(name, 'AREA')
    ld.energy = energy
    ld.size = 4.0
    lo = bpy.data.objects.new(name, ld)
    lo.location = loc
    bpy.context.scene.collection.objects.link(lo)

sc = bpy.context.scene
sc.render.engine = 'BLENDER_EEVEE_NEXT' if 'BLENDER_EEVEE_NEXT' in [i.identifier for i in bpy.types.RenderSettings.bl_rna.properties['engine'].enum_items] else 'BLENDER_EEVEE'
sc.render.resolution_x = 720
sc.render.resolution_y = 900
sc.render.film_transparent = False
sc.render.filepath = dst
sc.render.image_settings.file_format = 'PNG'
bpy.ops.render.render(write_still=True)
print('RENDERED', dst, 'objects=', len(objs), 'height=', round(h, 4))
