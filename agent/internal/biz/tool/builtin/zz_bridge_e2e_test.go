package builtin

// 跨语言联调：Go 后端真的在等，Python 桥在同一份 ask_results.json 上作答。
//
// 这条测试是为了钉死一个反复出现的失败模式：纯函数测试两边都绿，但没人
// 验证过「桥写进去的那个形状，后端到底认不认」。后端只等 10 秒就自己按
// recommended 落答案，所以如果桥写的格式不对，这里看到的会���
// auto_selected=true —— 现象是「主人明明回了，模型却当没回」。
//
// 它依赖 python3 与 skills/wechat-bot，任一缺失就 skip，所以留在仓库里
// 是安全的（纯 Go 环境不会因此变红）。
//
// 留着是有理由的：ask_user 这条链路上，Go 与 Python 两侧的测试都绿却对不上的
// 情况真的发生过。测试名故意写成 zz_ 前缀 —— 它不属于 ask_user 本身的语义，
// 而是一条跨语言的接线守卫。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const bridgeQuestions = `[{"id":"mode","question":"Choose","options":[` +
	`{"label":"first","description":"d1"},` +
	`{"label":"second","recommended":true,"description":"d2"}]},` +
	`{"id":"when","question":"When","options":[{"label":"today"},{"label":"tomorrow","description":"later"}]}]`

func TestBridgeAnswersBackendAcrossLanguages(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-language check")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 not available: %v", err)
	}
	repoRoot := os.Getenv("FAIRY_REPO_ROOT")
	if repoRoot == "" {
		repoRoot = ".."
	}
	bridgeDir, err := filepath.Abs(filepath.Join(repoRoot, "skills", "wechat-bot"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bridgeDir, "wechatbot", "ask_bridge.py")); err != nil {
		t.Skipf("wechat bridge not present: %v", err)
	}

	workspace := t.TempDir()

	// 让 Python 桥在这个 workspace 上把题答掉。
	script := fmt.Sprintf(`
import sys, time
sys.path.insert(0, %q)
from wechatbot import ask_bridge as ab
ab.RESULTS_PATH = __import__("pathlib").Path(%q) / "ask_user" / "ask_results.json"
seen = 0
deadline = time.time() + 8
while time.time() < deadline:
    found = ab.pending_index(seen)
    if found:
        idx, entry = found
        seen = ab.current_count()
        print("BRIDGE_ASK:", ab.format_questions(entry, ab.ASK_AUTO_SELECT_SECONDS).replace("\n", " | "))
        ok = ab.submit_reply(idx, "1B 2A")
        print("BRIDGE_SUBMIT_OK:", ok)
        break
    time.sleep(0.1)
else:
    print("BRIDGE_NEVER_SAW_THE_ASK")
`, bridgeDir, workspace)
	pyFile := filepath.Join(t.TempDir(), "bridge_answers.py")
	body := "workspace = " + "\"" + workspace + "\"\n"
	if err := os.WriteFile(pyFile, []byte(body+script), 0o600); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmd := exec.Command(python, pyFile)
		cmd.Dir = bridgeDir
		out, _ := cmd.CombinedOutput()
		t.Logf("bridge stdout:\n%s", out)
	}()

	// 给 Python 一点时间发现新条目，但远小于 10 秒时限。
	time.Sleep(500 * time.Millisecond)
	result := executeLocalAskUser(t, workspace, askArgsWithTimeout(bridgeQuestions, 10))
	wg.Wait()

	if result.IsError {
		t.Fatalf("unexpected error: %#v", result.Value)
	}
	if result.Value[askUserAutoSelectedParam] == true {
		t.Fatal("backend auto-selected: the bridge's answer was NOT recognised")
	}
	if result.WaitingReply {
		t.Fatal("backend must resolve, not hand back an open wait")
	}
	answer, ok := result.Value["answer"].(map[string]any)
	if !ok {
		t.Fatalf("answer must be a map, got %T", result.Value["answer"])
	}
	// 桥回的是 "1B 2A"：mode=second（description d2）、when=today
	mode, ok := answer["mode"].([]any)
	if !ok || len(mode) != 1 || mode[0] != "second" {
		t.Fatalf("mode must be the [label] list the bridge wrote, got %#v", answer["mode"])
	}
	if answer["mode_desc"] != "d2" {
		t.Fatalf("mode_desc must travel with the label, got %#v", answer["mode_desc"])
	}
	when, ok := answer["when"].([]any)
	if !ok || len(when) != 1 || when[0] != "today" {
		t.Fatalf("when must be the [label] list the bridge wrote, got %#v", answer["when"])
	}
	t.Logf("backend accepted the bridge's answer verbatim: %#v", answer)
}
