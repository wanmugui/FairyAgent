package local

import (
	"strings"
	"testing"
)

func TestBashPolicyBlocksDeniedCommands(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		"sudo apt install foo",
		"format c:",
		"shutdown -h now",
		"reboot",
	} {
		d := p.Check(cmd)
		if d.Allowed {
			t.Errorf("expected %q to be blocked, got allowed", cmd)
		}
		if !strings.Contains(strings.ToLower(d.Reason), "deny") {
			t.Errorf("expected deny reason for %q, got %q", cmd, d.Reason)
		}
	}
}

func TestBashPolicyBlocksDeniedPaths(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		"rm -rf C:\\Windows\\System32\\drivers\\etc",
		"cat /etc/passwd",
		"ls /boot/grub",
		"echo C:/Windows\\foo",
	} {
		d := p.Check(cmd)
		if d.Allowed {
			t.Errorf("expected %q to be blocked, got allowed", cmd)
		}
	}
}

func TestBashPolicyAllowsNormalCommands(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		"ls -la",
		"git status",
		"python -m pytest",
		"node server.js",
		"echo hello world",
		"FOO=bar python script.py", // env-assigned command
	} {
		d := p.Check(cmd)
		if !d.Allowed {
			t.Errorf("expected %q to be allowed, got blocked: %s", cmd, d.Reason)
		}
	}
}

func TestBashPolicyWhitelistEnforced(t *testing.T) {
	p := BashPolicy{AllowCommands: []string{"ls", "cat"}}
	for _, cmd := range []string{"ls -la", "cat foo.txt"} {
		d := p.Check(cmd)
		if !d.Allowed {
			t.Errorf("expected %q allowed under whitelist, got blocked: %s", cmd, d.Reason)
		}
	}
	for _, cmd := range []string{"rm foo", "echo hi"} {
		d := p.Check(cmd)
		if d.Allowed {
			t.Errorf("expected %q blocked under whitelist, got allowed", cmd)
		}
	}
}

func TestBashPolicyEmptyCommandIsBlocked(t *testing.T) {
	p := DefaultBashPolicy()
	if d := p.Check("   "); d.Allowed {
		t.Fatalf("expected empty command to be blocked")
	}
}

func TestBashPolicyDenyTakesPriorityOverAllow(t *testing.T) {
	p := BashPolicy{
		AllowCommands: []string{"sudo"},
		DenyCommands:  []string{"sudo"},
	}
	d := p.Check("sudo apt install foo")
	if d.Allowed {
		t.Fatalf("deny should beat allow")
	}
}

func TestBashPolicyChecksEveryCommandInCompoundLines(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		"echo ok && shutdown now",
		"echo ok; sudo rm -rf /tmp/x",
		"Get-ChildItem | Format-Volume",
		"cmd /c format C:",
		"bash -c \"shutdown now\"",
		"pwsh -Command \"Stop-Computer -Force\"",
	} {
		if decision := p.Check(cmd); decision.Allowed {
			t.Errorf("expected compound command %q to be blocked", cmd)
		}
	}
}

func TestBashPolicyBlocksExecutablePathsAndQuotedPaths(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		`C:\Windows\System32\runas.exe /user:admin cmd`,
		`cmd /c "C:\Windows\System32\format.exe C:"`,
		`tool --config="C:\Windows\System32\config\SAM"`,
		`bash -c 'cat "/etc/passwd"'`,
	} {
		if decision := p.Check(cmd); decision.Allowed {
			t.Errorf("expected executable/path command %q to be blocked", cmd)
		}
	}
}

func TestBashPolicyDoesNotFlagQuotedCommandText(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		`echo "shutdown now"`,
		`Write-Output 'sudo is just text'`,
		`python -c "print('ok')" 2>&1`,
	} {
		if decision := p.Check(cmd); !decision.Allowed {
			t.Errorf("expected %q to be allowed, got blocked: %s", cmd, decision.Reason)
		}
	}
}

func TestBashPolicyWhitelistAppliesToPipelines(t *testing.T) {
	p := BashPolicy{AllowCommands: []string{"ls", "cat"}}
	if decision := p.Check("ls -la | rm foo"); decision.Allowed {
		t.Fatal("expected non-whitelisted pipeline command to be blocked")
	}
	if decision := p.Check("ls -la | cat"); !decision.Allowed {
		t.Fatalf("expected whitelisted pipeline to pass: %s", decision.Reason)
	}
}

