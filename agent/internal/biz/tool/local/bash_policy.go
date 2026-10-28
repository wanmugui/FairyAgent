package local

import (
	"fmt"
	"strings"
)

// BashPolicy is the policy layer applied to every shell tool invocation. It
// tokenizes compound command lines and checks each executable, including common
// nested shell forms, against a command whitelist/blacklist and protected path
// prefixes. Models can request an escalation by passing
// `sandbox_permissions: danger-full-access` together with a justification; the
// host approval layer decides whether to accept it.
type BashPolicy struct {
	// AllowCommands is the whitelist. When non-empty, every executable in a
	// compound command line must match one of these names (case-insensitive).
	// An empty list disables the whitelist; every command passes this check.
	AllowCommands []string
	// DenyCommands is the blacklist. Each entry is matched against executable
	// names in the top-level command and common nested shell invocations.
	DenyCommands []string
	// DenyPaths is a list of absolute path prefixes (Windows: `C:\Windows`
	// or POSIX: `/etc`). If the command line contains any of these as
	// arguments, the command is denied. Use the absolute form; matching is
	// case-insensitive on Windows.
	DenyPaths []string
	// ActiveUploadScanner 是可选的，为 nil 时使用默认扫描器（DefaultScanRoots）。
	// 仅在终止进程 / 重启服务这类命令被拒时才调用 —— 那条路径本来就要人拍板，
	// 多一次目录扫描无妨；ps/ls/curl 这类高频只读命令完全不碰它，没有性能代价。
	ActiveUploadScanner *ActiveUploadScanner
}

// DefaultBashPolicy returns a conservative policy: dangerous commands blocked,
// well-known paths protected, no whitelist (so common dev tools work).
func DefaultBashPolicy() BashPolicy {
	return BashPolicy{
		AllowCommands: nil, // open by default; uncomment to enforce whitelist
		DenyCommands: []string{
			// Format / disk wipe
			"format", "format-volume", "clear-disk", "initialize-disk", "diskpart", "fdisk", "mkfs", "dd",
			// Privilege escalation
			"sudo", "su", "runas",
			// System-level shutdown
			"shutdown", "stop-computer", "reboot", "restart-computer", "halt", "poweroff", "init",
			// Network-config destroy
			"netsh", "iptables",

			// ---- 进程终止 ----
			//
			// 真实事故：模型为重启自己的测试实例执行了
			// `pkill -f "server.cjs 8100"`，而 -f 是**模式匹配**，
			// 命中了主人正在使用的 8081 服务，3.5GB 上传被硬生生打断，
			// 116MB 已传数据随进程一起消失。
			//
			// 这类命令的危险不在权限 —— 它们不碰保护路径、不需要提权，
			// 审核机制按设计就会放行。危险在于**影响面不可预知**：
			// 语义上无法只杀一个进程。故一律要求显式授权。
			//
			// kill 不在此黑名单：它能精确指定单个 PID，
			// 由 killInvocation 单独判定（裸 kill 无参数、
			// 文本中提及 kill 均放行，避免误伤只读排查）。
			"pkill", "killall", "kill5", "taskkill",

			// ---- 服务重启 ----
			//
			// 与 kill 同类危害：主人正在使用的服务被重启，
			// 进行中的上传/任务同样会断。
			"restart", "systemctl", "service", "supervisorctl",
		},
		DenyPaths: []string{
			// Windows system directories
			`C:\Windows`,
			`C:\Windows\System32`,
			`C:\Program Files`,
			`C:\Program Files (x86)`,
			// Boot / system
			`/etc`,
			`/boot`,
			`/usr/bin`,
			`/usr/sbin`,
			`/sbin`,
		},
	}
}

// Check inspects a command line and returns a PolicyDecision. Structured
// escalation is handled by the tool after this policy check and records that
// an escalation happened.
func (p BashPolicy) Check(command string) PolicyDecision {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return PolicyDecision{Allowed: false, Reason: "empty command"}
	}

	invocations := shellInvocations(tokenizeShellCommand(cmd))
	if len(invocations) == 0 {
		return PolicyDecision{Allowed: false, Reason: "command does not contain an executable"}
	}

	if len(p.AllowCommands) > 0 {
		for _, invocation := range invocations {
			if !matchesCommandList(invocation.Name, p.AllowCommands) {
				return PolicyDecision{
					Allowed: false,
					Reason:  fmt.Sprintf("command %q is not in the whitelist (allowed: %s)", invocation.Name, strings.Join(p.AllowCommands, ", ")),
				}
			}
		}
	}

	for _, invocation := range invocations {
		if decision := p.denyInvocation(invocation, 0); decision != nil {
			return *decision
		}
	}

	return PolicyDecision{Allowed: true}
}

