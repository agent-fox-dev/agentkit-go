//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-03-44: Dirty files are searched through an overlay shard and merged by
// score, so a fresh edit survives the max_files cap.
func TestOverlayShardMergeByScore_TS03_44(t *testing.T) {
	root := t.TempDir()

	// Create 30 files that all match a query with high scores.
	// Each file has a unique strong match for "HOTWORD" so they rank highly.
	for i := 0; i < 30; i++ {
		content := fmt.Sprintf("package pkg\n// HOTWORD HOTWORD HOTWORD line %d\nfunc Hot%d() { /* HOTWORD */ }\n", i, i)
		mkFile(t, root, fmt.Sprintf("hot%02d.go", i), content)
	}

	// Create one file that also matches but with lower initial score.
	mkFile(t, root, "edited.go", "package pkg\n// nothing here\nfunc Edited() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := New(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Build the index.
	in, _ := json.Marshal(map[string]any{"query": "HOTWORD"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial search failed: %s: %s", r.Error, r.Detail)
	}

	// Now edit edited.go so it matches HOTWORD (with lower score than the
	// 30 hot files).
	os.WriteFile(filepath.Join(root, "edited.go"),
		[]byte("package pkg\n// HOTWORD appears here\nfunc Edited() {}\n"), 0o644)
	idx.Invalidate("edited.go")

	// Search with max_files=10. The edited file should appear even though
	// its score is below the cap among indexed hits, because overlay hits
	// are merged by score.
	in, _ = json.Marshal(map[string]any{"query": "HOTWORD", "max_files": 10})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search after edit failed: %s: %s", r.Error, r.Detail)
	}

	paths := getResultFilePaths(r)
	foundEdited := false
	for _, p := range paths {
		if p == "edited.go" {
			foundEdited = true
			break
		}
	}
	if !foundEdited {
		t.Error("edited.go should appear in the result even though its score is below the cap among indexed hits")
	}

	// Verify dirty_files is 1.
	if d := r.Data; d != nil {
		df, _ := d["dirty_files"]
		switch v := df.(type) {
		case int:
			if v != 1 {
				t.Errorf("dirty_files = %d, want 1", v)
			}
		case float64:
			if int(v) != 1 {
				t.Errorf("dirty_files = %v, want 1", v)
			}
		default:
			t.Errorf("dirty_files has unexpected type %T = %v", df, df)
		}
	}

	// Verify the files are sorted by score (descending).
	if r.Data != nil {
		files, ok := r.Data["files"].([]any)
		if ok && len(files) > 1 {
			prevScore := 1e18
			for i, f := range files {
				m, ok := f.(map[string]any)
				if !ok {
					continue
				}
				score, _ := m["score"].(float64)
				if score > prevScore {
					t.Errorf("file %d score %.2f > previous score %.2f — not sorted by score", i, score, prevScore)
				}
				prevScore = score
			}
		}
	}

	// Repeat the query — the overlay should not be rebuilt (counter stays 1).
	overlayBuildsBefore := idx.OverlayBuildCount()
	in, _ = json.Marshal(map[string]any{"query": "HOTWORD", "max_files": 10})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("repeat search failed: %s: %s", r.Error, r.Detail)
	}
	overlayBuildsAfter := idx.OverlayBuildCount()
	if overlayBuildsAfter != overlayBuildsBefore {
		t.Errorf("overlay build count changed from %d to %d on repeat query with same dirty set",
			overlayBuildsBefore, overlayBuildsAfter)
	}
}

