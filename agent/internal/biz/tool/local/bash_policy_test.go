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
