package action_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/adrijshikhar/actr/internal/action"
	"github.com/adrijshikhar/actr/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_RejectsDotAndSlashVersions(t *testing.T) {
	baseDir := t.TempDir()
	keepDir := filepath.Join(baseDir, "_actions", "org", "repo", "v1")
	require.NoError(t, os.MkdirAll(keepDir, 0o755))

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{Name: "repo/action.yml", Mode: 0644, Size: int64(len("runs:\n  using: node20\n"))}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err := tw.Write([]byte("runs:\n  using: node20\n"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	r := action.NewActionResolver(baseDir, srv.Client())
	r.BaseURL = srv.URL
	for _, badVer := range []string{".", "./v1", "feature/branch", `..\other`, "foo/.."} {
		_, err := r.Resolve(context.Background(), &protocol.StepReference{
			Name:    "org/repo",
			Version: badVer,
		})
		require.Error(t, err, "version %q must be rejected", badVer)
		assert.ErrorIs(t, err, action.ErrPathTraversal, "version %q must be ErrPathTraversal", badVer)
	}
	assert.DirExists(t, keepDir)
}

func TestResolve_RejectsMultiEntrySymlinkAndHardlinkEscape(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	entries := []struct {
		hdr  tar.Header
		body string
	}{
		{hdr: tar.Header{Name: "repo-v1/a/b/c/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "repo-v1/a/b/c/up", Typeflag: tar.TypeSymlink, Linkname: "../../..", Mode: 0o777}},
		// Lexical Dir(a/b/c/up) + ../../../etc previously stayed inside cleanDest even though up -> cleanDest
		{hdr: tar.Header{Name: "repo-v1/a/b/c/up/escape", Typeflag: tar.TypeSymlink, Linkname: "../../../etc", Mode: 0o777}},
	}
	for _, e := range entries {
		if e.body != "" {
			e.hdr.Size = int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(&e.hdr))
		if e.body != "" {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	baseDir := t.TempDir()
	r := action.NewActionResolver(baseDir, srv.Client())
	r.BaseURL = srv.URL

	_, err := r.Resolve(context.Background(), &protocol.StepReference{
		Name:    "org/repo",
		Version: "v1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, action.ErrPathTraversal)
}

func TestResolve_RejectsHardlinkTargetingSymlink(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	entries := []struct {
		hdr  tar.Header
		body string
	}{
		{hdr: tar.Header{Name: "repo-v1/target.txt", Typeflag: tar.TypeReg, Mode: 0o644}, body: "target"},
		{hdr: tar.Header{Name: "repo-v1/sym", Typeflag: tar.TypeSymlink, Linkname: "target.txt", Mode: 0o777}},
		{hdr: tar.Header{Name: "repo-v1/hardlink", Typeflag: tar.TypeLink, Linkname: "repo-v1/sym", Mode: 0o644}},
	}
	for _, e := range entries {
		if e.body != "" {
			e.hdr.Size = int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(&e.hdr))
		if e.body != "" {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	baseDir := t.TempDir()
	r := action.NewActionResolver(baseDir, srv.Client())
	r.BaseURL = srv.URL

	_, err := r.Resolve(context.Background(), &protocol.StepReference{
		Name:    "org/repo",
		Version: "v1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, action.ErrPathTraversal)
}

func TestResolve_NormalizesWindowsBackslashesInTar(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	entries := []struct {
		hdr  tar.Header
		body string
	}{
		{hdr: tar.Header{Name: "repo-v1\\sub\\action.yml", Typeflag: tar.TypeReg, Mode: 0o644}, body: "runs:\n  using: node20\n  main: index.js\n"},
	}
	for _, e := range entries {
		if e.body != "" {
			e.hdr.Size = int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(&e.hdr))
		if e.body != "" {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	baseDir := t.TempDir()
	r := action.NewActionResolver(baseDir, srv.Client())
	r.BaseURL = srv.URL

	dest, err := r.Resolve(context.Background(), &protocol.StepReference{
		Name:    "org/repo",
		Version: "v1",
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dest, "sub", "action.yml"))
}

type tarEntry struct {
	Name     string
	Content  string
	Mode     int64
	Typeflag byte
	Linkname string
}

func createTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, entry := range entries {
		mode := entry.Mode
		if mode == 0 {
			if entry.Typeflag == tar.TypeDir {
				mode = 0755
			} else {
				mode = 0644
			}
		}
		typeflag := entry.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     entry.Name,
			Mode:     mode,
			Size:     int64(len(entry.Content)),
			Typeflag: typeflag,
			Linkname: entry.Linkname,
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if len(entry.Content) > 0 {
			_, err := tw.Write([]byte(entry.Content))
			require.NoError(t, err)
		}
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	return buf.Bytes()
}

func TestActionResolver_ZipSlipProtection(t *testing.T) {
	testCases := []struct {
		name    string
		entries []tarEntry
	}{
		{
			name: "relative traversal prefix",
			entries: []tarEntry{
				{Name: "../../malicious.txt", Content: "evil payload"},
			},
		},
		{
			name: "subfolder traversal escape",
			entries: []tarEntry{
				{Name: "setup-go-v5/../../malicious.txt", Content: "evil payload"},
			},
		},
		{
			name: "absolute path traversal",
			entries: []tarEntry{
				{Name: "/etc/passwd", Content: "evil payload"},
			},
		},
		{
			name: "symlink targeting outside destination",
			entries: []tarEntry{
				{Name: "setup-go-v5/evil-link", Typeflag: tar.TypeSymlink, Linkname: "../../malicious.txt"},
			},
		},
		{
			name: "symlink targeting absolute path",
			entries: []tarEntry{
				{Name: "setup-go-v5/abs-link", Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			archiveData := createTarGz(t, tc.entries)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/gzip")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(archiveData)
			}))
			defer server.Close()

			tmpDir := t.TempDir()
			resolver := action.NewActionResolver(tmpDir, server.Client())
			resolver.BaseURL = server.URL

			ref := &protocol.StepReference{
				Type: "repository",
				Name: "malicious/action",
				Ref:  "v1",
			}

			_, err := resolver.Resolve(context.Background(), ref)
			require.Error(t, err)
			assert.True(t, errors.Is(err, action.ErrPathTraversal), "expected ErrPathTraversal, got: %v", err)
		})
	}
}

func TestActionResolver_ForbidSetuid(t *testing.T) {
	entries := []tarEntry{
		{Name: "setup-go-v5/action.yml", Content: "name: test\nruns:\n  using: node20\n  main: index.js\n"},
		{Name: "setup-go-v5/suid-binary", Content: "evil", Mode: 04755},
	}
	archiveData := createTarGz(t, entries)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archiveData)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	resolver := action.NewActionResolver(tmpDir, server.Client())
	resolver.BaseURL = server.URL

	ref := &protocol.StepReference{
		Type: "repository",
		Name: "actions/test-setuid",
		Ref:  "v1",
	}

	_, err := resolver.Resolve(context.Background(), ref)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden setuid/setgid")
}

func TestActionResolver_DownloadAndExtract_Success(t *testing.T) {
	entries := []tarEntry{
		{Name: "setup-go-v5/", Typeflag: tar.TypeDir},
		{Name: "setup-go-v5/action.yml", Content: "name: setup-go\nruns:\n  using: node20\n  main: dist/index.js\n"},
		{Name: "setup-go-v5/dist/", Typeflag: tar.TypeDir},
		{Name: "setup-go-v5/dist/index.js", Content: "console.log('setup-go');", Mode: 0755},
	}
	archiveData := createTarGz(t, entries)

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		assert.Equal(t, "/actions/setup-go/tar.gz/v5", r.URL.Path)
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archiveData)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	resolver := action.NewActionResolver(tmpDir, server.Client())
	resolver.BaseURL = server.URL

	ref := &protocol.StepReference{
		Type: "repository",
		Name: "actions/setup-go",
		Ref:  "v5",
	}

	// First call downloads and extracts
	actionDir, err := resolver.Resolve(context.Background(), ref)
	require.NoError(t, err)

	expectedDir := filepath.Join(tmpDir, "_actions", "actions", "setup-go", "v5")
	assert.Equal(t, expectedDir, actionDir)

	manifestPath := filepath.Join(actionDir, "action.yml")
	assert.FileExists(t, manifestPath)
	manifestData, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	assert.Contains(t, string(manifestData), "name: setup-go")

	indexPath := filepath.Join(actionDir, "dist", "index.js")
	assert.FileExists(t, indexPath)
	indexFi, err := os.Stat(indexPath)
	require.NoError(t, err)
	// Executable permission preserved
	assert.Equal(t, os.FileMode(0755), indexFi.Mode().Perm())

	markerPath := filepath.Join(tmpDir, "_actions", "actions", "setup-go", "v5.completed")
	assert.FileExists(t, markerPath)

	// Second call hits cache (no new HTTP request)
	actionDir2, err := resolver.Resolve(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, expectedDir, actionDir2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&requestCount), "second call must use cached action without HTTP request")
}

func TestActionResolver_Subpath(t *testing.T) {
	entries := []tarEntry{
		{Name: "aws-actions-v1/", Typeflag: tar.TypeDir},
		{Name: "aws-actions-v1/ec2/", Typeflag: tar.TypeDir},
		{Name: "aws-actions-v1/ec2/action.yml", Content: "name: aws-ec2\nruns:\n  using: node20\n  main: index.js\n"},
		{Name: "aws-actions-v1/s3/action.yml", Content: "name: aws-s3\nruns:\n  using: node20\n  main: index.js\n"},
	}
	archiveData := createTarGz(t, entries)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/aws-actions/configure-aws/tar.gz/v1", r.URL.Path)
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archiveData)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	resolver := action.NewActionResolver(tmpDir, server.Client())
	resolver.BaseURL = server.URL

	ref := &protocol.StepReference{
		Type: "repository",
		Name: "aws-actions/configure-aws/ec2",
		Ref:  "v1",
	}

	subpathDir, err := resolver.Resolve(context.Background(), ref)
	require.NoError(t, err)

	expectedSubpath := filepath.Join(tmpDir, "_actions", "aws-actions", "configure-aws", "v1", "ec2")
	assert.Equal(t, expectedSubpath, subpathDir)
	assert.FileExists(t, filepath.Join(subpathDir, "action.yml"))
}

func TestActionResolver_ConcurrentDownload(t *testing.T) {
	entries := []tarEntry{
		{Name: "test-action-v1/action.yml", Content: "name: test\nruns:\n  using: node20\n  main: index.js\n"},
		{Name: "test-action-v1/index.js", Content: "console.log('hello');"},
	}
	archiveData := createTarGz(t, entries)

	var downloadAttempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&downloadAttempts, 1)
		// Small artificial latency to increase likelihood of lock contention
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archiveData)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	resolver := action.NewActionResolver(tmpDir, server.Client())
	resolver.BaseURL = server.URL

	ref := &protocol.StepReference{
		Type: "repository",
		Name: "actions/test-action",
		Ref:  "v1.0.0",
	}

	const goroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	results := make([]string, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			dir, err := resolver.Resolve(context.Background(), ref)
			if err != nil {
				errCh <- err
				return
			}
			results[idx] = dir
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent resolve error: %v", err)
	}

	expectedDir := filepath.Join(tmpDir, "_actions", "actions", "test-action", "v1.0.0")
	for i, res := range results {
		assert.Equal(t, expectedDir, res, "goroutine %d returned different path", i)
	}

	// Only 1 download should have occurred because other goroutines wait for the flock
	// and re-check markerFile!
	assert.Equal(t, int32(1), atomic.LoadInt32(&downloadAttempts), "flock must ensure exactly 1 download under concurrency")
}

func TestActionResolver_InvalidReference(t *testing.T) {
	tmpDir := t.TempDir()
	resolver := action.NewActionResolver(tmpDir, nil)

	testCases := []struct {
		name string
		ref  *protocol.StepReference
	}{
		{"nil reference", nil},
		{"empty name", &protocol.StepReference{Name: ""}},
		{"single segment name", &protocol.StepReference{Name: "actions"}},
		{"path traversal in name", &protocol.StepReference{Name: "actions/../evil"}},
		{"empty segments in name", &protocol.StepReference{Name: "///"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolver.Resolve(context.Background(), tc.ref)
			require.Error(t, err)
			assert.True(t, errors.Is(err, action.ErrInvalidReference) || errors.Is(err, action.ErrPathTraversal))
		})
	}
}

func TestActionResolver_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 Not Found"))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	resolver := action.NewActionResolver(tmpDir, server.Client())
	resolver.BaseURL = server.URL

	ref := &protocol.StepReference{
		Type: "repository",
		Name: "actions/missing-action",
		Ref:  "v1",
	}

	_, err := resolver.Resolve(context.Background(), ref)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestActionResolver_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	resolver := action.NewActionResolver(tmpDir, server.Client())
	resolver.BaseURL = server.URL

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ref := &protocol.StepReference{
		Type: "repository",
		Name: "actions/canceled-action",
		Ref:  "v1",
	}

	_, err := resolver.Resolve(ctx, ref)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestActionResolver_LockContentionContextCanceled(t *testing.T) {
	tmpDir := t.TempDir()
	resolver := action.NewActionResolver(tmpDir, http.DefaultClient)

	ref := &protocol.StepReference{
		Type: "repository",
		Name: "actions/locked-action",
		Ref:  "v1",
	}

	// Create and lock the file externally
	lockDir := filepath.Join(tmpDir, "_actions", "actions", "locked-action")
	require.NoError(t, os.MkdirAll(lockDir, 0755))
	lockFile := filepath.Join(lockDir, "v1.lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0644)
	require.NoError(t, err)
	defer func() {
		_ = f.Close()
	}()

	// Acquire lock on file to simulate a concurrent worker hanging
	require.NoError(t, action.LockFileForTest(context.Background(), f))
	defer func() {
		_ = action.UnlockFileForTest(f)
	}()

	// Now try to resolve with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = resolver.Resolve(ctx, ref)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled), "expected context deadline/canceled error, got: %v", err)
	assert.Less(t, elapsed, 1*time.Second, "lock acquisition did not respect context timeout")
}

func TestResolve_PostExtractionSymlinkVerification(t *testing.T) {
	t.Run("rejects dangling symlink after extraction without writing marker", func(t *testing.T) {
		archiveData := createTarGz(t, []tarEntry{
			{Name: "repo-v1/action.yml", Content: "runs:\n  using: node20\n  main: index.js\n"},
			{Name: "repo-v1/dangling", Typeflag: tar.TypeSymlink, Linkname: "does-not-exist", Mode: 0o777},
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(archiveData)
		}))
		defer srv.Close()

		baseDir := t.TempDir()
		r := action.NewActionResolver(baseDir, srv.Client())
		r.BaseURL = srv.URL

		_, err := r.Resolve(context.Background(), &protocol.StepReference{
			Name:    "org/dangling-repo",
			Version: "v1",
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, action.ErrPathTraversal)
		assert.NoFileExists(t, filepath.Join(baseDir, "_actions", "org", "dangling-repo", "v1.completed"))
	})

	t.Run("allows valid internal symlink resolving within destination", func(t *testing.T) {
		archiveData := createTarGz(t, []tarEntry{
			{Name: "repo-v1/action.yml", Content: "runs:\n  using: node20\n  main: index.js\n"},
			{Name: "repo-v1/index.js", Content: "console.log('ok');"},
			{Name: "repo-v1/alias.js", Typeflag: tar.TypeSymlink, Linkname: "index.js", Mode: 0o777},
		})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(archiveData)
		}))
		defer srv.Close()

		baseDir := t.TempDir()
		r := action.NewActionResolver(baseDir, srv.Client())
		r.BaseURL = srv.URL

		dest, err := r.Resolve(context.Background(), &protocol.StepReference{
			Name:    "org/valid-sym-repo",
			Version: "v1",
		})
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(dest, "alias.js"))
	})
}
