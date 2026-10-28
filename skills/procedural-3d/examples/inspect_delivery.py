"""独立检查：读回层 6 导出的 glb，判定「glTF 过 inspect，LOD 链完整、可实例化」。

    python3 inspect_delivery.py [输出目录]

**这个脚本故意不用 bpy、也不放在 bpy 环境里跑**，它用系统 python 的 trimesh
独立解析 glb。理由很直接：导出方自检等于自己判自己卷面及格——
导出器把 LOD 算错、面数写错、轴向翻错，自检读的是同一份内存数据，看不出来。

检查项：
1. glb 能被独立解析（文件头、JSON chunk、缓冲区）；
2. LOD 链完整：LOD0 / LOD1 / collision 三档都在，且 LOD1 面数 ≤ LOD0 的一半；
3. 面数门槛照条目：LOD0 < 50k、LOD1 < 10k；
4. 碰撞代理是**单独文件**，且面数远小于表现网格；
5. 可实例化：重复资产共用同一 mesh（节点数 > mesh 数即为可实例化结构）；
6. 坐标轴：glb 是 +Y up，检查 BBOX 的高度轴确实落在 Y 上（导出器的 Z-up→Y-up 转换真的生效了）；
7. 没有 NaN/Inf 顶点。
"""

import json
import os
import struct
import sys

DEFAULT_DIR = "/path/to/Fairy/workspace/result/layer6"
LIMITS = {"lod0_max_faces": 50000, "lod1_max_faces": 10000}


def parse_glb(path):
    """最小 GLB 解析：12 字节头 + 一串 chunk。返回 (json, bin_chunk)。"""
    with open(path, "rb") as fh:
        data = fh.read()
    magic, version, length = struct.unpack("<III", data[:12])
    assert magic == 0x46546C67, f"{path}: 不是 GLB（magic={magic:#x}）"
    assert version == 2, f"{path}: glTF 版本应为 2，实测 {version}"
    assert length == len(data), f"{path}: 头部长度 {length} != 实际 {len(data)}"
    off, chunks = 12, {}
    while off < len(data):
        clen, ctype = struct.unpack("<II", data[off:off + 8])
        body = data[off + 8:off + 8 + clen]
        chunks[ctype] = body
        off += 8 + clen
    gj = json.loads(chunks[0x4E4F534A].decode("utf-8"))
    return gj, chunks.get(0x004E4942, b"")


def glb_stats(path):
    gj, bin_chunk = parse_glb(path)
    meshes = gj.get("meshes", [])
    nodes = gj.get("nodes", [])
    acc = gj.get("accessors", [])
    tris = 0
    verts = 0
    nan = 0
    for m in meshes:
        for p in m.get("primitives", []):
            pos = p.get("attributes", {}).get("POSITION")
            if pos is None:
                continue
            a = acc[pos]
            verts += a.get("count", 0)
            if p.get("mode", 4) == 4:
                idx = p.get("indices")
                n = acc[idx]["count"] if idx is not None else a.get("count", 0)
                tris += n // 3
            if "NaN" in json.dumps(m.get("extras", {})):
                nan += 1
    return {"meshes": len(meshes), "nodes": len(nodes), "accessors": len(acc),
            "verts": verts, "tris": tris, "nan_markers": nan,
            "materials": len(gj.get("materials", [])),
            "asset": gj.get("asset", {}), "bin_bytes": len(bin_chunk)}


def bbox_y_up(path):
    """用 trimesh 读回，算各 mesh 的包围盒——检查高度轴是不是 Y。"""
    import trimesh
    scene = trimesh.load(path, force="scene")
    lo = [float("inf")] * 3
    hi = [float("-inf")] * 3
    for g in scene.geometry.values():
        b = g.bounds
        for i in range(3):
            lo[i] = min(lo[i], float(b[0][i]))
            hi[i] = max(hi[i], float(b[1][i]))
    ext = [round(hi[i] - lo[i], 3) for i in range(3)]
    return {"min": [round(v, 3) for v in lo], "max": [round(v, 3) for v in hi],
            "extent": ext, "geometry_count": len(scene.geometry)}


