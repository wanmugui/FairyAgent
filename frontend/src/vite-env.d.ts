/// <reference types="vite/client" />

// 让 TS 认识 Vite 的特殊 import 语法：
//   import css from './x.css?raw'   → 字符串
//   import mod from './x?raw'      → 字符串
// 没有这个声明文件，exportHtml.ts 里的 ?raw 动态 import 会报 TS2307。
//
// ⚠️ 注意：本文件不能写成 .d.ts 之外的形式，vite/client 的三斜线引用必须保留。
