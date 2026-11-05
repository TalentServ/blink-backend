package codex

import (
	"fmt"
	"strings"
)

// DiffLine is one rendered line in a hunk.
type DiffLine struct {
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	OldNo int    `json:"oldNo,omitempty"`
	NewNo int    `json:"newNo,omitempty"`
}

// DiffHunk is a contiguous change with a little surrounding context.
type DiffHunk struct {
	Index    int        `json:"index"`
	Header   string     `json:"header"`
	OldStart int        `json:"oldStart"`
	OldCount int        `json:"oldCount"`
	NewStart int        `json:"newStart"`
	NewCount int        `json:"newCount"`
	OldLines []string   `json:"-"`
	NewLines []string   `json:"-"`
	Lines    []DiffLine `json:"lines"`
}

// FileDiff is one path in a scope.
type FileDiff struct {
	Path      string     `json:"path"`
	Status    string     `json:"status"`
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	Hunks     []DiffHunk `json:"hunks"`
}

// LastTurnDiff compares this turn's files with the previous turn, falling back to base.
func LastTurnDiff(base, prev, current map[string]string) []FileDiff {
	before := map[string]string{}
	if current == nil {
		current = map[string]string{}
	}
	for p, content := range current {
		if prev != nil {
			if old, ok := prev[p]; ok {
				before[p] = old
				continue
			}
		}
		if old, ok := base[p]; ok {
			before[p] = old
		}
		_ = content
	}
	return DiffMaps(before, current)
}

// ParseUnifiedPatch turns a GitHub compare patch into structured hunks.
func hunkStarts(header string) (oldStart, newStart int) {
	oldStart, newStart = 1, 1
	for _, part := range strings.Fields(header) {
		if strings.HasPrefix(part, "-") {
			oldStart = leadingInt(part[1:])
		} else if strings.HasPrefix(part, "+") {
			newStart = leadingInt(part[1:])
		}
	}
	return oldStart, newStart
}

func leadingInt(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func ParseUnifiedPatch(path, status, patch string, additions, deletions int) FileDiff {
	lines := splitKeep(patch)
	hunks := []DiffHunk{}
	var cur *DiffHunk
	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			if cur != nil {
				hunks = append(hunks, *cur)
			}
			oldStart, newStart := hunkStarts(line)
			h := DiffHunk{Index: len(hunks), Header: line, OldStart: oldStart, NewStart: newStart}
			cur = &h
			continue
		}
		if cur == nil || line == "" && len(cur.Lines) == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			text := strings.TrimPrefix(line, "+")
			cur.NewLines = append(cur.NewLines, text)
			cur.Lines = append(cur.Lines, DiffLine{Kind: "add", Text: text, NewNo: cur.NewStart + cur.NewCount})
			cur.NewCount++
		case strings.HasPrefix(line, "-"):
			text := strings.TrimPrefix(line, "-")
			cur.OldLines = append(cur.OldLines, text)
			oldNo := 0
			if cur.OldStart > 0 {
				oldNo = cur.OldStart + cur.OldCount
			}
			cur.Lines = append(cur.Lines, DiffLine{Kind: "del", Text: text, OldNo: oldNo})
			cur.OldCount++
		default:
			text := strings.TrimPrefix(line, " ")
			oldNo := 0
			if cur.OldStart > 0 {
				oldNo = cur.OldStart + cur.OldCount
			}
			cur.Lines = append(cur.Lines, DiffLine{Kind: "context", Text: text, OldNo: oldNo, NewNo: cur.NewStart + cur.NewCount})
			cur.OldCount++
			cur.NewCount++
		}
	}
	if cur != nil {
		hunks = append(hunks, *cur)
	}
	if status == "" {
		status = "modified"
	}
	return FileDiff{Path: path, Status: status, Additions: additions, Deletions: deletions, Hunks: hunks}
}

// DiffMaps compares before -> after and returns changed files only.
func DiffMaps(before, after map[string]string) []FileDiff {
	if before == nil {
		before = map[string]string{}
	}
	if after == nil {
		after = map[string]string{}
	}
	seen := map[string]struct{}{}
	paths := []string{}
	for p := range before {
		seen[p] = struct{}{}
		paths = append(paths, p)
	}
	for p := range after {
		if _, ok := seen[p]; ok {
			continue
		}
		paths = append(paths, p)
	}
	sortStrings(paths)
	out := []FileDiff{}
	for _, p := range paths {
		oldText, oldOK := before[p]
		newText, newOK := after[p]
		if oldOK && newOK && oldText == newText {
			continue
		}
		status := "modified"
		if !oldOK {
			status = "added"
		} else if !newOK {
			status = "deleted"
		}
		hunks := buildHunks(oldText, newText)
		add, del := 0, 0
		for _, h := range hunks {
			add += len(h.NewLines)
			del += len(h.OldLines)
		}
		out = append(out, FileDiff{
			Path: p, Status: status, Additions: add, Deletions: del, Hunks: hunks,
		})
	}
	return out
}

