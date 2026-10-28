"""gnkit -- 把 Blender 几何节点连线写成 Python 表达式。

为什么需要这个
--------------
Blender 的几何节点（Geometry Nodes）API 是 ``node_tree.nodes.new()`` /
``links.new()`` 反复调用的命令式写法。节点一多，图就没法看了：

    cube = nt.nodes.new("GeometryNodeMeshCube")
    cube.inputs["Size"].default_value = (2, 2, 2)
    pos = nt.nodes.new("GeometryNodeInputPosition")
    add = nt.nodes.new("ShaderNodeVectorMath")
    add.operation = 'ADD'
    nt.links.new(cube.outputs["Mesh"], add.inputs[0])
    nt.links.new(pos.outputs["Position"], add.inputs[1])

节点名全是 ``cube`` / ``pos`` / ``add``，链接关系藏在第四行。
参考实现 ProceduralParadise 的做法是把 socket 包装成对象，让运算符返回新节点，
图就变成了 ``cube.mesh.scale(2).translate(pos.position * 1.5)`` —— 一行一件事。

本模块是那个思路的最小可运行版本，只覆盖建模常用的节点，不追求覆盖全部。

用法（必须在 Blender 的 Python 里跑）::

    import bpy
    import gnkit

    tree = gnkit.tree("MyAsset")           # 新建几何节点树
    obj = gnkit.object("Cube", tree)        # 挂到对象上
    out = tree.geo()
    out.mesh.primitive_cube(size=2).transform(
        translate=(0, 0, 1), rotate=(0, 0, 0.5)
    ).material(gnkit.material("Red", color=(0.8, 0.1, 0.1, 1)))
    tree.realize(obj)

验证::

    blender -b -noaudio --python gnkit_smoke.py
"""

from __future__ import annotations

import math

import bpy

__all__ = [
    "tree",
    "object",
    "material",
    "light",
    "camera",
    "Sock",
    "GNError",
    "MESH_TYPES",
]


class GNError(RuntimeError):
    """Raised when a node lookup or operation is not valid."""


def gnkit_error(msg):
    return GNError(msg)


# Mesh primitive ops are named the same in the API as in our shorthand.
MESH_TYPES = (
    "cube",
    "cylinder",
    "cone",
    "uv_sphere",
    "ico_sphere",
    "plane",
    "circle",
    "grid",
)


# --------------------------------------------------------------------------
# Socket wrapper: the reason graphs stay readable
# --------------------------------------------------------------------------
class Sock:
    """One input or output socket on a node.

    Arithmetic returns a *new node* rather than a value, so a chain of
    operations reads left to right and each link is visible in the source.
    """

    __slots__ = ("tree", "socket", "node")

    def __init__(self, tree, socket, node=None):
        self.tree = tree
        self.socket = socket
        self.node = node

    # -- identity -----------------------------------------------------------
    @property
    def identifier(self):
        return f"{type(self.node).__name__}:{self.socket.name}"

    def __repr__(self):
        return f"<Sock {self.identifier}>"

    # -- type helpers -------------------------------------------------------
    @property
    def is_vector(self):
        return self.socket.type == "VECTOR"

    @property
    def is_geometry(self):
        return self.socket.type == "GEOMETRY"

    def default(self):
        return self.socket.default_value

    # -- links --------------------------------------------------------------
    def connect(self, other):
        """Link ``other`` into this socket and return self, so it can chain."""
        self.tree.link(other, self)
        return self

    def value(self, v):
        """Set this socket's default when nothing is linked into it."""
        self.tree.assign(self, v)
        return self

    # -- arithmetic (each returns a NEW node) --------------------------------
    def _binop(self, op, other, reverse=False):
        node = self.tree.new("ShaderNodeVectorMath")
        node.operation = op
        if isinstance(other, Sock):
            a, b = (other, self) if reverse else (self, other)
        else:
            a, b = (other, self) if reverse else (self, other)
            node.inputs[1].default_value = _as_vec(other, self.is_vector)
        self.tree.link(a, node.inputs[0])
        self.tree.link(b, node.inputs[1])
        return Sock(self.tree, node.outputs["Vector"], node)

    def __add__(self, o):
        return self._binop("ADD", o)

    def __radd__(self, o):
        return self._binop("ADD", o, reverse=True)

    def __sub__(self, o):
        return self._binop("SUBTRACT", o)

    def __rsub__(self, o):
        return self._binop("SUBTRACT", o, reverse=True)

    def __mul__(self, o):
        return self._binop("SCALE", o)

    def __rmul__(self, o):
        return self._binop("SCALE", o, reverse=True)

    def __truediv__(self, o):
        return self._binop("SCALE", 1.0 / _as_scalar(o))

    def __neg__(self):
        return self._binop("MULTIPLY", -1.0)

    # -- component access ---------------------------------------------------
    @property
    def x(self):
        return self._sep()[0]

    @property
    def y(self):
        return self._sep()[1]

    @property
    def z(self):
        return self._sep()[2]

    def _sep(self):
        node = self.tree.new("ShaderNodeSeparateXYZ")
        self.tree.link(self, node.inputs["Vector"])
        return [Sock(self.tree, node.outputs[n], node) for n in ("X", "Y", "Z")]

    def join(self, other):
        node = self.tree.new("ShaderNodeCombineXYZ")
        self.tree.link(self, node.inputs["X"])
        if isinstance(other, Sock):
            self.tree.link(other, node.inputs["Y"])
        else:
            node.inputs["Y"].default_value = _as_vec(other, True)[1]
        return Sock(self.tree, node.outputs["Vector"], node)


