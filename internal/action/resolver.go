package action

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrijshikhar/actr/internal/protocol"
)

var (
	// ErrPathTraversal indicates an archive entry attempts path traversal outside destination.
	ErrPathTraversal = errors.New("archive contains invalid path traversal")
	// ErrInvalidReference indicates an invalid action step reference name or format.
	ErrInvalidReference = errors.New("invalid action reference")
)

// Resolver resolves action references to local filesystem paths.
type Resolver interface {
	Resolve(ctx context.Context, ref *protocol.StepReference) (string, error)
}

// ActionResolver downloads, verifies, and caches remote actions.
type ActionResolver struct {
	BaseDir    string
	HTTPClient *http.Client
	BaseURL    string // for testing; defaults to "https://codeload.github.com"
}

var _ Resolver = (*ActionResolver)(nil)

// NewActionResolver creates a new ActionResolver instance with default options.
func NewActionResolver(baseDir string, client *http.Client) *ActionResolver {
	if client == nil {
		client = http.DefaultClient
	}
	if absBase, err := filepath.Abs(baseDir); err == nil {
		baseDir = absBase
	}
	return &ActionResolver{
		BaseDir:    baseDir,
		HTTPClient: client,
		BaseURL:    "https://codeload.github.com",
	}
}

// Resolve resolves a StepReference to a local directory path, downloading and extracting it if needed.
func (r *ActionResolver) Resolve(ctx context.Context, ref *protocol.StepReference) (string, error) {
	if ref == nil || strings.TrimSpace(ref.Name) == "" {
		return "", ErrInvalidReference
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	trimmedName := strings.Trim(strings.TrimSpace(ref.Name), "/")
	parts := strings.Split(trimmedName, "/")
	var cleanParts []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			cleanParts = append(cleanParts, p)
		}
	}
	if len(cleanParts) < 2 {
		return "", ErrInvalidReference
	}

	owner := cleanParts[0]
	repo := cleanParts[1]
	if owner == "." || owner == ".." || repo == "." || repo == ".." {
		return "", ErrInvalidReference
	}
	if strings.ContainsAny(owner, `/\`) || strings.ContainsAny(repo, `/\`) {
		return "", ErrInvalidReference
	}

	var subpath string
	if len(cleanParts) > 2 {
		subpath = filepath.Join(cleanParts[2:]...)
	}
	if trimmedPath := strings.TrimSpace(ref.Path); trimmedPath != "" {
		if subpath != "" {
			subpath = filepath.Join(subpath, trimmedPath)
		} else {
			subpath = trimmedPath
		}
	}
	if subpath != "" {
		cleanSub := filepath.Clean(subpath)
		if strings.HasPrefix(cleanSub, "..") || filepath.IsAbs(cleanSub) {
			return "", ErrPathTraversal
		}
		subpath = cleanSub
	}

	version := strings.TrimSpace(ref.Ref)
	if version == "" {
		version = strings.TrimSpace(ref.Version)
	}
	if version == "" {
		version = "main"
	}
	cleanVersion := filepath.Clean(version)
	if strings.HasPrefix(cleanVersion, "..") || filepath.IsAbs(cleanVersion) {
		return "", ErrPathTraversal
	}

	baseDir := r.BaseDir
	if absBase, err := filepath.Abs(baseDir); err == nil {
		baseDir = absBase
	}
	destDir := filepath.Join(baseDir, "_actions", owner, repo, version)
	markerFile := filepath.Join(baseDir, "_actions", owner, repo, version+".completed")

	// Fast path: check if already downloaded and completed
	if _, err := os.Stat(markerFile); err == nil {
		return filepath.Abs(filepath.Join(destDir, subpath))
	}

	// Ensure parent directory exists for lock and marker files
	lockDir := filepath.Dir(markerFile)
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create action directory %s: %w", lockDir, err)
	}

	lockFilePath := filepath.Join(r.BaseDir, "_actions", owner, repo, version+".lock")
	lockF, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to open lock file %s: %w", lockFilePath, err)
	}
	defer func() {
		_ = unlockFile(lockF)
		_ = lockF.Close()
	}()

	// Acquire cross-platform exclusive advisory lock with context cancellation support
	if err := lockFile(ctx, lockF); err != nil {
		return "", fmt.Errorf("failed to acquire advisory lock on %s: %w", lockFilePath, err)
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}

	// Re-check markerFile after acquiring the lock (another process might have completed it)
	if _, err := os.Stat(markerFile); err == nil {
		return filepath.Abs(filepath.Join(destDir, subpath))
	}

	baseURL := r.BaseURL
	if baseURL == "" {
		baseURL = "https://codeload.github.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	reqURL := fmt.Sprintf("%s/%s/%s/tar.gz/%s", baseURL, owner, repo, version)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create download request: %w", err)
	}

	client := r.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download action %s/%s@%s: %w", owner, repo, version, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d (%s) downloading action %s/%s@%s", resp.StatusCode, resp.Status, owner, repo, version)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to create gzip reader for %s/%s@%s: %w", owner, repo, version, err)
	}
	defer func() {
		_ = gz.Close()
	}()

	tr := tar.NewReader(gz)

	_ = os.RemoveAll(destDir)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	cleanDest := filepath.Clean(destDir)
	if evalDest, err := filepath.EvalSymlinks(cleanDest); err == nil {
		cleanDest = filepath.Clean(evalDest)
	}

	var success bool
	defer func() {
		if !success {
			_ = os.RemoveAll(destDir)
		}
	}()

	// Zero-allocation streaming buffer reused across files
	buf := make([]byte, 32*1024)
	const (
		maxArchiveEntries    = 20000
		maxTotalArchiveBytes = 500 * 1024 * 1024 // 500MB
		maxSingleFileSize    = 100 * 1024 * 1024 // 100MB
	)
	var totalEntries int
	var totalBytes int64

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("archive read error: %w", err)
		}

		totalEntries++
		if totalEntries > maxArchiveEntries {
			return "", fmt.Errorf("archive exceeds entry limit of %d entries", maxArchiveEntries)
		}

		if hdr.Typeflag == tar.TypeXHeader || hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}

		cleanName := filepath.Clean(hdr.Name)
		if cleanName == "." || cleanName == "" {
			continue
		}

		// Zip Slip validation on raw archive entry name
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return "", ErrPathTraversal
		}

		// Check for forbidden setuid / setgid
		if hdr.Mode&06000 != 0 || hdr.FileInfo().Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			return "", fmt.Errorf("archive entry %s contains forbidden setuid/setgid bits", hdr.Name)
		}

		// Strip top-level directory segment if present (GitHub codeload tarballs wrap entries in repo-ref/)
		var relPath string
		if slashIdx := strings.Index(cleanName, "/"); slashIdx != -1 {
			relPath = cleanName[slashIdx+1:]
		} else {
			if hdr.Typeflag == tar.TypeDir {
				// Root directory entry itself
				continue
			}
			relPath = cleanName
		}

		if relPath == "" || relPath == "." {
			continue
		}

		target := filepath.Join(cleanDest, relPath)
		cleanTarget := filepath.Clean(target)

		// Strict Zip Slip validation against destination boundary
		if !strings.HasPrefix(cleanTarget, cleanDest+string(filepath.Separator)) && cleanTarget != cleanDest {
			return "", ErrPathTraversal
		}

		// Verify parent directory does not escape through a symlink chain
		parentDir := filepath.Dir(cleanTarget)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create directory %s: %w", parentDir, err)
		}
		evalParent, err := filepath.EvalSymlinks(parentDir)
		if err != nil {
			return "", fmt.Errorf("failed to evaluate parent directory symlinks: %w", err)
		}
		cleanEvalParent := filepath.Clean(evalParent)
		if !strings.HasPrefix(cleanEvalParent, cleanDest+string(filepath.Separator)) && cleanEvalParent != cleanDest {
			return "", fmt.Errorf("%w: target directory %s escapes destination through symlink", ErrPathTraversal, parentDir)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(cleanTarget, 0755); err != nil {
				return "", fmt.Errorf("failed to create directory %s: %w", cleanTarget, err)
			}
		case tar.TypeReg:
			if fi, err := os.Lstat(cleanTarget); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				_ = os.Remove(cleanTarget)
			}

			var perm os.FileMode = 0644
			if hdr.Mode&0111 != 0 || hdr.FileInfo().Mode()&0111 != 0 {
				perm = 0755
			}

			out, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
			if err != nil {
				return "", fmt.Errorf("failed to create file %s: %w", cleanTarget, err)
			}

			limitedReader := io.LimitReader(tr, maxSingleFileSize+1)
			n, err := io.CopyBuffer(out, limitedReader, buf)
			if n > maxSingleFileSize {
				_ = out.Close()
				return "", fmt.Errorf("file %s exceeds maximum single file size limit of 100MB", hdr.Name)
			}
			totalBytes += n
			if totalBytes > maxTotalArchiveBytes {
				_ = out.Close()
				return "", fmt.Errorf("archive exceeds total size limit of 500MB")
			}
			if err != nil {
				_ = out.Close()
				return "", fmt.Errorf("failed to write file %s: %w", cleanTarget, err)
			}

			if err := out.Close(); err != nil {
				return "", fmt.Errorf("failed to close file %s: %w", cleanTarget, err)
			}

			_ = os.Chmod(cleanTarget, perm)

		case tar.TypeSymlink:
			var resolvedLinkTarget string
			if filepath.IsAbs(hdr.Linkname) {
				resolvedLinkTarget = filepath.Clean(hdr.Linkname)
			} else {
				resolvedLinkTarget = filepath.Clean(filepath.Join(filepath.Dir(cleanTarget), hdr.Linkname))
			}

			if !strings.HasPrefix(resolvedLinkTarget, cleanDest+string(filepath.Separator)) && resolvedLinkTarget != cleanDest {
				return "", fmt.Errorf("%w: symlink %s targets %s outside destination %s", ErrPathTraversal, hdr.Name, hdr.Linkname, cleanDest)
			}

			_ = os.Remove(cleanTarget)
			if err := os.Symlink(hdr.Linkname, cleanTarget); err != nil {
				return "", fmt.Errorf("failed to create symlink %s -> %s: %w", cleanTarget, hdr.Linkname, err)
			}

		case tar.TypeLink:
			cleanLink := filepath.Clean(hdr.Linkname)
			if strings.HasPrefix(cleanLink, "..") || filepath.IsAbs(cleanLink) {
				return "", fmt.Errorf("%w: hard link %s targets outside destination", ErrPathTraversal, hdr.Name)
			}
			parts := strings.Split(cleanLink, string(filepath.Separator))
			if len(parts) > 1 {
				cleanLink = filepath.Join(parts[1:]...)
			}
			resolvedLinkTarget := filepath.Join(cleanDest, cleanLink)
			cleanResolvedLink := filepath.Clean(resolvedLinkTarget)

			if !strings.HasPrefix(cleanResolvedLink, cleanDest+string(filepath.Separator)) && cleanResolvedLink != cleanDest {
				return "", fmt.Errorf("%w: hard link %s targets outside destination", ErrPathTraversal, hdr.Name)
			}

			_ = os.Remove(cleanTarget)
			if err := os.Link(cleanResolvedLink, cleanTarget); err != nil {
				return "", fmt.Errorf("failed to create hard link %s -> %s: %w", cleanTarget, cleanResolvedLink, err)
			}

		default:
			if hdr.Typeflag == tar.TypeChar || hdr.Typeflag == tar.TypeBlock || hdr.Typeflag == tar.TypeFifo {
				return "", fmt.Errorf("unsupported special file type in archive: %v (%s)", hdr.Typeflag, hdr.Name)
			}
		}
	}

	if err := os.WriteFile(markerFile, []byte(time.Now().UTC().Format(time.RFC3339)), 0644); err != nil {
		return "", fmt.Errorf("failed to write completed marker: %w", err)
	}

	success = true
	return filepath.Abs(filepath.Join(destDir, subpath))
}