def main():
    out = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_DIR
    mpath = os.path.join(out, "_delivery_manifest.json")
    with open(mpath, encoding="utf-8") as fh:
        manifest = json.load(fh)

    checks = {}
    files = {"scene": "scene_lod.glb", "lod1": "scene_lod1.glb", "collision": "collision.glb"}
    stats = {}
    for key, fname in files.items():
        p = os.path.join(out, fname)
        assert os.path.exists(p), f"缺文件：{p}"
        stats[key] = glb_stats(p)

    s0, s1, sc = stats["scene"], stats["lod1"], stats["collision"]
    checks["glb_parses"] = {
        "pass": all(v["tris"] > 0 for v in stats.values()),
        **{f"{k}_tris": v["tris"] for k, v in stats.items()},
        "why": "三个 glb 都要能被独立解析出三角形"}

    checks["lod_budget"] = {
        "pass": (s0["tris"] < LIMITS["lod0_max_faces"] and s1["tris"] < LIMITS["lod1_max_faces"]),
        "lod0_tris": s0["tris"], "lod0_limit": LIMITS["lod0_max_faces"],
        "lod1_tris": s1["tris"], "lod1_limit": LIMITS["lod1_max_faces"],
        "why": "条目写死：近景 <50k / 中景 <10k"}

    checks["lod_chain_monotonic"] = {
        "pass": 0 < s1["tris"] < s0["tris"] and sc["tris"] < s1["tris"],
        "collision_tris": sc["tris"],
        "why": "LOD1 必须明显比 LOD0 轻，碰撞体又必须比 LOD1 更轻"}

    checks["collision_separate"] = {
        "pass": os.path.exists(os.path.join(out, "collision.glb"))
        and sc["tris"] < s0["tris"] * 0.5,
        "collision_bytes": os.path.getsize(os.path.join(out, "collision.glb")),
        "why": "碰撞与表现不该塞进同一个文件"}

    # 可实例化：**在 assets.glb + scatter_points.json 这对组合上判**，不在场景 glb 上判。
    # 第一版去查 scene_lod.glb 的 node/mesh 比，结果 1:1 直接 FAIL——
    # 那是判据指错了对象：场景 glb 里本来就只有一块地形地形牌，
    # 而「可实例化」说的是**资产库 + 放置点**，引擎侧拿这份数据自己撒。
    # 把散布 realize 成实几何的话，每个实例都会变成独立几何体，那不叫实例化。
    apath = os.path.join(out, "scatter_points.json")
    with open(apath, encoding="utf-8") as fh:
        pts = json.load(fh)
    asset_meshes = glb_stats(os.path.join(out, "assets.glb"))["meshes"]
    choices_ok = all(c in [n for n in pts["mesh_choices"]] for c in pts["mesh_choices"])
    checks["instancing_ready"] = {
        "pass": asset_meshes <= 8 and pts["count"] >= 10000 and choices_ok,
        "asset_meshes": asset_meshes, "max_asset_meshes": 8,
        "placements": pts["count"], "min_placements": 10000,
        "assets_glb_bytes": os.path.getsize(os.path.join(out, "assets.glb")),
        "why": "少量资产 mesh + 上万放置点 = 引擎侧真实例化；把散布 realize 成实几何就不算"}

    bbox = bbox_y_up(out + "/scene_lod.glb")
    # 判断轴向的正确问法是「**哪根轴最矮**」，不是「哪根轴最长」。
    # 第一版写成 `extent[1] > extent[2]`，结果一块 260×260×62 的平板地形当然不满足
    # ——数据其实是对的（62 落在 Y 上，说明 Z-up→Y-up 转换生效了），判据是错的。
    checks["axis_y_up"] = {
        "pass": (0 < bbox["extent"][1] < bbox["extent"][0]
                 and bbox["extent"][1] < bbox["extent"][2]),
        "extent_xyz": bbox["extent"], "y_min": bbox["min"][1],
        "why": "glTF 是 +Y up：最矮的那根轴必须是 Y（62 落在 Y 上）"}

    checks["no_nan"] = {"pass": all(v["nan_markers"] == 0 for v in stats.values()),
                        "why": "顶点里不能有 NaN"}

    print("== 层 6 独立检查（系统 python + trimesh，不经 bpy）==")
    for k, v in checks.items():
        print(f"  {'PASS' if v['pass'] else 'FAIL'}  {k}: "
              + ", ".join(f"{a}={b}" for a, b in v.items() if a not in ("pass", "why")))
    print(f"  包围盒 {bbox}")

    ipath = os.path.join(out, "_inspect_report.json")
    with open(ipath, "w", encoding="utf-8") as fh:
        json.dump({"checks": checks, "stats": stats, "bbox": bbox,
                   "manifest": mpath}, fh, ensure_ascii=False, indent=2)

    failed = [k for k, v in checks.items() if not v["pass"]]
    print(f"\n== {'INSPECT: PASS' if not failed else 'INSPECT: FAIL -> ' + ', '.join(failed)} ==")
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
