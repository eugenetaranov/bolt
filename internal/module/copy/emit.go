package copy

import (
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/tackhq/tack/internal/connector"
	"github.com/tackhq/tack/internal/module"
)

// Emit produces shell script text for the copy module.
func (m *Module) Emit(params map[string]any, vars map[string]any) (*module.EmitResult, error) {
	dest, err := module.RequireString(params, "dest")
	if err != nil {
		return nil, err
	}

	src := module.GetString(params, "src", "")
	content := module.GetString(params, "content", "")
	mode := module.GetString(params, "mode", "0644")
	dirMode := module.GetString(params, "dir_mode", "0755")
	owner := module.GetString(params, "owner", "")
	group := module.GetString(params, "group", "")
	backup := module.GetBool(params, "backup", false)
	createDirs := module.GetBool(params, "create_dirs", false)
	deleteExtra := module.GetBool(params, "delete", false)

	if src == "" && content == "" {
		return nil, fmt.Errorf("either 'src' or 'content' parameter is required")
	}

	// Resolve content
	var fileContent []byte
	if src != "" {
		srcPath := module.ResolveRolePath(src, params, "files")
		info, err := os.Stat(srcPath)
		if err != nil {
			return nil, fmt.Errorf("stat source %q: %w", srcPath, err)
		}
		if info.IsDir() {
			return emitDir(src, srcPath, dest, mode, dirMode, owner, group, backup, deleteExtra)
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return nil, fmt.Errorf("reading source file %q: %w", srcPath, err)
		}
		fileContent = data
	} else {
		fileContent = []byte(content)
	}

	lines, warnings := emitFile(dest, fileContent, mode, owner, group, backup, createDirs)
	return &module.EmitResult{
		Supported: true,
		Shell:     strings.Join(lines, "\n"),
		Warnings:  warnings,
	}, nil
}

// emitFile produces the shell lines that deploy a single file's content to
// dest with the given attributes. When createDirs is true it first creates the
// destination's parent directory.
func emitFile(dest string, fileContent []byte, mode, owner, group string, backup, createDirs bool) (lines, warnings []string) {
	qdest := connector.ShellQuote(dest)
	tmpDest := dest + ".tack.tmp"
	qtmp := connector.ShellQuote(tmpDest)

	// Create parent directories
	if createDirs {
		lines = append(lines, fmt.Sprintf("mkdir -p %s", connector.ShellQuote(destDir(dest))))
	}

	// Write content via heredoc or base64
	if utf8.Valid(fileContent) && !containsHeredocDelim(string(fileContent)) {
		lines = append(lines, fmt.Sprintf("cat > %s <<'TACK_EOF'", qtmp))
		lines = append(lines, string(fileContent))
		lines = append(lines, "TACK_EOF")
	} else {
		// Binary or content with heredoc delimiter — use base64
		encoded := base64.StdEncoding.EncodeToString(fileContent)
		lines = append(lines, fmt.Sprintf("echo %s | base64 -d > %s", connector.ShellQuote(encoded), qtmp))
		if utf8.Valid(fileContent) {
			warnings = append(warnings, "content contains heredoc delimiter, using base64 encoding")
		}
	}

	// Diff-guard: only replace if content differs
	lines = append(lines, fmt.Sprintf("if ! diff -q %s %s >/dev/null 2>&1; then", qdest, qtmp))
	if backup {
		lines = append(lines, fmt.Sprintf("  [ -f %s ] && cp %s %s.bak", qdest, qdest, qdest))
	}
	lines = append(lines, fmt.Sprintf("  mv %s %s", qtmp, qdest))
	lines = append(lines, "  TACK_CHANGED=$((TACK_CHANGED+1))")
	lines = append(lines, "else")
	lines = append(lines, fmt.Sprintf("  rm -f %s", qtmp))
	lines = append(lines, "fi")

	lines = append(lines, attrLines(dest, mode, owner, group)...)
	return lines, warnings
}

// emitDir produces the shell lines that sync a local directory tree to the
// target: mkdir per directory, per-file deploy, and symlink recreation.
func emitDir(srcParam, srcPath, dest, mode, dirMode, owner, group string, backup, deleteExtra bool) (*module.EmitResult, error) {
	remoteRoot := syncRoot(srcParam, srcPath, dest)

	lines := []string{"mkdir -p " + connector.ShellQuote(remoteRoot)}
	lines = append(lines, attrLines(remoteRoot, dirMode, owner, group)...)
	var warnings []string

	walkErr := filepath.WalkDir(srcPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcPath, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		remote := path.Join(remoteRoot, filepath.ToSlash(rel))

		switch {
		case d.IsDir():
			lines = append(lines, "mkdir -p "+connector.ShellQuote(remote))
			lines = append(lines, attrLines(remote, dirMode, owner, group)...)

		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return fmt.Errorf("read symlink %q: %w", p, err)
			}
			lines = append(lines, fmt.Sprintf("ln -sfn %s %s", connector.ShellQuote(target), connector.ShellQuote(remote)))

		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("reading %q: %w", p, err)
			}
			fl, fw := emitFile(remote, data, mode, owner, group, backup, false)
			lines = append(lines, fl...)
			warnings = append(warnings, fw...)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	if deleteExtra {
		warnings = append(warnings, "delete: pruning is not supported in exported shell scripts")
	}

	return &module.EmitResult{
		Supported: true,
		Shell:     strings.Join(lines, "\n"),
		Warnings:  warnings,
	}, nil
}

// attrLines emits chmod/chown lines for a path.
func attrLines(dest, mode, owner, group string) []string {
	var lines []string
	qdest := connector.ShellQuote(dest)
	if mode != "" {
		lines = append(lines, fmt.Sprintf("chmod %s %s", module.NormalizeMode(mode), qdest))
	}
	if owner != "" || group != "" {
		ownership := owner
		if group != "" {
			ownership += ":" + group
		}
		lines = append(lines, fmt.Sprintf("chown %s %s", connector.ShellQuote(ownership), qdest))
	}
	return lines
}

func destDir(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return "/"
	}
	return p[:idx]
}

func containsHeredocDelim(s string) bool {
	return strings.Contains(s, "TACK_EOF")
}

var _ module.Emitter = (*Module)(nil)
