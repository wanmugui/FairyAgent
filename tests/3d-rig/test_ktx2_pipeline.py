"""KTX2 编码接线的回归用例。

守护的是一次真实踩过的坑：接线时把整个 BIN chunk 替换成 KTX2，
positions/normals/indices 全被抹掉。结构校验（magic、扩展名）照样全过——
只有"几何 bufferView 是否还落在 buffer 范围内"才抓得住，所以专门测这条。

需要 basisu。缺失则 skip，不让这条阻塞其余测试。
"""
import json
import os
import struct
import sys

import pytest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "workspace"))
import ktx2_encode as K  # noqa: E402

ARTIFACT = "/home/user/Fairy/workspace/result/ktx2_final.glb"
SOURCE = "/home/user/Fairy/workspace/result/ktx2_src.glb"

pytestmark = pytest.mark.skipif(
    not os.path.exists(ARTIFACT) or not os.path.exists(SOURCE),
    reason="缺少 KTX2 产物或源 GLB（先跑 workspace/ktx2_src_build.py 与 ktx2_encode.py）")


def _glb():
    return K.parse(ARTIFACT)


def test_贴图bufferView里确实是KTX2():
    js, b = _glb()
    bv = js["bufferViews"][js["images"][0]["bufferView"]]
    data = b[bv["byteOffset"]: bv["byteOffset"] + bv["byteLength"]]
    assert data[:12] == K.KTX2_MAGIC, "图片 bufferView 内容不是 KTX2"


def test_KTX2头字段自洽():
    js, b = _glb()
    bv = js["bufferViews"][js["images"][0]["bufferView"]]
    data = b[bv["byteOffset"]: bv["byteOffset"] + bv["byteLength"]]
    info = K._validate_ktx2(data)
    assert info["w"] > 1 and info["h"] > 1, "退化尺寸 %dx%d，测不出画质" % (info["w"], info["h"])
    assert info["scheme"] in (1, 2), "supercompressionScheme 非法: %s" % info["scheme"]
    assert info["levels"] >= 1


def test_所有bufferView都在buffer范围内():
    """这条就是守护"整体替换 BIN chunk"那个坑的。"""
    js, b = _glb()
    for i, bv in enumerate(js["bufferViews"]):
        end = bv["byteOffset"] + bv["byteLength"]
        assert end <= len(b), "bufferView[%d] 越界 end=%d > buffer=%d" % (i, end, len(b))


def test_几何数据数量未变():
    src, _ = K.parse(SOURCE)
    out, _ = K.parse(ARTIFACT)
    for key in ("meshes", "accessors", "buffers", "primitives"):
        assert len(src.get(key, [])) == len(out.get(key, [])), \
            "%s 数量变了 %d→%d" % (key, len(src.get(key, [])), len(out.get(key, [])))


def test_buffer长度覆盖全部bufferView():
    js, b = _glb()
    need = max(bv["byteOffset"] + bv["byteLength"] for bv in js["bufferViews"])
    assert js["buffers"][0]["byteLength"] >= need, \
        "buffer.byteLength=%d 小于实际需要 %d" % (js["buffers"][0]["byteLength"], need)


def test_扩展按schema接线():
    """纹理挂扩展、自身 source 被删、图片 mimeType 正确、required/used 齐全。"""
    js, _ = _glb()
    tex = js["textures"][0]
    assert "source" not in tex, "纹理自身 source 未按 schema 删除"
    assert tex["extensions"][K.EXT]["source"] == 0
    assert js["images"][0]["mimeType"] == "image/ktx2"
    assert K.EXT in js["extensionsRequired"], "扩展未列入 extensionsRequired"
    assert K.EXT in js["extensionsUsed"]


def test_不是1x1的退化贴图():
    """1x1 贴图能通过一切结构校验却零信息量——显式挡住这种假通过。"""
    js, b = _glb()
    bv = js["bufferViews"][js["images"][0]["bufferView"]]
    w, h = struct.unpack_from("<II", b, bv["byteOffset"] + 20)
    assert w >= 64 and h >= 64, "贴图仅 %dx%d，太小测不出问题" % (w, h)