// TS-03-45: At 4% dirty no rebuild happens and at 6% a single rebuild
// replaces the index.
func TestRebuildThreshold_TS03_45(t *testing.T) {
	root := t.TempDir()

	// Create 100 files.
	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("package pkg\n// content%03d unique\nfunc F%03d() {}\n", i, i)
		mkFile(t, root, fmt.Sprintf("file%03d.go", i), content)
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := New(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Build the index.
	in, _ := json.Marshal(map[string]any{"query": "content000"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial search failed: %s: %s", r.Error, r.Detail)
	}
	if idx.BuildCount() != 1 {
		t.Fatalf("initial build count = %d, want 1", idx.BuildCount())
	}

	// Record the old run directory.
	oldRunDir := idx.RunDir()

	// Invalidate 4 files (4% of 100).
	for i := 0; i < 4; i++ {
		rel := fmt.Sprintf("file%03d.go", i)
		os.WriteFile(filepath.Join(root, rel),
			[]byte(fmt.Sprintf("package pkg\n// updated%03d unique\nfunc F%03d() {}\n", i, i)), 0o644)
		idx.Invalidate(rel)
	}

	// Query — should NOT trigger a rebuild.
	in, _ = json.Marshal(map[string]any{"query": "unique"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search at 4%% dirty failed: %s: %s", r.Error, r.Detail)
	}
	if idx.BuildCount() != 1 {
		t.Errorf("build count at 4%% dirty = %d, want 1", idx.BuildCount())
	}

	// Invalidate 2 more files (now 6% of 100).
	for i := 4; i < 6; i++ {
		rel := fmt.Sprintf("file%03d.go", i)
		os.WriteFile(filepath.Join(root, rel),
			[]byte(fmt.Sprintf("package pkg\n// updated%03d unique\nfunc F%03d() {}\n", i, i)), 0o644)
		idx.Invalidate(rel)
	}

	// Query — should trigger a rebuild.
	in, _ = json.Marshal(map[string]any{"query": "unique"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search at 6%% dirty failed: %s: %s", r.Error, r.Detail)
	}
	if idx.BuildCount() != 2 {
		t.Errorf("build count at 6%% dirty = %d, want 2", idx.BuildCount())
	}

	// The old shard directory should be gone.
	if oldRunDir != "" {
		if _, err := os.Stat(oldRunDir); !os.IsNotExist(err) {
			t.Errorf("old run directory %q should be deleted after rebuild, err=%v", oldRunDir, err)
		}
	}

	// The new content should be returned.
	in, _ = json.Marshal(map[string]any{"query": "updated000"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for updated content failed: %s: %s", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "updated000") {
		t.Error("after rebuild, the new content should be returned")
	}
}

// TS-03-46: Queries arriving during a rebuild wait, use the new index, and an
// abandoned wait returns aborted without waiting for the build.
func TestRebuildWaitAndAbort_TS03_46(t *testing.T) {
	root := t.TempDir()

	// Create 100 files.
	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("package pkg\n// word%03d unique\nfunc F%03d() {}\n", i, i)
		mkFile(t, root, fmt.Sprintf("file%03d.go", i), content)
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := New(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Build the index.
	in, _ := json.Marshal(map[string]any{"query": "word000"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial search failed: %s: %s", r.Error, r.Detail)
	}

	// Invalidate 6 files (6% of 100) to trigger a rebuild.
	for i := 0; i < 6; i++ {
		rel := fmt.Sprintf("file%03d.go", i)
		os.WriteFile(filepath.Join(root, rel),
			[]byte(fmt.Sprintf("package pkg\n// newword%03d unique\nfunc F%03d() {}\n", i, i)), 0o644)
		idx.Invalidate(rel)
	}

	// Block the rebuild using the outline hook.
	buildBlocked := make(chan struct{}, 1)
	releaseBuild := make(chan struct{})
	idx.testOutlineHook = func(_ string, _ int, _ bool) {
		select {
		case buildBlocked <- struct{}{}:
		default:
		}
		<-releaseBuild
	}

	// Start a query that will trigger the rebuild and block.
	var q1Result core.ToolResult
	q1Done := make(chan struct{})
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "unique"})
		q1Result = tool.Execute(ctx, in)
		close(q1Done)
	}()

	// Wait for the rebuild to block.
	select {
	case <-buildBlocked:
	case <-time.After(5 * time.Second):
		close(releaseBuild)
		t.Fatal("rebuild did not block in time")
	}

	// Start a second query with a cancellable context.
	ctx2, cancel2 := context.WithCancel(context.Background())
	var q2Result core.ToolResult
	q2Done := make(chan struct{})
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "unique"})
		q2Result = tool.Execute(ctx2, in)
		close(q2Done)
	}()

	// Give q2 time to start waiting.
	time.Sleep(100 * time.Millisecond)

	// Cancel q2's context — it should return aborted without waiting.
	cancel2()
	select {
	case <-q2Done:
	case <-time.After(2 * time.Second):
		close(releaseBuild)
		t.Fatal("cancelled waiter did not return in time")
	}

	if q2Result.OK {
		t.Error("cancelled waiter should return error")
	}
	if q2Result.Error != "aborted" {
		t.Errorf("cancelled waiter error = %q, want aborted", q2Result.Error)
	}

	// Verify the build is still blocked (q1 hasn't returned).
	select {
	case <-q1Done:
		t.Error("q1 should still be waiting for the build")
	default:
		// Good.
	}

	// Release the build.
	close(releaseBuild)
	select {
	case <-q1Done:
	case <-time.After(10 * time.Second):
		t.Fatal("q1 did not return after build was released")
	}

	// q1 should succeed with the new content.
	if !q1Result.OK {
		t.Fatalf("q1 should succeed: %s: %s", q1Result.Error, q1Result.Detail)
	}
}

// TS-03-49: After any sequence of write_file, edit_file and shell calls a
// code_search never shows older content than the last call.
func TestFreshnessPropertySequence_TS03_49(t *testing.T) {
	// Property test: for random sequences of operations, code_search always
	// shows the latest content and never shows overwritten or deleted content.

	rng := rand.New(rand.NewSource(42))

	for trial := 0; trial < 5; trial++ {
		t.Run(fmt.Sprintf("trial_%d", trial), func(t *testing.T) {
			root := t.TempDir()

			// Start with 5 files.
			model := make(map[string]string) // rel -> current content
			for i := 0; i < 5; i++ {
				rel := fmt.Sprintf("f%d.go", i)
				content := fmt.Sprintf("package pkg\n// initial%d token%d\nfunc F%d() {}\n", i, i, i)
				mkFile(t, root, rel, content)
				model[rel] = content
			}

			ws, err := tools.NewWorkspace(root)
			if err != nil {
				t.Fatal(err)
			}

			idx, err := New(ws, Options{
				TempDir:      t.TempDir(),
				Ignore:       tools.NoGlobalExcludes(),
				DisableCtags: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer idx.Close()

			tool := idx.Tools()[0]
			ctx := context.Background()

			// Build the index.
			in, _ := json.Marshal(map[string]any{"query": "initial0"})
			r := tool.Execute(ctx, in)
			if !r.OK {
				t.Fatalf("initial search failed: %s: %s", r.Error, r.Detail)
			}

			// Track tokens that have been overwritten or deleted.
			deadTokens := make(map[string]bool)
			tokenCounter := 100

			// Generate a random sequence of 1..15 operations.
			nOps := 1 + rng.Intn(15)
			var lastToken string

			for op := 0; op < nOps; op++ {
				tokenCounter++
				token := fmt.Sprintf("tok%d", tokenCounter)
				lastToken = token

				opType := rng.Intn(5)
				switch opType {
				case 0: // write_file (new or existing)
					rel := fmt.Sprintf("f%d.go", rng.Intn(8)) // may create new files
					oldContent, existed := model[rel]
					if existed {
						// Mark old tokens as dead.
						for _, line := range strings.Split(oldContent, "\n") {
							for _, w := range strings.Fields(line) {
								if strings.HasPrefix(w, "tok") {
									deadTokens[w] = true
								}
							}
						}
					}
					content := fmt.Sprintf("package pkg\n// %s written\nfunc W%d() {}\n", token, tokenCounter)
					os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644)
					model[rel] = content
					idx.Invalidate(rel)

				case 1: // edit_file (success)
					// Pick an existing file.
					var rels []string
					for r := range model {
						rels = append(rels, r)
					}
					if len(rels) == 0 {
						continue
					}
					sort.Strings(rels)
					rel := rels[rng.Intn(len(rels))]
					oldContent := model[rel]
					for _, line := range strings.Split(oldContent, "\n") {
						for _, w := range strings.Fields(line) {
							if strings.HasPrefix(w, "tok") {
								deadTokens[w] = true
							}
						}
					}
					content := fmt.Sprintf("package pkg\n// %s edited\nfunc E%d() {}\n", token, tokenCounter)
					os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644)
					model[rel] = content
					idx.Invalidate(rel)

				case 2: // shell edit
					var rels []string
					for r := range model {
						rels = append(rels, r)
					}
					if len(rels) == 0 {
						continue
					}
					sort.Strings(rels)
					rel := rels[rng.Intn(len(rels))]
					oldContent := model[rel]
					for _, line := range strings.Split(oldContent, "\n") {
						for _, w := range strings.Fields(line) {
							if strings.HasPrefix(w, "tok") {
								deadTokens[w] = true
							}
						}
					}
					content := fmt.Sprintf("package pkg\n// %s shell\nfunc S%d() {}\n", token, tokenCounter)
					os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644)
					model[rel] = content
					idx.Invalidate("") // shell triggers revalidateAll

				case 3: // shell delete
					var rels []string
					for r := range model {
						rels = append(rels, r)
					}
					if len(rels) == 0 {
						continue
					}
					sort.Strings(rels)
					rel := rels[rng.Intn(len(rels))]
					oldContent := model[rel]
					for _, line := range strings.Split(oldContent, "\n") {
						for _, w := range strings.Fields(line) {
							if strings.HasPrefix(w, "tok") {
								deadTokens[w] = true
							}
						}
					}
					os.Remove(filepath.Join(root, rel))
					delete(model, rel)
					idx.Invalidate("") // shell triggers revalidateAll

				case 4: // shell create
					rel := fmt.Sprintf("new%d.go", tokenCounter)
					content := fmt.Sprintf("package pkg\n// %s created\nfunc C%d() {}\n", token, tokenCounter)
					os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644)
					model[rel] = content
					idx.Invalidate("") // shell triggers revalidateAll
				}
			}

			// Search for the last token written.
			if lastToken == "" {
				return
			}

			in, _ = json.Marshal(map[string]any{"query": lastToken})
			r = tool.Execute(ctx, in)
			if !r.OK {
				t.Fatalf("search for %q failed: %s: %s", lastToken, r.Error, r.Detail)
			}

			// The result should contain the latest content.
			if !strings.Contains(r.Text, lastToken) {
				t.Errorf("result should contain latest token %q", lastToken)
			}

			// No dead token should appear in the result.
			for dt := range deadTokens {
				// Only check tokens that are not also in current model content.
				stillAlive := false
				for _, content := range model {
					if strings.Contains(content, dt) {
						stillAlive = true
						break
					}
				}
				if stillAlive {
					continue
				}
				if strings.Contains(r.Text, dt) {
					t.Errorf("dead token %q should not appear in result", dt)
				}
			}

			// Deleted files should not appear in results.
			resultPaths := getResultFilePaths(r)
			for _, p := range resultPaths {
				if _, exists := model[p]; !exists {
					t.Errorf("deleted file %q should not appear in results", p)
				}
			}
		})
	}
}

// --- helpers ---


