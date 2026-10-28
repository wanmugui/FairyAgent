// tsconfig 说明（注释放在同目录 README 里，这个文件必须保持严格 JSON：
// 仓库的提交钩子用 JSON.parse 校验，尾随逗号会让提交直接失败。）
//
// 渐进迁移的三个核心开关，缺一不可：
//
//   allowJs: true   —— 让存量 .js/.jsx 继续被类型检查器解析，因此不需要一次性
//                      改写整个前端；组件层可以按触碰频率慢慢转。
//   checkJs: false  —— 暂不对存量 JS 报错，只检查主动转成 .ts 的文件。
//                      把它改成 true 的那一天，就是 JS 债还清完的那一天。
//   strict: true    —— 只约束 .ts。转进来的每个文件都必须真过 strict，否则
//                      「转 TS」会退化成「改个后缀骗过检查器」。
//
// 另外开了 noUncheckedIndexedAccess：数组下标访问视为可能 undefined。
// 它在 ttsText.ts / sessionDate.ts 里当场抓出了三处纯 JS 时代靠隐式转换
// 掩盖的类型松散。
