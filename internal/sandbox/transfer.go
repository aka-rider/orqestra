package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

// Session encapsulates a named sandbox session for file staging and extraction.
type Session struct {
	Name      string
	StartedAt time.Time
}

// sessionDir returns the in-container directory for this session's files.
func (s Session) sessionDir() string {
	return "/workspace/.orqestra/" + s.Name
}

// CopyToContainer wraps content in a tar archive and copies it to the specified
// path inside the container using the Docker SDK.
func CopyToContainer(ctx context.Context, cli *dockerclient.Client, containerID, containerPath string, content io.Reader) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return fmt.Errorf("reading content for copy: %w", err)
	}

	dir := filepath.Dir(containerPath)
	base := filepath.Base(containerPath)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:    base,
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing tar header: %w", err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("writing tar content: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar writer: %w", err)
	}

	err = cli.CopyToContainer(ctx, containerID, dir, &buf, container.CopyToContainerOptions{})
	if err != nil {
		return fmt.Errorf("copy to container %s: %w", containerPath, err)
	}
	return nil
}

// CopyDirToContainer tars a host directory and copies it to the specified container path.
// Symlinks that escape the source directory are skipped to prevent host escapes.
func CopyDirToContainer(ctx context.Context, cli *dockerclient.Client, containerID, containerPath, hostDirPath string) error {
	hostDirPath, err := filepath.Abs(hostDirPath)
	if err != nil {
		return fmt.Errorf("resolving host dir path: %w", err)
	}

	// Docker's CopyToContainer extracts the tar into an existing directory.
	// We copy to the parent and prefix all tar entries with the target basename.
	parentDir := filepath.Dir(containerPath)
	baseName := filepath.Base(containerPath)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	err = filepath.Walk(hostDirPath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		// Resolve symlinks and skip any that escape the source directory.
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				slog.Debug("sandbox: skipping unresolvable symlink", "path", path)
				return nil
			}
			if !strings.HasPrefix(resolved, hostDirPath) {
				slog.Debug("sandbox: skipping escaping symlink", "path", path, "target", resolved)
				return nil
			}
		}

		relPath, err := filepath.Rel(hostDirPath, path)
		if err != nil {
			return err
		}

		// Prefix all entries with the target directory basename.
		var entryName string
		if relPath == "." {
			entryName = baseName + "/"
		} else {
			entryName = baseName + "/" + relPath
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("creating tar header for %s: %w", relPath, err)
		}
		hdr.Name = entryName

		// Normalize permissions.
		if info.IsDir() {
			hdr.Mode = 0o755
		} else {
			hdr.Mode = 0o644
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return fmt.Errorf("creating tar from %s: %w", hostDirPath, err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar writer: %w", err)
	}

	err = cli.CopyToContainer(ctx, containerID, parentDir, &buf, container.CopyToContainerOptions{})
	if err != nil {
		return fmt.Errorf("copy dir to container %s: %w", containerPath, err)
	}
	return nil
}

// CopyFromContainer retrieves a file or directory from the container as a tar stream.
// The caller is responsible for closing the returned ReadCloser.
func CopyFromContainer(ctx context.Context, cli *dockerclient.Client, containerID, containerPath string) (io.ReadCloser, error) {
	reader, _, err := cli.CopyFromContainer(ctx, containerID, containerPath)
	if err != nil {
		return nil, fmt.Errorf("copy from container %s: %w", containerPath, err)
	}
	return reader, nil
}

