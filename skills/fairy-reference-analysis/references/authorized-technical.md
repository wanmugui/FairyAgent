# 授权技术分析协议

## 前置条件

只分析用户拥有或明确授权的软件。授权不明确时停止。

本协议不包含：

- DRM / 许可证 / 付费墙 / 反作弊绕过
- 账号接管、凭据提取或未授权访问
- 解密受保护资产或绕过签名校验
- 恶意软件行为或攻击基础设施

## 只读盘点顺序

1. 目录结构、版本文件和包清单
2. 引擎标记：Unity、Godot、Unreal、Electron、RPG Maker 等
3. 可执行文件、原生库、脚本、资源包和数据目录
4. 未加密 JSON / CSV / XML / YAML / 日志 / 配置
5. 运行进程、窗口、网络和本地状态

先识别，后分析。不要为了提高「完整性」而解密或反编译受保护内容。

## 常见引擎线索

- Unity：`UnityPlayer.dll`、`*_Data`、`global-metadata.dat`
- Unreal：`*.pak`、`Engine`、`Content`
- Godot：`*.pck`、`project.godot`
- Electron：`resources/app/package.json`、`resources/app.asar`
- RPG Maker：`data/System.json`、`js/rmmz_*.js`、`img/system`

## 运行时观察

只在授权范围内观察：

- 进程树、启动参数、工作目录和日志
- 网络请求、请求体结构和错误响应
- 文件变化、缓存、保存文件和配置
- UI 可访问性树、热键、菜单和状态转换

抓包时避免收集敏感信息；分析完成后按用户要求清理临时数据。

## 结果输出

- 技术栈和目录地图
- 未加密数据格式说明
- 接口/协议行为矩阵
- 可复现实验步骤
- 不能处理或未授权的范围