func buildHunks(oldText, newText string) []DiffHunk {
	oldLines := splitKeep(oldText)
	newLines := splitKeep(newText)
	ops := lineOps(oldLines, newLines)
	hunks := []DiffHunk{}
	i := 0
	for i < len(ops) {
		if ops[i].kind == "equal" {
			i++
			continue
		}
		start := i
		for i < len(ops) && ops[i].kind != "equal" {
			i++
		}
		hunks = append(hunks, hunkFromOps(ops, start, i, len(hunks)))
	}
	if len(hunks) == 0 && oldText != newText {
		hunks = append(hunks, hunkFromOps(ops, 0, len(ops), 0))
	}
	return hunks
}

type lineOp struct {
	kind string
	text string
	oldN int
	newN int
}

func lineOps(a, b []string) []lineOp {
	if len(a)*len(b) > 250000 {
		return blockOps(a, b)
	}
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := []lineOp{}
	i, j := 0, 0
	oldN, newN := 1, 1
	for i < n && j < m {
		if a[i] == b[j] {
			ops = append(ops, lineOp{kind: "equal", text: a[i], oldN: oldN, newN: newN})
			i++
			j++
			oldN++
			newN++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, lineOp{kind: "del", text: a[i], oldN: oldN})
			i++
			oldN++
			continue
		}
		ops = append(ops, lineOp{kind: "add", text: b[j], newN: newN})
		j++
		newN++
	}
	for i < n {
		ops = append(ops, lineOp{kind: "del", text: a[i], oldN: oldN})
		i++
		oldN++
	}
	for j < m {
		ops = append(ops, lineOp{kind: "add", text: b[j], newN: newN})
		j++
		newN++
	}
	return ops
}

func blockOps(a, b []string) []lineOp {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	ops := []lineOp{}
	oldN, newN := 1, 1
	for i := 0; i < prefix; i++ {
		ops = append(ops, lineOp{kind: "equal", text: a[i], oldN: oldN, newN: newN})
		oldN++
		newN++
	}
	for i := prefix; i < len(a)-suffix; i++ {
		ops = append(ops, lineOp{kind: "del", text: a[i], oldN: oldN})
		oldN++
	}
	for j := prefix; j < len(b)-suffix; j++ {
		ops = append(ops, lineOp{kind: "add", text: b[j], newN: newN})
		newN++
	}
	for i := len(a) - suffix; i < len(a); i++ {
		ops = append(ops, lineOp{kind: "equal", text: a[i], oldN: oldN, newN: newN})
		oldN++
		newN++
	}
	return ops
}

func hunkFromOps(ops []lineOp, start, end, index int) DiffHunk {
	h := DiffHunk{Index: index}
	if start < len(ops) {
		if ops[start].oldN > 0 {
			h.OldStart = ops[start].oldN
		}
		if ops[start].newN > 0 {
			h.NewStart = ops[start].newN
		}
	}
	if h.OldStart == 0 {
		h.OldStart = 1
	}
	if h.NewStart == 0 {
		h.NewStart = 1
	}
	for _, op := range ops[start:end] {
		switch op.kind {
		case "del":
			h.OldLines = append(h.OldLines, op.text)
			h.OldCount++
			h.Lines = append(h.Lines, DiffLine{Kind: "del", Text: op.text, OldNo: op.oldN})
		case "add":
			h.NewLines = append(h.NewLines, op.text)
			h.NewCount++
			h.Lines = append(h.Lines, DiffLine{Kind: "add", Text: op.text, NewNo: op.newN})
		default:
			h.OldCount++
			h.NewCount++
			h.Lines = append(h.Lines, DiffLine{Kind: "context", Text: op.text, OldNo: op.oldN, NewNo: op.newN})
		}
	}
	h.Header = fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
	return h
}

// ApplySelectedHunks rebuilds a file from base, applying only the chosen hunks.
func ApplySelectedHunks(base string, hunks []DiffHunk, selected map[int]bool) string {
	lines := splitKeep(base)
	out := []string{}
	cursor := 0
	for _, h := range hunks {
		start := h.OldStart - 1
		if start < 0 {
			start = 0
		}
		for cursor < start && cursor < len(lines) {
			out = append(out, lines[cursor])
			cursor++
		}
		if selected[h.Index] {
			out = append(out, h.NewLines...)
			cursor += h.OldCount
			if cursor > len(lines) {
				cursor = len(lines)
			}
			continue
		}
		for k := 0; k < h.OldCount && cursor < len(lines); k++ {
			out = append(out, lines[cursor])
			cursor++
		}
	}
	if cursor < len(lines) {
		out = append(out, lines[cursor:]...)
	}
	return strings.Join(out, "\n")
}

