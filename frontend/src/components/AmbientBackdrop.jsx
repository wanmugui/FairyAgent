import React, { useEffect, useRef } from 'react';

const LOGOS = Array.from({ length: 6 });

/* 复合带条数。
   要铺满整屏：相邻复合带在法线方向贴紧时，每条占 314×sin45 ≈ 222px，
   屏幕 p 跨度 = (宽+高)×0.7071 ≈ 1655px，
   8 条覆盖 1554px 刚好铺满，9 条留一点余量防两端露空。
   条数写死在这里，不用 CSS 生成 —— 斜带要各自独立的走带动画和
   奇偶配色，DOM 节点必须是实体。 */
const BAND_COUNT = Array.from({ length: 9 }, (_, i) => i);

/* 带子与带子之间的“缝隙细节”内容。
   参考 ZZZ 海报墙：大标题之外还有小号副信息、条形码、警示块。
   这些东西填在缝里，空缺就不会读成“缺了一块”。
   数量刻意压低 —— 密度过高会把背景墙读成噪点。 */
const DETAIL_TAGS = [
  'AGENCY // 07',
  'EVIDENCE 001',
  'ZONE H-3',
  'NO. 0421',
  'FILE COPY',
  'ROUTE 12-B',
];

const DETAIL_BARS = [14, 8, 22, 5, 17, 10];

function LogoUnit() {
  return (
    <span className="zzz-logo-unit">
      <b>FAIRY</b>
    </span>
  );
}

function LogoRun({ prefix }) {
  return (
    <span className="zzz-logo-run">
      {LOGOS.map((_, index) => <LogoUnit key={prefix + '-' + index} />)}
    </span>
  );
}

/* 细节带的一节：小号等宽标签 + 条形码 + 实心色块 */
function DetailUnit({ tag, bars }) {
  return (
    <span className="zzz-detail-unit">
      <span className="zzz-detail-tag">{tag}</span>
      <span className="zzz-detail-barcode">
        {bars.map((w, i) => (
          <i key={i} style={{ width: w }} />
        ))}
      </span>
      <span className="zzz-detail-chip" />
    </span>
  );
}

function DetailRun({ prefix }) {
  return (
    <span className="zzz-detail-run">
      {DETAIL_TAGS.map((tag, i) => (
        <DetailUnit
          key={prefix + '-' + i}
          tag={tag}
          bars={DETAIL_BARS.slice(i % 4).concat(DETAIL_BARS.slice(0, i % 4))}
        />
      ))}
    </span>
  );
}

/* ZZZ 滚动海报墙 */
export default function AmbientBackdrop() {

  // ── 走带宽度测量 ──
  // 背景走带的关键帧原本用 translate3d(-50%, 0, 0)：百分比基准是元素自身宽度。
  // 字体加载完成前后走带内容宽度会变，-50% 的绝对值随之跳变，
  // 多条走带各自独立跳 → 整屏抖动（实测逐帧位移差在 -0.88 ~ -5.21 之间乱跳）。
  // 这里把「单个 run 的宽度」测出来写成 --zzz-band-w（px），
  // 关键帧改用 calc(-1 * var(--zzz-band-w, 50%))，基准固定、不随布局重算。
  const bandRootRef = useRef(null);
  useEffect(() => {
    const root = bandRootRef.current;
    if (!root) return undefined;

    const measure = () => {
      root.querySelectorAll('.zzz-logo-scroll').forEach((scroll) => {
        // 每条走带里是 2 个 run，动画走的是「一遍」的长度，
        // 所以取第一个子元素（单遍 run）的宽度。
        const run = scroll.firstElementChild;
        if (!run) return;
        const w = run.getBoundingClientRect().width;
        if (w > 0) scroll.style.setProperty('--zzz-band-w', `${w}px`);
      });
    };

    measure();
    // 字体加载完成后内容宽度会变，必须重测一次
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(measure).catch(() => {});
    }
    const ro = new ResizeObserver(measure);
    ro.observe(root);
    window.addEventListener('resize', measure);
    return () => {
      ro.disconnect();
      window.removeEventListener('resize', measure);
    };
  }, []);
  return (
    <div className="zzz-ambient-backdrop zzz-bg-zzz" ref={bandRootRef} aria-hidden="true">
          {/* ── 复合带循环 ──
              一条斜带 = 一个 .zzz-composite-band 容器，容器整体倾斜 -45deg，
              内部用 flex-direction: column 竖排三层：
                1) FAIRY 大字带（最粗）
                2) ZONE h-3 条形码装饰条（居中）
                3) 电影胶卷条（最细）

              关键：先在未倾斜的坐标系里把三层摆好，再整体倾斜一次。
              之前给每层单独加 rotate，用 left 偏移想制造「垂直分层」，
              结果 left 在斜带里是沿带长方向平移，分层根本不成立 ——
              胶卷条和大字带落在同一层被完全盖住，屏幕上只剩一条胶卷。

              条数由 CSS 变量 --zzz-band-count 决定（要铺满整屏，
              11 条起步），深浅交替由 --band 变量在 CSS 里按奇偶覆盖。
              JSX 只负责把结构循环出来，样式一律交给 CSS。 */}
          {BAND_COUNT.map((i) => (
            <div
              className="zzz-composite-band"
              key={i}
              data-band={i}
              /* 深浅交替直接内联，不走 CSS 属性选择器。
                 属性选择器 .zzz-composite-band[data-band] 在浏览器里
                 匹配得上（matches() 返回 true），但那条规则始终没进
                 CSSOM —— 页面读到的 --band 一直是兜底值，字色也就一直是白的。
                 内联样式优先级最高，不受这个问题影响。 */
              style={i % 2 === 0
                /* 浅色带：底色压到 .30，让下方那层电影胶卷的纹理透上来。
                   .62 时胶卷被完全盖住，背景只剩一条平涂的白带；
                   降到 .30 后白带变成半透明，胶卷的齿孔剪影隐约可见，
                   黑字仍够清楚（深色字在半透明浅底上对比依然足够）。 */
                ? { '--band': 'rgba(232, 233, 228, .30)', '--band-ink': '#10140f', '--band-ink-stroke': 'rgba(16, 20, 15, .68)' }
                /* 深色带：同样留一点透明，胶卷的黑片基与齿孔能从底下透出轮廓。 */
                : { '--band': 'rgba(14, 17, 16, .55)', '--band-ink': '#eef1f2', '--band-ink-stroke': 'rgba(232, 234, 236, .92)' }}
            >
              {/* 三层并列，彼此独立、紧贴但不融合：
                  1) FAIRY 大字带
                  2) ZONE h-3 条形码装饰条
                  3) 电影胶卷条
                  胶卷是独立的第三层，不垫在字底下。 */}
              <div className="zzz-band-layer zzz-band-layer--logo">
                <div className="zzz-logo-scroll">
                  <LogoRun prefix={`a${i}1`} />
                  <LogoRun prefix={`a${i}2`} />
                </div>
              </div>
              <div className="zzz-band-layer zzz-band-layer--detail">
                <div className="zzz-logo-scroll">
                  <DetailRun prefix={`d${i}1`} />
                  <DetailRun prefix={`d${i}2`} />
                </div>
              </div>
              <div className="zzz-band-layer zzz-band-layer--film">
                <div className="zzz-film-run" />
              </div>
            </div>
          ))}
    </div>
  );
}
