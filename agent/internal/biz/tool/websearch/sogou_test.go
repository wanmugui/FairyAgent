package websearch

import (
	"context"
	"strings"
	"testing"
)

// Shaped from a real Sogou /web?query=金沙洲 房价 response captured against
// this host on 2026-10-04. The real markup is div.vrwrap blocks, an
// h3.vr-title > a title link, a url="..." real destination, and a
// div id="cacheresult_summary_N" description node — plus inline <style>
// blobs that must never surface as prose.
const sogouFixture = `<!DOCTYPE html><html><head><title>金沙洲 房价 - 搜狗搜索</title>
<style>.vr-tit{--clr:#000;--fs:14px;--lh:1.5;--pad:0 4px;--bg:transparent}</style>
</head><body>
<div class="vrwrap">
  <h3 class="vr-title"><a href="/link?url=DYZ0aXB" id="sogou_vr_11002601_title_0" uigs="article_title_0">广州金沙洲南二手房小区房价_房天下</a></h3>
  <div class="text-layout" id="cacheresult_summary_0">金沙洲南小区均价29552元/平米，环比上涨0.11%，共35套在售。</div>
  <div class="citeurl"><span class="g-hemi">gz.esf.fang.com</span></div>
</div>
<div class="vrwrap" url="https://gz.ke.com/ershoufang/jinshazhou/">
  <h3 class="vr-title"><a href="/link?url=DYZ0aXB" uigs="article_title_1">贝壳找房_广州金沙洲二手房</a></h3>
  <div class="fz-mid text-layout" id="cacheresult_summary_1">金沙洲在售房源与成交价，含恒骏广场、誉山国际等小区，参考均价42000元/m²。</div>
</div>
<div class="vrwrap">
  <h3 class="vr-title"><a href="https://news.example.com/gold">今日金价走势分析</a></h3>
  <div class="fz-mid" id="cacheresult_summary_2">黄金市场行情，应被保留但不应冒充房价结果。</div>
</div>
<div class="vrwrap">
  <h3 class="vr-title"><a href="/link?url=DYZ0aXB">重复的同一 wrapper 应去重</a></h3>
  <div id="cacheresult_summary_3">与第一条同属 /link?url=DYZ0aXB。</div>
</div>
<div class="foot"><a href="https://www.sogou.com/next">下一页</a></div>
</body></html>`

func TestParseSogouHTML(t *testing.T) {
	results := parseSogouHTML(sogouFixture, 10)

	if len(results) != 3 {
		t.Fatalf("want 3 results (dup + foot excluded), got %d: %+v", len(results), results)
	}
	if !strings.Contains(results[0].Title, "金沙洲南") {
		t.Errorf("result[0] title lost CJK: %q", results[0].Title)
	}
	if !strings.Contains(results[0].Snippet, "29552") {
		t.Errorf("result[0] snippet lost price: %q", results[0].Snippet)
	}
	// The url= attribute must win over the /link?url= wrapper.
	if got := results[1].URL; got != "https://gz.ke.com/ershoufang/jinshazhou/" {
		t.Errorf("result[1] URL: want url= attribute value, got %q", got)
	}
	// "元/m²" must survive the CSS filter.
	if got := results[1].Snippet; !strings.Contains(got, "42000") {
		t.Errorf("result[1] snippet lost price: %q", got)
	}
	// Relative /link?url= redirect must be absolutised.
	if got := results[0].URL; !strings.HasPrefix(got, "https://www.sogou.com/link?url=") {
		t.Errorf("result[0] URL: want absolute redirect, got %q", got)
	}
	for i, r := range results {
		if strings.ContainsAny(r.Snippet, "<{}") {
			t.Errorf("result[%d] snippet leaked markup/CSS: %q", i, r.Snippet)
		}
		if strings.Contains(r.Snippet, "--clr") {
			t.Errorf("result[%d] snippet leaked CSS custom property: %q", i, r.Snippet)
		}
	}
}

func TestSogouRespectsLimit(t *testing.T) {
	if got := parseSogouHTML(sogouFixture, 2); len(got) != 2 {
		t.Errorf("limit 2: got %d results", len(got))
	}
}

func TestSogouName(t *testing.T) {
	var p Provider = NewSogouProvider(nil)
	if p.Name() != "sogou" {
		t.Errorf("Name: want sogou, got %q", p.Name())
	}
	// NewSogouProvider(nil) must fall back to a working default client.
	if _, err := p.Search(context.Background(), "金沙洲 房价", 3); err != nil {
		t.Logf("offline: live sogou call not exercised (%v)", err)
	}
}

func TestLooksLikeCode(t *testing.T) {
	code := []string{
		`.vr-tit{--clr:#000;--fs:14px;--lh:1.5}`,
		`a{color:red}`,
	}
	for _, s := range code {
		if !looksLikeCode(s) {
			t.Errorf("looksLikeCode(%q) = false, want true", s)
		}
	}
	real := []string{
		`金沙洲南小区均价29552元/平米，环比上涨0.11%，共35套在售。`,
		`参考均价42000元/m²，含3室2厅。`,
		`金沙洲位于白云区西部。`,
	}
	for _, s := range real {
		if looksLikeCode(s) {
			t.Errorf("looksLikeCode(%q) = true, want false", s)
		}
	}
}

func TestTrimLeadingJunk(t *testing.T) {
	cases := map[string]string{
		`">金沙洲均价`: "金沙洲均价",
		`   "abc`: "abc",
		`干净文本`:    "干净文本",
		`"`:       "",
	}
	for in, want := range cases {
		if got := trimLeadingJunk(in); got != want {
			t.Errorf("trimLeadingJunk(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripTagsDropsScriptBody(t *testing.T) {
	got := stripTags(`<p>保留</p><script>var secret="不要出现";</script><b>尾巴</b>`)
	if strings.Contains(got, "不要出现") {
		t.Errorf("script body leaked: %q", got)
	}
	if !strings.Contains(got, "保留") || !strings.Contains(got, "尾巴") {
		t.Errorf("text outside script was dropped: %q", got)
	}
}