// RevertHunk removes one hunk from the current (new) file.
func RevertHunk(current string, h DiffHunk) string {
	lines := splitKeep(current)
	start := h.NewStart - 1
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := start + h.NewCount
	if end > len(lines) {
		end = len(lines)
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, h.OldLines...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}

// ApplyGitAction updates the Neon working tree. commitFiles is what should be pushed.
func ApplyGitAction(base, uncommitted, staged map[string]string, action, path string, hunkIndex int) (map[string]string, map[string]string, map[string]string, map[string]string, error) {
	base = cloneMap(base)
	uncommitted = cloneMap(uncommitted)
	staged = cloneMap(staged)
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "stage":
		if path == "" {
			return nil, nil, nil, nil, fmt.Errorf("path is required")
		}
		if hunkIndex >= 0 {
			content, err := stageOneHunk(base[path], uncommitted[path], staged[path], hunkIndex)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			staged[path] = content
		} else if content, ok := uncommitted[path]; ok {
			staged[path] = content
		} else {
			return nil, nil, nil, nil, fmt.Errorf("nothing to stage for %s", path)
		}
	case "unstage":
		if path == "" {
			return nil, nil, nil, nil, fmt.Errorf("path is required")
		}
		if hunkIndex >= 0 {
			content, err := unstageOneHunk(base[path], uncommitted[path], staged[path], hunkIndex)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			if content == base[path] {
				delete(staged, path)
			} else {
				staged[path] = content
			}
		} else {
			delete(staged, path)
		}
	case "unstage_all":
		staged = map[string]string{}
	case "stage_all":
		for p, content := range uncommitted {
			staged[p] = content
		}
	case "revert":
		if path == "" {
			return nil, nil, nil, nil, fmt.Errorf("path is required")
		}
		if hunkIndex >= 0 {
			hunks := buildHunks(base[path], uncommitted[path])
			if hunkIndex >= len(hunks) {
				return nil, nil, nil, nil, fmt.Errorf("hunk %d not found", hunkIndex)
			}
			next := RevertHunk(uncommitted[path], hunks[hunkIndex])
			if next == base[path] {
				delete(uncommitted, path)
			} else {
				uncommitted[path] = next
			}
		} else {
			delete(uncommitted, path)
		}
		delete(staged, path)
	case "revert_all":
		uncommitted = map[string]string{}
		staged = map[string]string{}
	case "commit_staged":
		commit := cloneMap(staged)
		if len(commit) == 0 {
			return nil, nil, nil, nil, fmt.Errorf("nothing staged")
		}
		for p, content := range commit {
			base[p] = content
			if uncommitted[p] == content {
				delete(uncommitted, p)
			}
		}
		staged = map[string]string{}
		return base, uncommitted, staged, commit, nil
	default:
		return nil, nil, nil, nil, fmt.Errorf("unknown git action %s", action)
	}
	return base, uncommitted, staged, nil, nil
}

func stageOneHunk(baseText, current, already string, hunkIndex int) (string, error) {
	hunks := buildHunks(baseText, current)
	if hunkIndex >= len(hunks) {
		return "", fmt.Errorf("hunk %d not found", hunkIndex)
	}
	selected := selectedHunks(baseText, current, already)
	selected[hunkIndex] = true
	return ApplySelectedHunks(baseText, hunks, selected), nil
}

func unstageOneHunk(baseText, current, already string, hunkIndex int) (string, error) {
	hunks := buildHunks(baseText, current)
	if hunkIndex >= len(hunks) {
		return "", fmt.Errorf("hunk %d not found", hunkIndex)
	}
	selected := selectedHunks(baseText, current, already)
	delete(selected, hunkIndex)
	return ApplySelectedHunks(baseText, hunks, selected), nil
}

func selectedHunks(baseText, current, already string) map[int]bool {
	selected := map[int]bool{}
	if already == "" || already == baseText {
		return selected
	}
	for _, h := range buildHunks(baseText, current) {
		if hunkApplied(already, h) {
			selected[h.Index] = true
		}
	}
	return selected
}

func hunkApplied(content string, h DiffHunk) bool {
	if len(h.NewLines) == 0 {
		return false
	}
	return strings.Contains(content, strings.Join(h.NewLines, "\n"))
}

func splitKeep(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

func cloneMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		j := i
		for j > 0 && in[j] < in[j-1] {
			in[j], in[j-1] = in[j-1], in[j]
			j--
		}
	}
}