// CopyFileFromContainer extracts a single file from the container to a host path.
// Includes Zip Slip protection to prevent path traversal attacks via malicious tar headers.
func CopyFileFromContainer(ctx context.Context, cli *dockerclient.Client, containerID, containerPath, hostPath string) error {
	reader, err := CopyFromContainer(ctx, cli, containerID, containerPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	// Ensure parent directory exists.
	dir := filepath.Dir(hostPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	tr := tar.NewReader(reader)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("file not found in tar archive from %s", containerPath)
		}
		if err != nil {
			return fmt.Errorf("reading tar from container: %w", err)
		}

		// Zip Slip protection: sanitize the path in the tar header.
		cleanName := filepath.Clean(hdr.Name)
		if strings.Contains(cleanName, "..") {
			return fmt.Errorf("zip slip detected in tar entry: %q", hdr.Name)
		}

		// Skip directories — we only want the file.
		if hdr.Typeflag == tar.TypeDir {
			continue
		}

		// Write the file to the host path.
		mode := os.FileMode(hdr.Mode) & 0o777
		if mode == 0 {
			mode = 0o644
		}
		f, err := os.OpenFile(hostPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return fmt.Errorf("creating file %s: %w", hostPath, err)
		}
		// Cap copy at 1GB to prevent unbounded reads.
		_, copyErr := io.Copy(f, io.LimitReader(tr, 1<<30))
		closeErr := f.Close()
		if copyErr != nil {
			return fmt.Errorf("writing file %s: %w", hostPath, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing file %s: %w", hostPath, closeErr)
		}
		return nil
	}
}

// StageInputs creates the session directory inside the container and copies
// input.md and system-prompt.md into it.
func (d *DockerSandbox) StageInputs(ctx context.Context, sess Session, inputMD, systemPromptMD string) error {
	if err := d.ensureClient(); err != nil {
		return err
	}

	sessDir := sess.sessionDir()

	// Create the session directory inside the container.
	exitCode, err := d.execInternal(ctx, []string{"mkdir", "-p", sessDir})
	if err != nil {
		return fmt.Errorf("creating session dir %s: %w", sessDir, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("creating session dir %s: exit code %d", sessDir, exitCode)
	}

	// Copy input.md.
	inputPath := sessDir + "/input.md"
	if err := CopyToContainer(ctx, d.cli, d.containerID, inputPath, strings.NewReader(inputMD)); err != nil {
		return fmt.Errorf("staging input.md: %w", err)
	}

	// Copy system-prompt.md.
	promptPath := sessDir + "/system-prompt.md"
	if err := CopyToContainer(ctx, d.cli, d.containerID, promptPath, strings.NewReader(systemPromptMD)); err != nil {
		return fmt.Errorf("staging system-prompt.md: %w", err)
	}

	slog.Debug("sandbox: staged inputs", "session", sess.Name, "dir", sessDir)
	return nil
}

// ExtractArtifact reads the output.md file from the session directory inside the container.
// Returns the raw bytes (capped at 10MB) or a detailed error if the file doesn't exist.
func (d *DockerSandbox) ExtractArtifact(ctx context.Context, sess Session) ([]byte, error) {
	if err := d.ensureClient(); err != nil {
		return nil, err
	}

	outputPath := sess.sessionDir() + "/output.md"

	reader, err := CopyFromContainer(ctx, d.cli, d.containerID, outputPath)
	if err != nil {
		// Provide debugging info: list actual session directory contents.
		detail := d.listSessionDir(ctx, sess)
		return nil, fmt.Errorf("extracting artifact %s: %w\nSession dir contents: %s", outputPath, err, detail)
	}
	defer reader.Close()

	tr := tar.NewReader(reader)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			detail := d.listSessionDir(ctx, sess)
			return nil, fmt.Errorf("output.md not found in tar from %s\nSession dir contents: %s", outputPath, detail)
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar for artifact: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		// Cap at 10MB.
		const maxSize = 10 * 1024 * 1024
		data, err := io.ReadAll(io.LimitReader(tr, maxSize))
		if err != nil {
			return nil, fmt.Errorf("reading artifact content: %w", err)
		}
		return data, nil
	}
}

// listSessionDir returns a string listing the contents of the session directory
// for debugging purposes. Returns an empty string if listing fails.
func (d *DockerSandbox) listSessionDir(ctx context.Context, sess Session) string {
	var out bytes.Buffer
	exitCode, err := d.execInternalWithOutput(ctx, []string{"ls", "-la", sess.sessionDir()}, &out)
	if err != nil || exitCode != 0 {
		return "(unable to list session dir)"
	}
	return out.String()
}
