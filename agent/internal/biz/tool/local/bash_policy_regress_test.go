package local

import (
	"fmt"
	"testing"
)

// TestEverydayCommandsStillAllowed 是整套 kill/restart 拦截的**误伤护栏**。
//
// 加黑名单最典型的翻车方式不是「拦得不够」，而是「拦多了」：
// 把 ps / git / ls 这些日常命令误伤，模型连诊断问题的能力都没了。
// 所以这里逐条钉死：日常命令必须放行，
// 而危险命令即使藏在管道/复合命令里也必须仍然被拦。
func TestEverydayCommandsStillAllowed(t *testing.T) {
	p := DefaultBashPolicy()
	p.ActiveUploadScanner = &ActiveUploadScanner{Roots: []string{"/tmp"}}

	// 日常开发命令清单：加了 kill/restart 拦截后，一条都不能误伤
	everyday := []string{
		"ls -la",
		"ls /tmp",
		"ps -ef",
		"ps aux | grep node",
		"pgrep -af server.cjs",
		"curl -s http://127.0.0.1:8081/api/usage",
		"git status",
		"git log --oneline -5",
		"git commit -m 'x'",
		"go build ./...",
		"npm run dev",
		"grep -rn pattern .",
		"tail -100 log.txt",
		"head -20 file.txt",
		"find . -name '*.go'",
		"du -sh .",
		"df -h",
		"echo hello",
		"mkdir -p /tmp/x",
		"chmod 755 /tmp/x",
		"which node",
		"node --version",
		"python3 -c 'print(1)'",
		"sleep 1",
		"kill",    // 裸 kill 无参数
		"kill -l", // 列出信号
		"kill --help",
		"echo 'kill -9 123'",
		"grep kill README.md",
	}
	for _, cmd := range everyday {
		d := p.Check(cmd)
		if !d.Allowed {
			t.Errorf("日常命令被误伤: %q -> %s", cmd, d.Reason)
		}
	}
	// 反向验证：危险命令即使藏在复合命令里也必须被拦。
	for _, cmd := range []string{
		"ls | pkill",
		"echo x && pkill -f node",
		"cat foo.txt; kill -9 123",
		"true; restart fairy",
	} {
		d := p.Check(cmd)
		if d.Allowed {
			t.Errorf("复合命令中的危险操作未被拦截: %q", cmd)
		}
	}

	// /etc 属于既有 DenyPaths，不是本次改动引入的行为，单独确认它仍然生效即可。
	if d := p.Check("cat /etc/hosts"); d.Allowed {
		t.Errorf("保护目录 /etc 应仍然被拦")
	}

	fmt.Println("日常命令全部放行 OK，复合命令中的危险操作仍被拦 OK")
}
