# 已下线脚本（2026-10-04 归档）

| 脚本 | 状态 | 原因 |
|---|---|---|
| `to3d.py` | ❌ 不可用 | 依赖 TripoSR 权重，本机不存在 |
| `preview_glb.py` | ⚠️ 随之下线 | 依赖 `to3d.py` 的产物做预览 |

**替代路线**：Blender 真实建模，见 `../SKILL.md` 第 2 步，
或直接跑 `skills/sculpting-character-pipeline/references/blockout-character.py`。

保留仅为回退可能，**不要在 `scripts/` 里恢复**。
