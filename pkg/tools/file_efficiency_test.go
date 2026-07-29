package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Layer 2: read_file line-range + line numbers ---

func TestReadFile_LineNumbers(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	content := "line one\nline two\nline three\n"
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte(content), 0644)

	result, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	expected := "1: line one\n2: line two\n3: line three"
	if result.Content != expected {
		t.Errorf("content = %q, want %q", result.Content, expected)
	}
	if result.TotalLines != 3 {
		t.Errorf("total_lines = %d, want 3", result.TotalLines)
	}
}

func TestReadFile_OffsetAndLimit(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, "content of line "+strings.Repeat("x", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(dir, "big.txt")
	os.WriteFile(path, []byte(content), 0644)

	offset := 5
	limit := 3
	result, err := ReadFile(nil, ReadFileArgs{Path: path, Offset: &offset, Limit: &limit})
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	// Should return lines 5, 6, 7 with line numbers
	if result.TotalLines != 20 {
		t.Errorf("total_lines = %d, want 20", result.TotalLines)
	}
	if result.Range != "lines 5-7 of 20" {
		t.Errorf("range = %q, want %q", result.Range, "lines 5-7 of 20")
	}

	resultLines := strings.Split(result.Content, "\n")
	if len(resultLines) != 3 {
		t.Fatalf("got %d lines, want 3", len(resultLines))
	}
	if !strings.HasPrefix(resultLines[0], "5: ") {
		t.Errorf("first line = %q, want prefix '5: '", resultLines[0])
	}
	if !strings.HasPrefix(resultLines[2], "7: ") {
		t.Errorf("third line = %q, want prefix '7: '", resultLines[2])
	}
}

func TestReadFile_OffsetPastEnd(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "small.txt")
	os.WriteFile(path, []byte("only one line"), 0644)

	offset := 100
	result, err := ReadFile(nil, ReadFileArgs{Path: path, Offset: &offset})
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if result.Content != "" {
		t.Errorf("content should be empty for offset past end, got %q", result.Content)
	}
	if result.TotalLines != 1 {
		t.Errorf("total_lines = %d, want 1", result.TotalLines)
	}
}

func TestReadFile_EmptyFile(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	os.WriteFile(path, []byte(""), 0644)

	result, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if result.TotalLines != 0 {
		t.Errorf("total_lines = %d, want 0", result.TotalLines)
	}
}

func TestReadFile_LimitExceedsTotalLines(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("a\nb\nc"), 0644)

	limit := 1000
	result, err := ReadFile(nil, ReadFileArgs{Path: path, Limit: &limit})
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	// Should return all 3 lines
	if result.TotalLines != 3 {
		t.Errorf("total_lines = %d, want 3", result.TotalLines)
	}
	resultLines := strings.Split(result.Content, "\n")
	if len(resultLines) != 3 {
		t.Errorf("got %d lines, want 3", len(resultLines))
	}
}

// --- Layer 1: edit_file verification context ---