// TestBashPolicyBlocksProcessKilling 复现真实事故：
// 模型执行 `pkill -f "server.cjs 8100"` 想重启自己的测试实例，
// 结果模式匹配命中了用户正在使用的 8081 服务，用户的 3.5GB 上传被中断。
//
// 根因：pkill/killall 天生是**模式匹配**，语义上就无法只杀一个进程。
// 旧的黑名单里没有它们，所以这条命令畅通无阻 —— 它不碰保护路径、
// 不需要提权，审核机制按设计放行。
//
// 这类命令的危险不在权限，而在**影响面不可预知**。必须显式要求授权。
func TestBashPolicyBlocksProcessKilling(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		`pkill -f "server.cjs 8100"`, // 真实事故命令
		"pkill -f node",
		"pkill node",
		"killall node",
		"killall -9 fairy",
		"kill -9 740122",
		"pkill -9 -u alice",
	} {
		d := p.Check(cmd)
		if d.Allowed {
			t.Errorf("危险 kill 命令 %q 未被拦截 —— 这会导致误杀其他进程", cmd)
		}
	}
}

// TestBashPolicyBlocksServiceRestart 服务重启与 kill 同类危害：
// 主人正在使用的服务被重启，同样会中断进行中的任务。
func TestBashPolicyBlocksServiceRestart(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		"restart fairy",
		"systemctl restart fairy",
		"service fairy restart",
		"supervisorctl restart fairy",
	} {
		d := p.Check(cmd)
		if d.Allowed {
			t.Errorf("服务重启命令 %q 未被拦截", cmd)
		}
	}
}

// TestBashPolicyStillAllowsHarmlessInspection 守住误伤边界：
// 加了 kill 拦截后，「看进程」这类只读排查必须照常放行，
// 否则模型就没法诊断问题本身了。
func TestBashPolicyStillAllowsHarmlessInspection(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{
		"ps -ef",
		"ps aux | grep server.cjs",
		"pgrep -af server.cjs",
		"lsof -i :8081",
		"kill",             // 裸 kill 无参数
		"echo kill -9 123", // 仅提及，不执行
	} {
		d := p.Check(cmd)
		if !d.Allowed {
			t.Errorf("只读排查命令 %q 被误拦：%s", cmd, d.Reason)
		}
	}
}

// TestBashPolicyDangerousKillIsEscalatable 守住「拦截不是死路」这条底线。
//
// 加了 kill/pkill 黑名单后，最容易犯的后续错误是把它改成永久拒绝 —
// 那样模型永远无法重启服务，功能被焊死。
//
// 现状（bash.go）：
//
//	if !decision.Allowed && canEscalate {
//		decision = decision.WithEscalation()
//		decision.Allowed = true
//	}
//
// 所以主人点同意后，命令必须能真的执行。
//
// 这个用例锁住的是**意图**：拦截的语义是「先问主人」，
// 不是「不许做」。谁把 WithEscalation 这条路径改坏，这里立刻红。
func TestBashPolicyDangerousKillIsEscalatable(t *testing.T) {
	p := DefaultBashPolicy()
	d := p.Check(`pkill -f "server.cjs 8100"`)
	if d.Allowed {
		t.Fatal("危险命令必须先被拦下，等待主人决定")
	}
	// 拒绝理由必须明确告诉模型「怎么才能继续」，
	// 而不是只说「不行」—— 否则模型会卡住或换招绕路。
	if d.Reason == "" || !strings.Contains(d.Reason, "danger-full-access") {
		t.Errorf("拒绝理由应指向升级授权路径，实际=%q", d.Reason)
	}
	if !strings.Contains(d.Reason, "justification") {
		t.Errorf("拒绝理由应要求给出 justification，实际=%q", d.Reason)
	}
}

// TestBashPolicyKillDenialExplainsConsequence 拒绝理由必须写清代价。
//
// 今天事故的根因不是「命令写错」，而是执行时不知道会毁掉什么。
// 所以理由里必须点名「可能中断主人的上传/任务」，而不是泛泛而谈。
func TestBashPolicyKillDenialExplainsConsequence(t *testing.T) {
	p := DefaultBashPolicy()
	for _, cmd := range []string{`pkill -f node`, "kill -9 740122", "restart fairy"} {
		d := p.Check(cmd)
		if d.Allowed {
			t.Fatalf("%q 应被拦截", cmd)
		}
		if !strings.Contains(d.Reason, "active work") && !strings.Contains(d.Reason, "user") {
			t.Errorf("%q 的拒绝理由未说明对主人的影响：%q", cmd, d.Reason)
		}
	}
}
