package local

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// UploadPartFile 是未完成分片上传在磁盘上的暂存文件。
//
// filemanager 的分片上传把每一片直接写到**目标文件旁边**，
// 命名为 ".<原文件名>.<uuid>.part"，直到 commit 才改名落位。
// 所以「磁盘上存在 .part」等价于「有上传正在进行或已中断未清理」——
// 这是今天事故能被检测到的物理依据。
type UploadPartFile struct {
	Path    string
	Target  string // 去掉前缀 . 与后缀 .part 后的目标文件名
	Size    int64
	ModTime time.Time
}

// ActiveUploadScanner 用于在执行 kill / restart 这类
// 「可能中断主人正在进行的任务」的命令之前，检查磁盘上是否还有
// 未完成的上传暂存文件。
//
// 【为什么需要它】今天的事故本质不是「命令写错了」，
// 而是执行命令时**不知道自己会毁掉什么**：
// 模型看到的是「一个跑着的 server 进程」，看不到
// 「背后有一条 3.47GB 的上传正在进行，已传 116MB」。
// 拦命令是治标，知道代价才是治本 —— 所以在拦截的同一处
// 把代价算清楚并写进拒绝理由里。
type ActiveUploadScanner struct {
	// Roots 是要扫描的目录列表（主人的数据目录）。
	// 为空时使用 DefaultScanRoots()。
	Roots []string
	// MaxDepth 限制递归深度，避免在大目录上做无界扫描。
	// 0 表示使用默认值 DefaultMaxDepth。
	MaxDepth int
	// Now 便于测试注入固定时间。
	Now func() time.Time
	// RecentWindow 判定「活跃」的时长阈值。
	// 超过这个时长没动静的 .part 视为残留垃圾，不再拦截。
	RecentWindow time.Duration
	// ScanLimit 限制单次扫描返回的条目数，防止给模型灌一大屏。
	ScanLimit int
}

// DefaultMaxDepth 是扫描深度上限。分片暂存一般就在目标目录里，
// 浅层扫描足够，成本也低。
const DefaultMaxDepth = 3

// DefaultRecentWindow 取 30 分钟：一个仍在进行的上传，
// 分片之间通常只隔几秒到几十秒；超过 30 分钟没更新的
// 基本是崩溃留下的残留，不该继续拦着主人做别的事。
const DefaultRecentWindow = 30 * time.Minute

// DefaultScanLimit 限制提示里的条目数。
const DefaultScanLimit = 3

// DefaultScanRoots 返回默认扫描根。
func DefaultScanRoots() []string {
	if root := strings.TrimSpace(os.Getenv("FAIRY_DATA_DIR")); root != "" {
		return []string{root}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return []string{
			filepath.Join(home, ".local", "share", "Fairy"),
			filepath.Join(home, "workspace"),
		}
	}
	return nil
}

func (s ActiveUploadScanner) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s ActiveUploadScanner) maxDepth() int {
	if s.MaxDepth > 0 {
		return s.MaxDepth
	}
	return DefaultMaxDepth
}

func (s ActiveUploadScanner) window() time.Duration {
	if s.RecentWindow > 0 {
		return s.RecentWindow
	}
	return DefaultRecentWindow
}

func (s ActiveUploadScanner) limit() int {
	if s.ScanLimit > 0 {
		return s.ScanLimit
	}
	return DefaultScanLimit
}

func (s ActiveUploadScanner) roots() []string {
	if len(s.Roots) > 0 {
		return s.Roots
	}
	return DefaultScanRoots()
}

// uploadPartNamePattern 与 filemanager 的 sweepStaleInDir 保持同一套标准：
//
//	fm-upload.cjs:  /^\..+\.[0-9a-f-]{8,}\.part$/
//
// 【为什么不自己再定义一套】我最初按「32 位无连字符 hex」写，结果漏掉了磁盘上
// 真实存在的 `.img0003 (1).jpg.357bb1f3-2eab-42db-aac8-....part` ——
// 因为后端用的是 crypto.randomUUID()，产出的是**带连字符的 36 字符** UUID。
// 两套标准必然漂移，所以直接对齐后端正则。
var uploadPartNamePattern = regexp.MustCompile(`^\..+\.[0-9a-f-]{8,}\.part$`)