type shellInvocation struct {
	Name   string
	Tokens []string
}

func (p BashPolicy) denyInvocation(invocation shellInvocation, depth int) *PolicyDecision {
	if depth > 4 {
		return &PolicyDecision{
			Allowed:     false,
			Reason:      "nested shell command depth exceeds the policy limit",
			MatchedDeny: invocation.Name,
		}
	}
	if matchesCommandList(invocation.Name, p.DenyCommands) {
		// 进程终止 / 服务重启类命令，光说「在黑名单里」是不够的 ——
		// 今天的事故根因正是执行时不知道会毁掉什么。
		// 这类命令的拒绝理由必须点明它可能中断主人的上传/任务，
		// 逼模型在请求授权前先查清影响面、再如实告知主人。
		if consequence := destructiveConsequence(invocation.Name); consequence != "" {
			// 再往前看一步：把磁盘上真实存在的未完成上传点出来。
			// 「可能中断」是泛泛警告，「有一条 3.47GB 的上传正在进行」
			// 才是模型能拿去如实告诉主人的具体代价。
			if detail := p.activeUploadDetail(); detail != "" {
				consequence += " Concretely: " + detail
			}
			return &PolicyDecision{
				Allowed:     false,
				Reason:      consequence + " Check the exact target with ps/pgrep first, tell the user what will be lost, then request sandbox_permissions=danger-full-access with a justification.",
				MatchedDeny: invocation.Name,
			}
		}
		return &PolicyDecision{
			Allowed:     false,
			Reason:      fmt.Sprintf("command %q is in the deny list; request sandbox_permissions=danger-full-access with a justification to override", invocation.Name),
			MatchedDeny: invocation.Name,
		}
	}
	if decision := p.killDenial(invocation); decision != nil {
		return decision
	}
	if decision := p.pathDenial(invocation.Tokens); decision != nil {
		return decision
	}
	nested := nestedShellCommand(invocation)
	if strings.TrimSpace(nested) == "" {
		return nil
	}
	for _, nestedInvocation := range shellInvocations(tokenizeShellCommand(nested)) {
		if decision := p.denyInvocation(nestedInvocation, depth+1); decision != nil {
			decision.Reason = fmt.Sprintf("nested command %q: %s", nestedInvocation.Name, decision.Reason)
			return decision
		}
	}
	return nil
}

// activeUploadDetail 扫描磁盘上未完成的上传，返回可读的描述；没有则返回空串。
//
// 扫描失败一律当作「没有」—— 诊断信息不能反过来把命令拦死。
// 真正的拦截理由由 destructiveConsequence 负责，这里只负责补充细节。
func (p BashPolicy) activeUploadDetail() string {
	scanner := ActiveUploadScanner{}
	if p.ActiveUploadScanner != nil {
		scanner = *p.ActiveUploadScanner
	}
	defer func() { _ = recover() }() // 扫描器出错绝不能影响放行判断
	return scanner.Describe()
}

// destructiveConsequence 说明「终止进程 / 重启服务」会毁掉什么。
// 只对这两类命令返回文案，其余黑名单命令（rm、dd、sudo 等）
// 走通用理由即可 —— 它们的破坏性大家都知道，不必啰嗦。
func destructiveConsequence(name string) string {
	switch normalizeCommandName(name) {
	case "pkill", "killall", "kill5", "taskkill", "kill":
		return fmt.Sprintf(
			"%q terminates processes by pattern match — it can kill the user's own active work "+
				"(an in-flight upload, a running job) without meaning to, and that work is lost irreversibly.",
			name)
	case "restart", "systemctl", "service", "supervisorctl":
		return fmt.Sprintf(
			"%q restarts a service — any of the user's active work being served by it "+
				"(in-flight uploads, running jobs) is lost irreversibly.",
			name)
	}
	return ""
}

