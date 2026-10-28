# 已下线脚本（2026-10-04 归档）

这两个脚本属于旧的图像推断路线，**当前不可用**，已从 `scripts/` 移出以免被误执行。

| 脚本 | 状态 | 原因 |
|---|---|---|
| `local_triposr.py` | ❌ 不可用 | 依赖 TripoSR 权重 `triposr/weights/model.ckpt`，本机**不存在**（已查 `~/.cache/torch`、`~/.triposr`、`~/triposr` 均无） |
| `tripo_gen.sh` | ❌ 已停用 | Tripo 云端 API 路线下线。注意 `~/.local/bin/tripo` 与 `~/.tripo_key_cn` **仍在**，但该路线已按需求停用，不要执行 |
| `test_local_post.py` | ❌ 失效 | 双重失效：`trimesh` 未安装（`ModuleNotFoundError`），且 import 的 `local_triposr` 已移出 |

**替代路线**：Blender 真实建模，见 `../SKILL.md` 与
`../references/blockout-character.py`（可跑通的人形建模 + 三视角渲染）。

保留这些文件仅为回退可能，**不要在 `scripts/` 里恢复它们**。