func TestEditFile_VerificationContext(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "line_"+strings.Repeat("a", i)+"_end")
	}
	content := strings.Join(lines, "\n") + "\n"
	path := writeTestFile(t, dir, "test.txt", content)

	// Target line 15 specifically: "line_aaaaaaaaaaaaaaa_end" (15 a's)
	result, err := EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "line_aaaaaaaaaaaaaaa_end",
		NewString: "REPLACED_LINE_FIFTEEN",
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	if !result.Success {
		t.Fatal("Success = false, want true")
	}
	ctx := result.VerificationContext
	if ctx == "" {
		t.Fatal("VerificationContext is empty, expected unified hunk")
	}
	if !strings.Contains(ctx, "@@ test.txt:15") {
		t.Errorf("verification_context header want @@ test.txt:15, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "- 15| line_aaaaaaaaaaaaaaa_end") {
		t.Errorf("verification_context should show removed line, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "+ 15| REPLACED_LINE_FIFTEEN") {
		t.Errorf("verification_context should show added line, got:\n%s", ctx)
	}
	// Surrounding context lines present
	if !strings.Contains(ctx, "  14|") || !strings.Contains(ctx, "  16|") {
		t.Errorf("verification_context should include surrounding context, got:\n%s", ctx)
	}
	if !strings.Contains(result.Message, "(line 15)") {
		t.Errorf("message should include line number, got: %s", result.Message)
	}
}

func TestEditFile_VerificationContextDeletion(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	// Pad the file so the deletion is well below the start — catches the old
	// bug that always centered verification context on line 1 for deletions.
	var prefix []string
	for i := 1; i <= 20; i++ {
		prefix = append(prefix, fmt.Sprintf("pad_%d", i))
	}
	content := strings.Join(prefix, "\n") + "\nkeep this\nremove this\nkeep this too\n"
	path := writeTestFile(t, dir, "test.txt", content)

	result, err := EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "remove this\n",
		NewString: "", // deletion
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	if !result.Success {
		t.Fatal("Success = false")
	}
	ctx := result.VerificationContext
	if ctx == "" {
		t.Fatal("VerificationContext is empty for deletion")
	}
	// Line 22 = 20 pad lines + "keep this" + "remove this"
	if !strings.Contains(ctx, "@@ test.txt:22") {
		t.Errorf("deletion hunk should target line 22, not file start, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "- 22| remove this") {
		t.Errorf("deletion hunk should show removed line, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "keep this") {
		t.Errorf("deletion hunk should show surrounding context, got:\n%s", ctx)
	}
	// Must not dump the beginning of the file as if the edit were there.
	if strings.Contains(ctx, "| pad_1\n") || strings.HasSuffix(ctx, "| pad_1") {
		t.Errorf("deletion hunk must not fall back to file start, got:\n%s", ctx)
	}
	// Pure deletion: no + lines for the removed content
	if strings.Contains(ctx, "+ 22|") {
		t.Errorf("pure deletion should not emit + line for removed content, got:\n%s", ctx)
	}
}

func TestEditFile_VerificationContextMultiLine(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	content := "before\nold one\nold two\nafter\n"
	path := writeTestFile(t, dir, "multi.txt", content)

	result, err := EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "old one\nold two",
		NewString: "new one\nnew two\nnew three",
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	ctx := result.VerificationContext
	if !strings.Contains(ctx, "@@ multi.txt:2") {
		t.Errorf("want multi-line header at line 2, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "- 2| old one") || !strings.Contains(ctx, "- 3| old two") {
		t.Errorf("want removed multi-line block, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "+ 2| new one") || !strings.Contains(ctx, "+ 4| new three") {
		t.Errorf("want added multi-line block, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "  1| before") || !strings.Contains(ctx, "after") {
		t.Errorf("want surrounding context, got:\n%s", ctx)
	}
}

func TestEditFile_VerificationContextRegex(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	content := "alpha\nversion: 1.2.3\nomega\n"
	path := writeTestFile(t, dir, "ver.txt", content)

	result, err := EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: `version: (\d+\.\d+\.\d+)`,
		NewString: "version: $1-rc1",
		Regex:     true,
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	ctx := result.VerificationContext
	if !strings.Contains(ctx, "@@ ver.txt:2") {
		t.Errorf("want regex edit at line 2, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "- 2| version: 1.2.3") {
		t.Errorf("want actual matched text removed, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "+ 2| version: 1.2.3-rc1") {
		t.Errorf("want expanded replacement added, got:\n%s", ctx)
	}
}

func TestEditFile_VerificationContextReplaceAll(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	content := "aaa\nfoo\nbbb\nfoo\nccc\n"
	path := writeTestFile(t, dir, "all.txt", content)

	result, err := EditFile(nil, EditFileArgs{
		Path:       path,
		OldString:  "foo",
		NewString:  "bar",
		ReplaceAll: true,
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	if result.Replacements != 2 {
		t.Fatalf("replacements = %d, want 2", result.Replacements)
	}
	// Hunk shows first occurrence only; message reports full count.
	ctx := result.VerificationContext
	if !strings.Contains(ctx, "@@ all.txt:2") {
		t.Errorf("want first-occurrence hunk at line 2, got:\n%s", ctx)
	}
	if !strings.Contains(result.Message, "Replaced 2 occurrence") {
		t.Errorf("message should report full count, got: %s", result.Message)
	}
}

func TestEditFile_VerificationContextInsert(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := writeTestFile(t, dir, "ins.go", "func main() {\n\tsetup()\n}\n")

	// Insert by expanding an anchor line into anchor + new lines.
	result, err := EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "\tsetup()",
		NewString: "\tsetup()\n\trun()",
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	ctx := result.VerificationContext
	if !strings.Contains(ctx, "@@ ins.go:2") {
		t.Errorf("want insert at line 2, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "- 2| \tsetup()") {
		t.Errorf("want removed anchor, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "+ 2| \tsetup()") || !strings.Contains(ctx, "+ 3| \trun()") {
		t.Errorf("want inserted lines, got:\n%s", ctx)
	}
}

func TestEditFile_VerificationContextShrink(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := writeTestFile(t, dir, "shrink.txt", "keep\none\ntwo\nthree\nend\n")

	result, err := EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "one\ntwo\nthree",
		NewString: "only",
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	ctx := result.VerificationContext
	if !strings.Contains(ctx, "- 2| one") || !strings.Contains(ctx, "- 4| three") {
		t.Errorf("want multi-line removal, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "+ 2| only") {
		t.Errorf("want single added line, got:\n%s", ctx)
	}
	if strings.Contains(ctx, "+ 3|") {
		t.Errorf("shrink should not emit extra + lines, got:\n%s", ctx)
	}
}

// --- write_file verification context ---

func TestWriteFile_VerificationContextCreate(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	result, err := WriteFile(nil, WriteFileArgs{
		FilePath: path,
		Content:  "hello\nworld\n",
	})
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if !result.Created {
		t.Error("Created = false, want true for new file")
	}
	if !strings.Contains(result.Message, "Created") {
		t.Errorf("message should say Created, got: %s", result.Message)
	}
	ctx := result.VerificationContext
	if !strings.Contains(ctx, "@@ new.txt:1 (created)") {
		t.Errorf("want created header, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "+ 1| hello") || !strings.Contains(ctx, "+ 2| world") {
		t.Errorf("want + lines for new content, got:\n%s", ctx)
	}
	if strings.Contains(ctx, "- ") {
		t.Errorf("create should not emit - lines, got:\n%s", ctx)
	}
}

func TestWriteFile_VerificationContextOverwrite(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := writeTestFile(t, dir, "old.txt", "alpha\nbeta\n")

	result, err := WriteFile(nil, WriteFileArgs{
		FilePath: path,
		Content:  "gamma\ndelta\nepsilon\n",
	})
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if result.Created {
		t.Error("Created = true, want false for overwrite")
	}
	if !strings.Contains(result.Message, "Overwrote") {
		t.Errorf("message should say Overwrote, got: %s", result.Message)
	}
	ctx := result.VerificationContext
	if !strings.Contains(ctx, "@@ old.txt:1 (overwritten, 2→3 lines)") {
		t.Errorf("want overwrite header with line counts, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "- 1| alpha") || !strings.Contains(ctx, "- 2| beta") {
		t.Errorf("want - lines for old content, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "+ 1| gamma") || !strings.Contains(ctx, "+ 3| epsilon") {
		t.Errorf("want + lines for new content, got:\n%s", ctx)
	}
}

func TestWriteFile_VerificationContextTruncate(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	// Build content well over the 30-line body budget so the hunk truncates.
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "line_%d\n", i)
	}
	path := filepath.Join(dir, "big.txt")

	result, err := WriteFile(nil, WriteFileArgs{
		FilePath: path,
		Content:  b.String(),
	})
	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	ctx := result.VerificationContext
	if !strings.Contains(ctx, "…") {
		t.Errorf("large create should truncate with …, got:\n%s", ctx)
	}
	// Should not dump all 50 + lines
	plusCount := strings.Count(ctx, "+ ")
	if plusCount > 35 {
		t.Errorf("expected truncated + lines, got %d in:\n%s", plusCount, ctx)
	}
}

// --- Layer 3: File read cache ---

func TestFileReadCache_Basic(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "cached.txt")
	os.WriteFile(path, []byte("hello world"), 0644)

	// First read: should NOT be a cache hit
	result, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if result.Unchanged {
		t.Error("first read should not be unchanged")
	}
	if result.Content != "1: hello world" {
		t.Errorf("content = %q", result.Content)
	}

	// Second read (same path, same range): should be a cache hit
	result, err = ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !result.Unchanged {
		t.Error("second read should be unchanged (cache hit)")
	}
	if result.TotalLines != 1 {
		t.Errorf("total_lines = %d, want 1", result.TotalLines)
	}
}

func TestFileReadCache_InvalidatedByEdit(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "edited.txt")
	os.WriteFile(path, []byte("original content"), 0644)

	// First read: populates cache
	_, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Edit the file (invalidates cache)
	_, err = EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "original",
		NewString: "modified",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}

	// Third read: should NOT be a cache hit (edit invalidated it)
	result, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("third read: %v", err)
	}
	if result.Unchanged {
		t.Error("read after edit should not be unchanged")
	}
	if !strings.Contains(result.Content, "modified") {
		t.Errorf("should contain 'modified', got: %s", result.Content)
	}
}

func TestFileReadCache_InvalidatedByShellCommand(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "shell.txt")
	os.WriteFile(path, []byte("before shell"), 0644)

	// First read: populates cache
	_, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Shell command: marks all unverified
	_, err = ShellCommand(nil, ShellCommandArgs{Command: "echo hello"})
	if err != nil {
		t.Fatalf("shell_command: %v", err)
	}

	// Modify the file externally (simulating what a shell command might do)
	os.WriteFile(path, []byte("after shell"), 0644)

	// Wait a moment to ensure mtime changes
	time.Sleep(10 * time.Millisecond)

	// Read again: since file was modified (mtime changed), should NOT be cache hit
	result, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("read after shell: %v", err)
	}
	if result.Unchanged {
		t.Error("read after shell+modification should not be unchanged")
	}
	if !strings.Contains(result.Content, "after shell") {
		t.Errorf("should contain 'after shell', got: %s", result.Content)
	}
}

func TestFileReadCache_ForceBypass(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "force.txt")
	os.WriteFile(path, []byte("force content"), 0644)

	// First read: populates cache
	_, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Second read with force=true: should return content even though unchanged
	result, err := ReadFile(nil, ReadFileArgs{Path: path, Force: true})
	if err != nil {
		t.Fatalf("force read: %v", err)
	}
	if result.Unchanged {
		t.Error("force read should never be unchanged")
	}
	if !strings.Contains(result.Content, "force content") {
		t.Errorf("force read should have content, got: %s", result.Content)
	}
}

func TestFileReadCache_MustReadBeforeEdit(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "guard.txt")
	os.WriteFile(path, []byte("guard content"), 0644)

	// Read a different file first (to activate the guard)
	otherPath := filepath.Join(dir, "other.txt")
	os.WriteFile(otherPath, []byte("other"), 0644)
	_, err := ReadFile(nil, ReadFileArgs{Path: otherPath})
	if err != nil {
		t.Fatalf("read other: %v", err)
	}

	// Now try to edit guard.txt WITHOUT reading it first
	result, err := EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "guard",
		NewString: "edited",
	})
	if err != nil {
		t.Fatalf("EditFile() error = %v", err)
	}
	// Should be rejected (not an error, but success=false)
	if result.Success {
		t.Fatal("edit without prior read should be rejected (success=false)")
	}
	if !strings.Contains(result.Message, "must read") {
		t.Errorf("expected 'must read' message, got: %s", result.Message)
	}

	// Now read the file, then edit should work
	_, err = ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("read guard.txt: %v", err)
	}

	result, err = EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "guard",
		NewString: "edited",
	})
	if err != nil {
		t.Fatalf("EditFile() after read error = %v", err)
	}
	if !result.Success {
		t.Fatalf("edit after read should succeed, got: %s", result.Message)
	}
}

func TestFileReadCache_DiskPersistence(t *testing.T) {
	// Use a dedicated cache file for this test
	dir := t.TempDir()
	cacheFilePath = filepath.Join(dir, "persist_cache.json")
	t.Cleanup(func() { cacheFilePath = defaultCacheFilePath })

	testFile := filepath.Join(dir, "persist.txt")
	os.WriteFile(testFile, []byte("persistent"), 0644)

	// First read: populates cache and writes to disk
	_, err := ReadFile(nil, ReadFileArgs{Path: testFile})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// Verify cache file was written
	if _, err := os.Stat(cacheFilePath); os.IsNotExist(err) {
		t.Fatal("cache file was not written to disk")
	}

	// Simulate fresh process: load cache from disk directly
	cache := LoadFileReadCache()
	if cache == nil {
		t.Fatal("LoadFileReadCache returned nil")
	}
	key := buildCacheKey(testFile, 1, 0)
	entry, ok := cache.Get(key)
	if !ok {
		t.Fatal("cache entry not found after disk reload")
	}
	if entry.Source != "read" {
		t.Errorf("source = %q, want 'read'", entry.Source)
	}
	if entry.TotalLines != 1 {
		t.Errorf("total_lines = %d, want 1", entry.TotalLines)
	}
}

// --- Cross-agent cache scoping ---

func TestFileReadCache_CrossAgentScoping(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.txt")
	os.WriteFile(path, []byte("shared content"), 0644)

	// Agent "po" reads the file
	currentCaller = "po"
	result, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("po read: %v", err)
	}
	if result.Unchanged {
		t.Error("po first read should not be unchanged")
	}
	if result.Content != "1: shared content" {
		t.Errorf("po content = %q", result.Content)
	}

	// Agent "dev" reads the SAME file — should NOT get unchanged
	// (dev has never seen this file in its own conversation)
	currentCaller = "dev"
	result, err = ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("dev read: %v", err)
	}
	if result.Unchanged {
		t.Error("dev first read should NOT be unchanged (different caller)")
	}
	if result.Content != "1: shared content" {
		t.Errorf("dev content = %q", result.Content)
	}

	// Agent "dev" reads AGAIN — now it SHOULD get unchanged
	result, err = ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("dev second read: %v", err)
	}
	if !result.Unchanged {
		t.Error("dev second read SHOULD be unchanged (same caller, same file)")
	}

	// Agent "po" reads again — should also get unchanged (po already read it)
	currentCaller = "po"
	result, err = ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("po second read: %v", err)
	}
	if !result.Unchanged {
		t.Error("po second read SHOULD be unchanged")
	}
}