// killDenial 单独处理 kill，因为它和 pkill/killall 的性质不同：
// kill 能精确指定单个 PID，所以不放进黑名单一刀切禁掉；
// 但「终止一个进程」这件事本身仍需主人点头 —— 那个进程可能是
// 正在服务主人请求的服务，也可能是 Fairy 自己。
//
// 放行的三种情况（避免误伤只读排查）：
//   - 裸 `kill` 无参数：只是打印用法，什么也没干
//   - `kill -l`：列出信号名
//   - 文本里提到 kill（如 echo "kill -9 123"）：不是可执行调用
func (p BashPolicy) killDenial(invocation shellInvocation) *PolicyDecision {
	if normalizeCommandName(invocation.Name) != "kill" {
		return nil
	}
	var signals []string
	var targets []string
	for _, token := range invocation.Tokens {
		trimmed := strings.TrimSpace(token)
		switch {
		case trimmed == "" || trimmed == "&":
		case strings.HasPrefix(trimmed, "-"):
			signals = append(signals, strings.ToLower(trimmed))
		default:
			targets = append(targets, trimmed)
		}
	}
	// 没有目标就不是终止行为，放行。
	if len(targets) == 0 {
		return nil
	}
	detail := strings.Join(signals, " ")
	if detail != "" {
		detail += " "
	}
	reason := fmt.Sprintf(
		"kill %s%s would terminate a running process — that process may be serving the user's "+
			"active work (uploads, jobs) and killing it loses that work irreversibly.",
		detail, strings.Join(targets, " "))
	if active := p.activeUploadDetail(); active != "" {
		reason += " Concretely: " + active
	}
	return &PolicyDecision{
		Allowed: false,
		Reason: reason + " Verify the exact PID with ps/pgrep first, tell the user what will be lost, " +
			"then request sandbox_permissions=danger-full-access with a justification.",
		MatchedDeny: "kill",
	}
}