# --------------------------------------------------------------------------
# Geometry wrapper: the ergonomic surface of the whole library
# --------------------------------------------------------------------------
class Geo:
    """A geometry socket with the modelling verbs chained onto it."""

    __slots__ = ("tree", "sock")

    def __init__(self, tree, sock):
        self.tree = tree
        self.sock = sock

    def __repr__(self):
        return f"<Geo {self.sock.identifier}>"

    # -- primitives ---------------------------------------------------------
    def cube(self, size=1.0, **kw):
        return self._prim("GeometryNodeMeshCube", {"Size": _as_vec(size, True)}, kw)

    def cylinder(self, radius=0.5, depth=1.0, vertices=32, **kw):
        return self._prim(
            "GeometryNodeMeshCylinder",
            {"Radius": _as_scalar(radius), "Depth": _as_scalar(depth), "Vertices": vertices},
            kw,
        )

    def cone(self, radius1=0.5, radius2=0.0, depth=1.0, vertices=32, **kw):
        bot, top = _cone_socket_names()
        vals = {bot: _as_scalar(radius1), top: _as_scalar(radius2),
                "Depth": _as_scalar(depth), "Vertices": vertices}
        return self._prim("GeometryNodeMeshCone", vals, kw)

    def sphere(self, radius=0.5, segments=32, rings=16, **kw):
        return self._prim(
            "GeometryNodeMeshUVSphere",
            {"Radius": _as_scalar(radius), "Segments": segments, "Rings": rings},
            kw,
        )

    def ico_sphere(self, radius=0.5, subdivisions=2, **kw):
        """二十面体球。低模场景元素（石头、灌木）用它，天然有棱面。

        subdivisions=1 -> 20 面，是「有棱面的石头」最省的选择。
        """
        vals = {"Radius": _as_scalar(radius), "Subdivisions": subdivisions}
        return self._prim("GeometryNodeMeshIcoSphere", vals, kw)

    def grid(self, size_x=1.0, size_y=1.0, verts_x=2, verts_y=2, **kw):
        vals = {
            "Size X": _as_scalar(size_x),
            "Size Y": _as_scalar(size_y),
            "Vertices X": verts_x,
            "Vertices Y": verts_y,
        }
        return self._prim("GeometryNodeMeshGrid", vals, kw)

    def circle(self, radius=0.5, **kw):
        return self._prim("GeometryNodeMeshCircle", {"Radius": _as_scalar(radius)}, kw)

    def _prim(self, node_type, values, kw):
        node = self.tree.new(node_type)
        # 未知 socket 名**直接报错**，绝不静默跳过。
        # 这不是洁癖：5.1 把 GeometryNodeMeshCone 的 "Radius"/"Radius 2"
        # 改名成 "Radius Bottom"/"Radius Top"，旧代码按老名字写就静默丢参数——
        # 实测 cone(radius1=3) 和 cone(radius1=1) 出来的锥体半径都是 1，
        # 而脚本一路绿灯。静默丢参数比报错贵得多。
        available = [i.name for i in node.inputs]
        unknown = []
        for name, v in list(values.items()) + list(kw.items()):
            if name in node.inputs:
                node.inputs[name].default_value = v
            else:
                unknown.append(name)
        if unknown:
            raise KeyError(
                f"{node_type} 没有输入 {unknown}；实际可用输入为 {available}。")
        self.tree.link(self.sock, node.inputs[0])
        out = "Mesh" if "Mesh" in node.outputs else "Curve" if "Curve" in node.outputs else node.outputs[0]
        return Geo(self.tree, Sock(self.tree, node.outputs[out], node))

    # -- transforms ---------------------------------------------------------
    def transform(self, translate=(0, 0, 0), rotate=(0, 0, 0), scale=(1, 1, 1)):
        """translate -> rotate -> scale on one Transform node.

        Blender's Transform node takes all three at once (unlike 2.8's
        transform_space enum), so this is a single node with three inputs --
        which is also cheaper than three chained nodes.
        Rotate is in degrees here, converted to radians.
        """
        n = self.tree.new("GeometryNodeTransform")
        self.tree.link(self.sock, n.inputs["Geometry"])
        if any(translate):
            n.inputs["Translation"].default_value = _as_vec(translate, True)
        if any(rotate):
            n.inputs["Rotation"].default_value = tuple(
                math.radians(d) for d in _as_vec(rotate, True)
            )
        if scale != (1, 1, 1):
            n.inputs["Scale"].default_value = _as_vec(scale, True)
        return Geo(self.tree, Sock(self.tree, n.outputs["Geometry"], n))

    def instance_on_points(self, source, rotation=(0, 0, 0), scale=(1, 1, 1), **kw):
        """Array one geometry across the points of another.

        ``source`` is any Sock -- ``Geo.prototype()`` is the usual way to make
        one.
        """
        n = self.tree.new("GeometryNodeInstanceOnPoints")
        for name, v in kw.items():
            if name in n.inputs:
                n.inputs[name].default_value = v
        self.tree.link(self.sock, n.inputs["Points"])
        # The Instance socket is a plain Geometry socket with no
        # default_value -- the prototype is linked in like any other geometry,
        # not assigned as an object reference.
        self.tree.link(source, n.inputs["Instance"])
        if any(rotation):
            n.inputs["Rotation"].default_value = tuple(
                math.radians(d) for d in _as_vec(rotation, True)
            )
        if scale != (1, 1, 1):
            n.inputs["Scale"].default_value = _as_vec(scale, True)
        out = "Instances" if "Instances" in n.outputs else n.outputs[0]
        return Geo(self.tree, Sock(self.tree, n.outputs[out], n))

    def asset_socket(self):
        """A Mesh socket that can be fed to ``instance_on_points``.

        InstanceOnPoints takes a node *object*, so the prototype has to be a
        separate node whose Mesh output we hand over directly.
        """
        return self.sock

    def prototype(self, node_type="GeometryNodeMeshCube", **defaults):
        """Build a standalone primitive node suitable for instancing.

        InstanceOnPoints wants the *node object*, not a link, so the prototype
        is created unlinked and its Mesh output is handed over directly.
        """
        n = self.tree.new(node_type)
        for name, v in defaults.items():
            if name not in n.inputs:
                continue
            if n.inputs[name].type == 'VECTOR':
                n.inputs[name].default_value = _as_vec(v, True)
            elif n.inputs[name].type == 'VALUE':
                n.inputs[name].default_value = _as_scalar(v)
            else:
                n.inputs[name].default_value = v
        return Sock(self.tree, n.outputs["Mesh"], n)

    def grid_points(self, size_x=10.0, size_y=10.0, count_x=10, count_y=10):
        """A mesh grid used as an instance-on-points target."""
        n = self.tree.new("GeometryNodeMeshGrid")
        n.inputs["Size X"].default_value = size_x
        n.inputs["Size Y"].default_value = size_y
        n.inputs["Vertices X"].default_value = count_x
        n.inputs["Vertices Y"].default_value = count_y
        return Geo(self.tree, Sock(self.tree, n.outputs["Mesh"], n))

    # -- combining ----------------------------------------------------------
    def join(self, other):
        n = self.tree.new("GeometryNodeJoinGeometry")
        self.tree.link(self.sock, n.inputs["Geometry"])
        self.tree.link(other.sock if isinstance(other, Geo) else other, n.inputs["Geometry"])
        return Geo(self.tree, Sock(self.tree, n.outputs["Geometry"], n))

    def boolean(self, other, operation='DIFFERENCE'):
        """Boolean op: DIFFERENCE / UNION / INTERSECT."""
        n = self.tree.new("GeometryNodeMeshBoolean")
        n.operation = operation
        self.tree.link(self.sock, n.inputs[0])
        self.tree.link(other.sock if isinstance(other, Geo) else other, n.inputs[1])
        out = "Mesh" if "Mesh" in n.outputs else n.outputs[0]
        return Geo(self.tree, Sock(self.tree, n.outputs[out], n))

    def extrude(self, individual=False, offset=(0, 0, 0)):
        n = self.tree.new("GeometryNodeExtrudeMesh")
        self.tree.link(self.sock, n.inputs["Mesh"])
        n.inputs["Individual"].default_value = individual
        if any(offset):
            self.tree.link(self.tree.const(offset).sock, n.inputs["Offset"])
        return Geo(self.tree, Sock(self.tree, n.outputs["Mesh"], n))

    # -- node groups --------------------------------------------------------
    def set_material(self, *mats):
        n = self.tree.new("GeometryNodeSetMaterial")
        self.tree.link(self.sock, n.inputs["Geometry"])
        for m in mats:
            n.inputs["Material"].default_value = m
        return Geo(self.tree, Sock(self.tree, n.outputs["Geometry"], n))

    # convenience alias -- reads better in a chain
    material = set_material

    # -- finishing ----------------------------------------------------------
    def shade_smooth(self, smooth=True):
        n = self.tree.new("GeometryNodeSetShadeSmooth")
        n.inputs["Shade Smooth"].default_value = smooth
        self.tree.link(self.sock, n.inputs["Geometry"])
        return Geo(self.tree, Sock(self.tree, n.outputs["Geometry"], n))

    def realize(self):
        """Bake instances down to real geometry.

        Needed after ``instance_on_points``: an Instances output has no vertices
        of its own, so a realized mesh is the only thing to_mesh() can see.
        """
        n = self.tree.new("GeometryNodeRealizeInstances")
        self.tree.link(self.sock, n.inputs["Geometry"])
        return Geo(self.tree, Sock(self.tree, n.outputs["Geometry"], n))

    def subdivide(self, level=1):
        n = self.tree.new("GeometryNodeSubdivideMesh")
        n.inputs["Level"].default_value = level
        self.tree.link(self.sock, n.inputs["Mesh"])
        return Geo(self.tree, Sock(self.tree, n.outputs["Mesh"], n))

    def delete_faces(self, selection=None):
        """Delete faces. With no selection this deletes everything."""
        n = self.tree.new("GeometryNodeDeleteGeometry")
        n.domain = 'FACE'
        self.tree.link(self.sock, n.inputs["Geometry"])
        if selection is not None:
            self.tree.link(selection, n.inputs["Selection"])
        return Geo(self.tree, Sock(self.tree, n.outputs["Geometry"], n))

    # -- field access (for selection / attribute-driven values) ------------
    def position(self):
        return self._position()

    def _position(self):
        n = self.tree.new("GeometryNodeInputPosition")
        return Sock(self.tree, n.outputs["Position"], n)

    def noise(self, scale=5.0, detail=2.0, dimension='3D'):
        n = self.tree.new("ShaderNodeTexNoise")
        n.noise_dimensions = dimension
        n.inputs["Scale"].default_value = scale
        n.inputs["Detail"].default_value = detail
        return Sock(self.tree, n.outputs["Fac"], n)

    def index(self):
        n = self.tree.new("GeometryNodeInputIndex")
        return Sock(self.tree, n.outputs["Index"], n)

    def random(self, seed=None):
        n = self.tree.new("FunctionNodeRandomValue")
        n.data_type = 'FLOAT_VECTOR'
        if seed is not None:
            self.tree.link(seed, n.inputs["Seed"])
        return Sock(self.tree, n.outputs[0], n)

    def value(self, v):
        n = self.tree.new("ShaderNodeValue")
        n.outputs[0].default_value = _as_vec(v, False)
        return Sock(self.tree, n.outputs[0], n)

    def combine(self, x, y, z):
        n = self.tree.new("ShaderNodeCombineXYZ")
        for sock, key in ((x, "X"), (y, "Y"), (z, "Z")):
            if isinstance(sock, Sock):
                self.tree.link(sock, n.inputs[key])
            else:
                n.inputs[key].default_value = _as_scalar(sock)
        return Sock(self.tree, n.outputs["Vector"], n)

    def math(self, op, a, b=None):
        n = self.tree.new("ShaderNodeMath")
        n.operation = op
        if isinstance(a, Sock):
            self.tree.link(a, n.inputs[0])
        else:
            n.inputs[0].default_value = _as_scalar(a)
        if b is not None:
            if isinstance(b, Sock):
                self.tree.link(b, n.inputs[1])
            else:
                n.inputs[1].default_value = _as_scalar(b)
        return Sock(self.tree, n.outputs[0], n)

    def compare(self, op, a, b, data_type='FLOAT'):
        """Field comparison -- the workhorse of delete_faces selection.

        FunctionNodeCompare carries one A/B socket *per* data type, all sharing
        the names "A"/"B", so the correct pair has to be picked by
        data_type rather than by name.
        """
        n = self.tree.new("FunctionNodeCompare")
        n.operation = op
        n.data_type = data_type
        order = ["FLOAT", "INT", "VECTOR", "STRING", "ROTATION", "MATRIX", "COLOR"]
        if data_type not in order:
            raise gnkit_error(f"unsupported compare data_type: {data_type}")
        base = order.index(data_type) * 2
        for idx, val in ((base, a), (base + 1, b)):
            if isinstance(val, Sock):
                self.tree.link(val, n.inputs[idx])
            elif n.inputs[idx].type == 'VECTOR':
                n.inputs[idx].default_value = _as_vec(val, True)
            else:
                n.inputs[idx].default_value = _as_scalar(val)
        return Sock(self.tree, n.outputs["Result"], n)