// LooksLikeUploadPartFile 判定一个文件名是否是分片上传的暂存文件。
func LooksLikeUploadPartFile(name string) bool {
	return uploadPartNamePattern.MatchString(name)
}

// UploadPartTarget 从暂存文件名还原出目标文件名。
func UploadPartTarget(name string) string {
	// 与 uploadPartNamePattern 同构：掐掉开头的 "." 和结尾的 ".part"，
	// body 形如 "<原文件名>.<uuid>"。uuid 里含连字符但不含点号，
	// 所以从**最后一个小数点**切开就是原文件名 —— 原文件名里的点不受影响。
	body := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".part")
	if cut := strings.LastIndex(body, "."); cut > 0 && cut < len(body)-1 {
		return body[:cut]
	}
	return body
}

// Scan 返回当前活跃（近期被写入）的分片暂存文件，按最近修改时间倒序。
func (s ActiveUploadScanner) Scan() []UploadPartFile {
	var found []UploadPartFile
	cutoff := s.now().Add(-s.window())
	seen := map[string]struct{}{}

	for _, root := range s.roots() {
		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				// 目录不可读就跳过，不让扫描本身变成阻塞点。
				if entry != nil && entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				if strings.Count(filepath.Clean(path), string(os.PathSeparator))-rootDepth >= s.maxDepth() {
					return filepath.SkipDir
				}
				return nil
			}
			if !LooksLikeUploadPartFile(entry.Name()) {
				return nil
			}
			info, statErr := entry.Info()
			if statErr != nil {
				return nil
			}
			// 超过时间窗 = 崩溃残留，不算活跃，不该继续拦主人。
			if info.ModTime().Before(cutoff) {
				return nil
			}
			if _, dup := seen[path]; dup {
				return nil
			}
			seen[path] = struct{}{}
			found = append(found, UploadPartFile{
				Path:    path,
				Target:  UploadPartTarget(entry.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
			})
			return nil
		})
	}

	sort.Slice(found, func(i, j int) bool {
		return found[i].ModTime.After(found[j].ModTime)
	})
	return found
}

// Describe 把扫描结果写成人话，直接放进 policy 的拒绝理由里。
// 重点是给出**具体代价**（哪个文件、多大、已传多久），
// 让模型在请求授权时能如实告知主人会损失什么。
func (s ActiveUploadScanner) Describe() string {
	parts := s.Scan()
	if len(parts) == 0 {
		return ""
	}
	limit := s.limit()
	var b strings.Builder
	verb := "is"
	if len(parts) > 1 {
		verb = "are"
	}
	fmt.Fprintf(&b, "there %s %d unfinished upload session%s on disk:",
		verb, len(parts), plural(len(parts)))
	for i, part := range parts {
		if i >= limit {
			fmt.Fprintf(&b, " (and %d more)", len(parts)-limit)
			break
		}
		fmt.Fprintf(&b, "\n  - %s (%s received so far, last written %s)",
			part.Target, humanBytes(part.Size), humanAge(s.now().Sub(part.ModTime)))
	}
	b.WriteString(
		"\nRestarting or killing the server will discard these partial uploads and the user must re-upload from scratch. " +
			"Tell the user what will be lost and get their explicit go-ahead before requesting " +
			"sandbox_permissions=danger-full-access.")
	return b.String()
}

// HasActiveUploads 是快速布尔判断，供调用方决定是否要做完整描述。
func (s ActiveUploadScanner) HasActiveUploads() bool {
	return len(s.Scan()) > 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%.1fh ago", d.Hours())
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