func (p BashPolicy) pathDenial(tokens []string) *PolicyDecision {
	for _, path := range extractPathLikeTokens(tokens) {
		normalized := strings.ToLower(strings.ReplaceAll(path, `\`, `/`))
		normalized = strings.TrimRight(normalized, "/")
		for _, deny := range p.DenyPaths {
			denyNorm := strings.ToLower(strings.ReplaceAll(strings.TrimRight(deny, `\/`), `\`, `/`))
			if normalized == denyNorm || strings.HasPrefix(normalized, denyNorm+"/") {
				return &PolicyDecision{
					Allowed:     false,
					Reason:      fmt.Sprintf("path %q is under a protected directory (%s); request sandbox_permissions=danger-full-access with a justification to override", path, deny),
					MatchedDeny: path,
				}
			}
		}
	}
	return nil
}

// PolicyDecision is the result of a BashPolicy check.
type PolicyDecision struct {
	Allowed     bool
	Reason      string
	MatchedDeny string
	Escalated   bool
}

// WithEscalation marks an allow decision as having skipped deny rules.
func (d PolicyDecision) WithEscalation() PolicyDecision {
	d.Escalated = true
	return d
}

func shellInvocations(tokens []string) []shellInvocation {
	segments := splitShellSegments(tokens)
	invocations := make([]shellInvocation, 0, len(segments))
	for _, segment := range segments {
		name, args := commandFromSegment(segment)
		if name == "" {
			continue
		}
		invocations = append(invocations, shellInvocation{Name: name, Tokens: args})
	}
	return invocations
}

func splitShellSegments(tokens []string) [][]string {
	var segments [][]string
	current := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if isShellCommandSeparator(token) {
			if len(current) > 0 {
				segments = append(segments, current)
				current = nil
			}
			continue
		}
		current = append(current, token)
	}
	if len(current) > 0 {
		segments = append(segments, current)
	}
	return segments
}

func isShellCommandSeparator(token string) bool {
	switch token {
	case ";", "&&", "||", "|", "&", "\n":
		return true
	default:
		return false
	}
}

func commandFromSegment(segment []string) (string, []string) {
	for index, token := range segment {
		if token == "" || token == "&" {
			continue
		}
		if isEnvironmentAssignment(token) {
			continue
		}
		return normalizeCommandName(token), append([]string(nil), segment[index+1:]...)
	}
	return "", nil
}

func isEnvironmentAssignment(token string) bool {
	equal := strings.IndexByte(token, '=')
	if equal <= 0 {
		return false
	}
	return isEnvAssignmentName(token[:equal])
}

func normalizeCommandName(token string) string {
	name := strings.Trim(strings.TrimSpace(token), `"'`+"`")
	name = strings.ReplaceAll(name, "/", `\`)
	if index := strings.LastIndexByte(name, '\\'); index >= 0 {
		name = name[index+1:]
	}
	name = strings.ToLower(name)
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".com", ".ps1"} {
		if strings.HasSuffix(name, suffix) {
			name = strings.TrimSuffix(name, suffix)
			break
		}
	}
	return name
}

func matchesCommandList(command string, values []string) bool {
	normalized := normalizeCommandName(command)
	for _, value := range values {
		if normalized == normalizeCommandName(value) {
			return true
		}
	}
	return false
}

func nestedShellCommand(invocation shellInvocation) string {
	if len(invocation.Tokens) == 0 {
		return ""
	}
	switch normalizeCommandName(invocation.Name) {
	case "bash", "sh", "zsh", "dash":
		if argument, ok := optionArgument(invocation.Tokens, "-c"); ok {
			return argument
		}
	case "pwsh", "powershell":
		for _, option := range []string{"-command", "-c"} {
			if argument, ok := optionArgument(invocation.Tokens, option); ok {
				return argument
			}
		}
	case "cmd":
		for _, option := range []string{"/c", "/k"} {
			if argument, ok := optionArgument(invocation.Tokens, option); ok {
				return argument
			}
		}
	}
	return ""
}

func optionArgument(tokens []string, option string) (string, bool) {
	for index, token := range tokens {
		if !strings.EqualFold(strings.TrimSpace(token), option) {
			continue
		}
		if index+1 >= len(tokens) {
			return "", false
		}
		return strings.Join(tokens[index+1:], " "), true
	}
	return "", false
}

func isEnvAssignmentName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
				return false
			}
			continue
		}
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// extractPathLikeTokens pulls absolute-looking paths from already-tokenized
// command text, including values such as `--config=/etc/app.conf`.
func extractPathLikeTokens(tokens []string) []string {
	var out []string
	for _, raw := range tokens {
		candidate := strings.Trim(strings.TrimSpace(raw), `"'`+"`")
		if equal := strings.IndexByte(candidate, '='); equal >= 0 {
			candidate = candidate[equal+1:]
		}
		candidate = strings.Trim(candidate, `"'`+"`()[]{}")
		candidate = strings.TrimRight(candidate, ",;:")
		if strings.HasPrefix(candidate, "/") || isWindowsAbsolute(candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func tokenizeShellCommand(command string) []string {
	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, current.String())
		current.Reset()
	}

	runes := []rune(command)
	var quote rune
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			if r == '\\' && quote == '"' && index+1 < len(runes) && runes[index+1] == quote {
				current.WriteRune(quote)
				index++
				continue
			}
			current.WriteRune(r)
			continue
		}

		switch r {
		case '\'', '"', '`':
			quote = r
		case ' ', '\t', '\r':
			flush()
		case '\n':
			flush()
			tokens = append(tokens, "\n")
		case ';', '|', '&':
			if r == '&' && index > 0 && runes[index-1] == '>' {
				current.WriteRune(r)
				continue
			}
			flush()
			operator := string(r)
			if (r == '&' || r == '|') && index+1 < len(runes) && runes[index+1] == r {
				operator += string(r)
				index++
			}
			tokens = append(tokens, operator)
		case '>', '<':
			flush()
			operator := string(r)
			if index+1 < len(runes) && runes[index+1] == r {
				operator += string(r)
				index++
			}
			tokens = append(tokens, operator)
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func isWindowsAbsolute(p string) bool {
	if len(p) < 3 {
		return false
	}
	if !((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		return false
	}
	if p[1] != ':' {
		return false
	}
	if p[2] != '\\' && p[2] != '/' {
		return false
	}
	return true
}