# --------------------------------------------------------------------------
# Tree: owns the node group, hosts link/assign/new
# --------------------------------------------------------------------------
class GN:
    """A geometry node group under construction."""

    def __init__(self, name="GeometryNodes"):
        self.group = bpy.data.node_groups.new(name, "GeometryNodeTree")
        self._in = self.group.nodes.new("NodeGroupInput")
        self._in.location = (-900, 0)
        self._out = self.group.nodes.new("NodeGroupOutput")
        self._out.location = (900, 0)
        self.group.interface.new_socket(
            "Geometry", in_out='OUTPUT', socket_type="NodeSocketGeometry"
        )
        self._out.is_active_output = True
        self.count = 0

    # -- node plumbing ------------------------------------------------------
    def new(self, node_type):
        node = self.group.nodes.new(node_type)
        self.count += 1
        # Auto-layout: a simple column per "depth" so the graph stays readable.
        node.location = (-600 + (self.count % 6) * 190, -260 - (self.count // 6) * 170)
        node.label = node.bl_idname.replace("GeometryNode", "").replace("ShaderNode", "")
        return node

    def link(self, from_sock, to_sock):
        src = _unwrap(from_sock)
        self.group.links.new(src, to_sock)
        return to_sock

    def assign(self, sock, value):
        s = _unwrap(sock)
        if s.type == 'VECTOR' and not isinstance(value, Sock):
            value = _as_vec(value, True)
        elif s.type == 'VALUE' and not isinstance(value, Sock):
            value = _as_scalar(value)
        s.default_value = value
        return sock

    def inputs_of(self, node_type):
        """Probe a node type's input names without instantiating it."""
        return _inputs_of(self.group, node_type)

    def const(self, v):
        """A Value node holding a constant -- handy inside a link."""
        n = self.new("ShaderNodeValue")
        n.outputs[0].default_value = _as_vec(v, False)
        return Geo(self, Sock(self, n.outputs[0], n))

    # -- entry points -------------------------------------------------------
    def geo(self):
        """A chainable geometry source.

        Chain-off points here are *sources*, not the group output -- linking
        an output socket into a node is illegal in Blender ("Same
        input/output direction of sockets"). The real group output is wired
        by ``finish()``.
        """
        if not hasattr(self, "_src") or self._src is None:
            self._src = self.new("GeometryNodeMeshLine")
        return Geo(self, Sock(self, self._src.outputs["Mesh"], self._src))

    def finish(self, geo):
        """Wire the finished chain into the group's output."""
        self.link(geo.sock if isinstance(geo, Geo) else geo, self._out.inputs[0])
        return self

    def input(self, name):
        return Sock(self, self._in.outputs[name], self._in)

    def realize(self, target=None, save_as=None):
        """Evaluate the modifier and optionally save the .blend.

        Without this the node graph is just a description; nothing exists in
        the scene until Blender evaluates it. ``realize()`` forces evaluation
        and reports the object counts so a script can assert the build worked.
        """
        if target is not None:
            target.modifiers.clear()
            mod = target.modifiers.new(name="GeometryNodes", type='NODES')
            mod.node_group = self.group
        dg = bpy.context.evaluated_depsgraph_get()
        dg.update()
        if target is not None:
            ev = target.evaluated_get(dg)
            try:
                mesh = ev.to_mesh()
                counts = {
                    "verts": len(mesh.vertices),
                    "faces": len(mesh.polygons),
                    "objects": len(mesh.objects) if hasattr(mesh, "objects") else 1,
                }
                ev.to_mesh_clear()
            except Exception as exc:  # evaluation may fail on empty graphs
                counts = {"error": str(exc)}
        else:
            counts = {}
        if save_as:
            bpy.ops.wm.save_as_mainfile(filepath=save_as)
        return counts


# --------------------------------------------------------------------------
# Module-level conveniences
# --------------------------------------------------------------------------
_CONE_SOCKETS = None


def _cone_socket_names():
    """锥体两个半径的真实 socket 名，**实测出来的，不靠记忆**。

    5.x 把 `Radius` / `Radius 2` 改名成 `Radius Bottom` / `Radius Top`；
    按老名字写会被静默忽略，于是 cone(radius1=3) 和 cone(radius1=1) 出来一样大。
    """
    global _CONE_SOCKETS
    if _CONE_SOCKETS is None:
        import bpy
        ng = bpy.data.node_groups.new("__cone_probe", "GeometryNodeTree")
        try:
            names = [i.name for i in ng.nodes.new("GeometryNodeMeshCone").inputs]
        finally:
            bpy.data.node_groups.remove(ng)
        bottom = next((n for n in ("Radius Bottom", "Radius") if n in names), None)
        top = next((n for n in ("Radius Top", "Radius 2") if n in names), None)
        if not bottom or not top:
            raise KeyError(f"认不出锥体的半径 socket 名，实际可用：{names}")
        _CONE_SOCKETS = (bottom, top)
    return _CONE_SOCKETS


def tree(name="GeometryNodes"):
    return GN(name)


def object(name, gntree=None, location=(0, 0, 0)):
    """Create (or reuse) a mesh object wired to a geometry node group.

    An empty mesh datablock is mandatory: Blender refuses to create a NODES
    modifier on a None-data object and returns None instead of raising, which
    then fails much later with a confusing 'NoneType has no node_group'.
    """
    obj = bpy.data.objects.get(name)
    if obj is None:
        obj = bpy.data.objects.new(name, bpy.data.meshes.new(f"{name}_mesh"))
    elif obj.data is None:
        obj.data = bpy.data.meshes.new(f"{name}_mesh")
    if location != (0, 0, 0):
        obj.location = _as_vec(location, True)
    if gntree is not None:
        mod = obj.modifiers.get("GeometryNodes")
        if mod is None:
            mod = obj.modifiers.new(name="GeometryNodes", type='NODES')
        mod.node_group = gntree.group
    if obj.name not in bpy.context.collection.objects:
        bpy.context.collection.objects.link(obj)
    return obj


def material(name, color=(0.8, 0.8, 0.8, 1.0), metallic=0.0, roughness=0.5,
             emission=None, emission_strength=0.0):
    """Create or fetch a Principled BSDF material."""
    mat = bpy.data.materials.get(name)
    if mat is None:
        mat = bpy.data.materials.new(name)
    mat.use_nodes = True
    bsdf = next((n for n in mat.node_tree.nodes if n.type == 'BSDF_PRINCIPLED'), None)
    if bsdf is None:
        bsdf = mat.node_tree.nodes.new("ShaderNodeBsdfPrincipled")
        out = mat.node_tree.nodes.new("ShaderNodeOutputMaterial")
        mat.node_tree.links.new(bsdf.outputs[0], out.inputs[0])
    bsdf.inputs["Base Color"].default_value = _as_rgba(color)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    if emission is not None:
        if "Emission Color" in bsdf.inputs:
            bsdf.inputs["Emission Color"].default_value = _as_rgba(emission)
        if "Emission Strength" in bsdf.inputs:
            bsdf.inputs["Emission Strength"].default_value = emission_strength
    return mat


def light(name, kind='SUN', energy=5.0, location=(0, 0, 10), color=(1, 1, 1), angle=None):
    """Sun / point / area light."""
    data = bpy.data.lights.new(name, type=kind)
    data.energy = energy
    data.color = _as_vec(color, False)[:3]
    if angle is not None and kind == 'SUN':
        data.angle = math.radians(angle)
    obj = bpy.data.objects.new(name, data)
    obj.location = _as_vec(location, True)
    bpy.context.collection.objects.link(obj)
    return obj


def camera(name, location=(0, -6, 3), look_at=(0, 0, 1), lens=50.0):
    """Camera aimed at a point -- the common case, without manual Euler math."""
    from mathutils import Vector
    data = bpy.data.cameras.new(name)
    data.lens = lens
    obj = bpy.data.objects.new(name, data)
    obj.location = _as_vec(location, True)
    direction = Vector(_as_vec(look_at, True)) - Vector(_as_vec(location, True))
    obj.rotation_euler = direction.to_track_quat('-Z', 'Y').to_euler()
    bpy.context.collection.objects.link(obj)
    bpy.context.scene.camera = obj
    return obj


def _inputs_of(tree, node_type):
    """List a node type's input names by instantiating a throwaway instance."""
    probe = bpy.data.node_groups.new("__probe", "GeometryNodeTree")
    try:
        node = probe.nodes.new(node_type)
        return [s.name for s in node.inputs]
    finally:
        bpy.data.node_groups.remove(probe)


# --------------------------------------------------------------------------
# Coercion helpers
# --------------------------------------------------------------------------
def _as_scalar(v):
    if isinstance(v, (int, float)):
        return float(v)
    if isinstance(v, (tuple, list)):
        return float(v[0]) if v else 0.0
    return 0.0


def _as_rgba(v):
    """Colour sockets need 4 components; accept RGB or RGBA."""
    vals = [float(x) for x in (v if isinstance(v, (tuple, list)) else (v, v, v))]
    while len(vals) < 4:
        vals.append(1.0)
    return tuple(vals[:4])


def _unwrap(v):
    """Return the underlying bpy socket for a Sock, a Geo, or a raw socket."""
    if isinstance(v, Sock):
        return v.socket
    if isinstance(v, Geo):
        return _unwrap(v.sock)
    return v


def _as_vec(v, force_vector):
    if isinstance(v, Sock):
        return v
    if isinstance(v, (int, float)):
        return (float(v), float(v), float(v)) if force_vector else float(v)
    if isinstance(v, (tuple, list)):
        vals = [float(x) for x in v]
        while len(vals) < 3:
            vals.append(1.0 if force_vector and vals else 0.0)
        return tuple(vals[:3])
    return (0.0, 0.0, 0.0)
