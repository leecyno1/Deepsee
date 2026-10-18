package buildprofile

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const modulePath = "github.com/sjzar/chatlog"

// EnsureGoRun delegates an incomplete temporary `go run .` executable to the
// same verified bin/chatlog used by `make run`. A regular packaged executable
// is left untouched; official build entry points already enforce the profile.
func EnsureGoRun(args []string) error {
	executable, err := os.Executable()
	if Complete {
		return nil
	}
	if err != nil || !isGoRunTemporaryExecutable(executable) {
		return nil
	}

	root, err := sourceRoot()
	if err != nil {
		return err
	}
	makePath, err := exec.LookPath("make")
	if err != nil {
		return fmt.Errorf("locate make for complete go run profile: %w", err)
	}

	fmt.Fprintln(os.Stderr, "检测到普通 go run，正在加载与 make run 一致的完整构建配置…")
	build := exec.Command(makePath, "build")
	build.Dir = root
	build.Stdin = os.Stdin
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build complete chatlog binary: %w", err)
	}

	target := filepath.Join(root, "bin", "chatlog")
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		if err == nil {
			err = fmt.Errorf("target is a directory")
		}
		return fmt.Errorf("locate complete chatlog binary %s: %w", target, err)
	}
	argv := []string{target}
	if len(args) > 1 {
		argv = append(argv, args[1:]...)
	}
	return syscall.Exec(target, argv, os.Environ())
}

func isGoRunTemporaryExecutable(path string) bool {
	normalized := filepath.ToSlash(filepath.Clean(path))
	for _, part := range strings.Split(normalized, "/") {
		// Go 1.25 and earlier normally execute from a go-buildNNN temp
		// directory. Newer toolchains may execute a cached go-run binary from
		// ~/Library/Caches/go-build instead, so both forms use this component
		// test rather than relying on an /exe/ suffix.
		if part == "go-build" || isNumberedGoBuildDir(part) {
			return true
		}
	}
	return false
}

func isNumberedGoBuildDir(name string) bool {
	const prefix = "go-build"
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return false
	}
	for _, char := range name[len(prefix):] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func sourceRoot() (string, error) {
	candidates := make([]string, 0, 3)
	if override := strings.TrimSpace(os.Getenv("CHATLOG_SOURCE_ROOT")); override != "" {
		candidates = append(candidates, override)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if _, filename, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Dir(filename))
	}
	for _, candidate := range candidates {
		if root := findModuleRoot(candidate); root != "" {
			return root, nil
		}
	}
	return "", fmt.Errorf("locate %s source root for go run delegation", modulePath)
}

func findModuleRoot(start string) string {
	start, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for current := filepath.Clean(start); ; current = filepath.Dir(current) {
		moduleFile := filepath.Join(current, "go.mod")
		raw, err := os.ReadFile(moduleFile)
		if err == nil && strings.Contains(string(raw), "module "+modulePath) {
			if info, scriptErr := os.Stat(filepath.Join(current, "scripts", "build_chatlog_binary.sh")); scriptErr == nil && !info.IsDir() {
				return current
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
	}
}
