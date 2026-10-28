package main

import (
	"strings"
	"testing"
)

// 运行态事实必须「有就说、没有就明说」，不能靠猜——今晚三次栽在不知道
// 服务重启没有、改动上线没有、代理接没有。

func TestRuntimeFactsBlockAlwaysReportsServerStart(t *testing.T) {
	t.Setenv("FAIRY_SERVER_STARTED_AT", "2026-10-16 19:42:33")
	block := runtimeFactsBlock(nil)
	if !strings.Contains(block, "运行态事实") {
		t.Fatal("缺少运行态事实块")
	}
	if !strings.Contains(block, "2026-10-16 19:42:33") {
		t.Fatalf("未回显 server 启动时间: %s", block)
	}
	if !strings.Contains(block, "本 agent 进程启动于") {
		t.Fatalf("缺少 agent 进程启动时间: %s", block)
	}
}

func TestRuntimeFactsSaysUnknownRatherThanGuess(t *testing.T) {
	t.Setenv("FAIRY_SERVER_STARTED_AT", "")
	block := runtimeFactsBlock(nil)
	if !strings.Contains(block, "未知") {
		t.Fatalf("拿不到 server 启动时间时必须写「未知」，不能留空让人猜: %s", block)
	}
}

func TestRuntimeFactsWarnsWhenSourceNewerThanBinary(t *testing.T) {
	// 这条专门防「以为改了其实没编译上线」。用直接构造的缓存绕过真实文件系统。
	factsCache.mu.Lock()
	factsCache.hasValue = true
	factsCache.fetched = timeNowForTest()
	factsCache.srcTime = futureForTest()
	factsCache.gitHead = "deadbee"
	factsCache.mu.Unlock()
	defer func() {
		factsCache.mu.Lock()
		factsCache.hasValue = false
		factsCache.mu.Unlock()
	}()
	block := runtimeFactsBlock(nil)
	if !strings.Contains(block, "尚未编译上线") {
		t.Fatalf("源码晚于二进制时必须明确警告: %s", block)
	}
	if !strings.Contains(block, "deadbee") {
		t.Fatalf("应回显 git HEAD: %s", block)
	}
}

func TestProxyPresenceIsReported(t *testing.T) {
	t.Setenv("https_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTP_PROXY", "")
	if !strings.Contains(runtimeFactsBlock(nil), "未注入") {
		t.Fatal("无代理时必须写明未注入")
	}
	t.Setenv("https_proxy", "http://127.0.0.1:7897")
	if !strings.Contains(runtimeFactsBlock(nil), "127.0.0.1:7897") {
		t.Fatal("有代理时必须回显地址")
	}
}