func TestFileReadCache_CrossAgentInvalidation(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "edited.txt")
	os.WriteFile(path, []byte("original"), 0644)

	// Both agents read the file
	currentCaller = "po"
	_, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("po read: %v", err)
	}
	currentCaller = "dev"
	_, err = ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("dev read: %v", err)
	}

	// Dev edits the file — should invalidate for ALL callers
	_, err = EditFile(nil, EditFileArgs{
		Path:      path,
		OldString: "original",
		NewString: "modified",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}

	// PO reads again — should get fresh content (edit invalidated all entries)
	currentCaller = "po"
	result, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("po read after edit: %v", err)
	}
	if result.Unchanged {
		t.Error("po read after edit should NOT be unchanged")
	}
	if !strings.Contains(result.Content, "modified") {
		t.Errorf("should contain 'modified', got: %s", result.Content)
	}
}

func TestFileReadCache_EmptyCallerFallback(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nocaller.txt")
	os.WriteFile(path, []byte("hello"), 0644)

	// Read with empty caller (single-agent mode / backwards compat)
	currentCaller = ""
	result, err := ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if result.Unchanged {
		t.Error("first read should not be unchanged")
	}

	// Second read with same empty caller — should dedup
	result, err = ReadFile(nil, ReadFileArgs{Path: path})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if !result.Unchanged {
		t.Error("second read with same (empty) caller should be unchanged")
	}
}
