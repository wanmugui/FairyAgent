// L 型安装支架 —— 带圆角和四个安装孔的示例零件
//
// 参数合法区间用注释声明，model.py check 会读它并强制校验：
//   model.py check examples/bracket.scad --set width=200   # 越界会报错退出
//   model.py check examples/bracket.scad --set width=100   # 正常
//
// @param width 40 140
// @param depth 30 120
// @param height 20 100
// @param thick 3 25
// @param fillet 2 20
// @param hole_d 3 25
// @param hole_inset 8 40

width     = 80;   // X 向总宽
depth     = 60;   // Y 向总深（底板）
height    = 40;   // 立板高度
thick     = 5;    // 板材厚
fillet    = 8;    // 外圆角半径
hole_d    = 6;    // 安装孔直径
hole_inset= 12;   // 孔心到边的距离

$fn = 64;
eps = 0.01;

// ---------- 几何模块 ----------

// XY 平面的圆角板：厚度沿 Z，底面在 z=0
module plate_xy(w, d, t, r) {
    hull()
        for (sx = [-1, 1], sy = [-1, 1])
            translate([sx * (w / 2 - r), sy * (d / 2 - r), 0])
                cylinder(h = t, r = r);
}

// XZ 平面的圆角板：厚度沿 Y、中心落在 y=0，**高度以 z=0 为中心**
// 注意这个模块不自带 z 基准，调用方必须自己把它抬到想要的高度。
module plate_xz(w, h, t, r) {
    translate([0, -t / 2, 0])
        hull()
            for (sx = [-1, 1], sz = [-1, 1])
                translate([sx * (w / 2 - r), 0, sz * (h / 2 - r)])
                    rotate([-90, 0, 0])
                        cylinder(h = t, r = r);
}

// 角撑加强筋：YZ 平面的直角三角形，沿 X 拉伸 tx，x 居中。
// 三角形从 y=0 往 **-Y** 方向长到 -ty（不是 +Y），
// 调用方把它 translate 到立板内表面，让它朝前长；朝 +Y 长会从后缘捅出去。
module gusset(ty, tz, tx) {
    translate([-tx / 2, 0, 0])
        rotate([90, 0, 90])
            linear_extrude(height = tx)
                polygon([[0, 0], [-ty, 0], [0, tz]]);
}

module bracket() {
    difference() {
        union() {
            // 底板
            plate_xy(width, depth, thick, fillet);
            // 立板，贴着底板后缘。plate_xz 以 z=0 为中心，必须抬 height/2
            // 让它坐在底板上，否则一半沉到 z=0 以下、立板顶的孔会打空。
            translate([0, depth / 2 - thick / 2, height / 2])
                plate_xz(width, height, thick, fillet);
            // 中间加强筋：根部贴在立板内表面 y=depth/2-thick，朝前长
            translate([0, depth / 2 - thick, thick])
                gusset(depth / 2 - thick, height - thick, thick * 1.2);
        }

        // 底板两个安装孔（靠近自由边），从底面向下切，通孔
        for (sx = [-1, 1])
            translate([sx * (width / 2 - hole_inset), -depth / 2 + hole_inset, -eps])
                cylinder(h = thick + 2 * eps, d = hole_d);

        // 立板两个安装孔（靠近顶边）。
        // 起点必须是立板**内表面** y=depth/2-thick，不能用中面：
        // 从中面起切只切掉后半块板，变成从背面看有孔、正面看不见的盲孔。
        for (sx = [-1, 1])
            translate([sx * (width / 2 - hole_inset), depth / 2 - thick, height - hole_inset])
                rotate([-90, 0, 0])
                    cylinder(h = thick + 2 * eps, d = hole_d);
    }
}

// ---------- 自检：几何前提，违反就直接报错而不是渲出个废件 ----------

// 圆角：半径必须小于最短边的半宽，否则 hull 直接退化
assert(fillet < min(width, depth) / 2 - 1, str("圆角太大：fillet < min(width,depth)/2 - 1"));
// 孔位：离边要够远，既不能压到圆角起点，也不能撞到另一条边
assert(hole_inset > fillet + 1, str("孔位太靠边：hole_inset > fillet + 1"));
assert(hole_inset < depth / 2 - fillet, str("孔位撞到后缘"));
// 孔必须整圈落在圆角内侧的平直区：孔的外缘 < 圆角起点，否则会啃掉圆角
assert(width / 2 - hole_inset + hole_d / 2 < width / 2 - fillet, str("立板孔啃到侧边圆角"));
assert(depth / 2 - hole_inset + hole_d / 2 < depth / 2 - fillet, str("底板孔啃到前边圆角"));
assert(hole_d < hole_inset * 2, str("孔太大：hole_d < 2 * hole_inset"));
// 立板孔必须整圈落在板内
assert(height - hole_inset > hole_d / 2 + 1, str("立板孔溢出板顶"));
assert(height - hole_inset + hole_d / 2 < height, str("立板孔穿出板顶"));
// 板要够高够厚才放得下
assert(height > fillet + hole_inset + 2, str("立板太矮：height > fillet + hole_inset + 2"));
assert(thick * 1.2 < width / 2 - fillet, str("加强筋太厚"));

bracket();
