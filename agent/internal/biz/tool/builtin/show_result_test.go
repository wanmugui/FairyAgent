package builtin

import "testing"

// show_result 过去把预览地址硬编码成 http://localhost:8081。
// 远端用户（走 Cloudflare 隧道访问）点开就是他们自己电脑的 8081，
// 永远是空白——而且没有任何报错，看起来像"图坏了"。
//
// 这里锁住两件事：
//  1. 默认（没配 FAIRY_PUBLIC_BASE_URL）必须保持 localhost，本机用户不受影响；
//  2. 配了公网基址时，文件与 PPT 两种 URL 都必须带上该基址，且不能留 localhost。

func TestShowResultURLUsesLocalhostByDefault(t *testing.T) {
	t.Setenv("FAIRY_PUBLIC_BASE_URL", "")
	base := showResultPublicBase()
	if base != defaultShowResultBase {
		t.Fatalf("未配置公网基址时应回落到 %q，实际 %q", defaultShowResultBase, base)
	}
}

func TestShowResultURLHonoursPublicBase(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"公网 https", "https://files.example.com/filemanage", "https://files.example.com/filemanage"},
		{"公网 http", "http://example.test:9000", "http://example.test:9000"},
		{"结尾斜杠要剥掉", "https://files.example.com/filemanage/", "https://files.example.com/filemanage"},
		{"首尾空白要修掉", "  https://a.b/c  ", "https://a.b/c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAIRY_PUBLIC_BASE_URL", tc.input)
			if got := showResultPublicBase(); got != tc.want {
				t.Fatalf("基址归一化错误：期望 %q，实际 %q", tc.want, got)
			}
		})
	}
}

func TestShowResultTargetURLs(t *testing.T) {
	t.Setenv("FAIRY_PUBLIC_BASE_URL", "https://files.example.com/filemanage")
	base := showResultPublicBase()
	abs := "/path/to/Fairy/workspace/result/x.png"

	fileURL := showResultTargetURL(base, "index-fm.html", "file", abs)
	wantFile := "https://files.example.com/filemanage/index-fm.html?file=" + urlQueryEscape(abs)
	if fileURL != wantFile {
		t.Fatalf("文件预览地址错误：\n期望 %s\n实际 %s", wantFile, fileURL)
	}
	if contains(fileURL, "localhost") {
		t.Fatalf("公网模式下 URL 仍含 localhost: %s", fileURL)
	}

	pptURL := showResultTargetURL(base, "api/ppt-preview", "deck_dir", abs)
	if contains(pptURL, "localhost") {
		t.Fatalf("PPT 预览地址仍含 localhost: %s", pptURL)
	}
	if !contains(pptURL, "api/ppt-preview?deck_dir=") {
		t.Fatalf("PPT 预览地址形态不对: %s", pptURL)
	}
}

func TestShowResultTargetURLLocalDefault(t *testing.T) {
	t.Setenv("FAIRY_PUBLIC_BASE_URL", "")
	base := showResultPublicBase()
	got := showResultTargetURL(base, "index-fm.html", "file", "/tmp/a.png")
	want := "http://localhost:8081/index-fm.html?file=" + urlQueryEscape("/tmp/a.png")
	if got != want {
		t.Fatalf("本机默认地址被改坏了：\n期望 %s\n实际 %s", want, got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		})()
}
