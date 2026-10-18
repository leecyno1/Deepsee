package architecture

import (
	"bufio"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReleaseAndSourceAreMacOSOnly(t *testing.T) {
	root := repositoryRoot(t)
	unsupportedOS := "win" + "dows"
	for _, path := range []string{
		filepath.Join(root, "internal", "wechat", "decrypt", unsupportedOS),
		filepath.Join(root, "internal", "wechat", "key", unsupportedOS),
		filepath.Join(root, "internal", "wechat", "process", unsupportedOS),
		filepath.Join(root, "pkg", "appver", "version_"+unsupportedOS+".go"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unsupported platform implementation remains: %s", path)
		}
	}

	for _, path := range []string{
		filepath.Join(root, ".goreleaser.yaml"),
		filepath.Join(root, ".github", "workflows", "release.yml"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(content)), unsupportedOS) {
			t.Fatalf("unsupported platform release target remains in %s", path)
		}
	}
}

func TestCompositionBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	servicesRoot := filepath.Join(root, "internal", "chatlog")
	compositionFile := filepath.Join(servicesRoot, "modules", "modules.go")
	forbiddenConstructors := []string{
		"wechat.NewService(",
		"database.NewService(",
		"http.NewService(",
		"messagehook.New(",
	}

	err := filepath.WalkDir(servicesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(content)
		if filepath.Clean(path) != filepath.Clean(compositionFile) {
			for _, constructor := range forbiddenConstructors {
				if strings.Contains(text, constructor) {
					t.Errorf("service construction escaped composition root: %s contains %s", path, constructor)
				}
			}
		}
		if strings.Contains(text, "runtime.GOOS") {
			t.Errorf("runtime platform selection remains in application service: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	managerSource, err := os.ReadFile(filepath.Join(root, "internal", "wechat", "manager.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(managerSource), "DefaultManager") {
		t.Fatal("global WeChat manager returned")
	}
}

func TestLayerDependencyDirection(t *testing.T) {
	root := repositoryRoot(t)
	rules := map[string][]string{
		"internal/chatlog/ports": {
			"/internal/chatlog/http",
			"/internal/chatlog/database",
			"/internal/chatlog/modules",
			"/internal/chatlog/usecase",
			"/internal/chatlog/adapters",
			"/internal/wechatdb",
		},
		"internal/chatlog/usecase": {
			"/internal/chatlog/http",
			"/internal/chatlog/database",
			"/internal/chatlog/modules",
			"/internal/chatlog/adapters",
			"/internal/wechatdb",
			"/internal/wechat/key/darwin",
		},
		"internal/chatlog/http": {
			"/internal/chatlog/database",
			"/internal/chatlog/messagehook",
			"/internal/chatlog/modules",
			"/internal/chatlog/usecase",
			"/internal/chatlog/adapters",
			"/internal/chatlog/wechat",
			"/internal/wechatdb",
			"/pkg/util/dat2img",
		},
		"internal/chatlog/database": {
			"/internal/chatlog/http",
			"/internal/chatlog/messagehook",
			"/internal/chatlog/modules",
			"/internal/chatlog/usecase",
			"/internal/chatlog/adapters",
		},
	}
	for relativeDir, forbidden := range rules {
		dir := filepath.Join(root, filepath.FromSlash(relativeDir))
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			for _, spec := range file.Imports {
				importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
				if unquoteErr != nil {
					return unquoteErr
				}
				for _, fragment := range forbidden {
					if strings.Contains(importPath, fragment) {
						t.Errorf("%s imports forbidden dependency %s", path, importPath)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcreteAdaptersStayBehindCompositionRoot(t *testing.T) {
	root := repositoryRoot(t)
	applicationRoot := filepath.Join(root, "internal", "chatlog")
	allowedRoots := []string{
		filepath.Join(applicationRoot, "adapters") + string(filepath.Separator),
		filepath.Join(applicationRoot, "modules") + string(filepath.Separator),
	}
	forbidden := []string{
		"/internal/chatlog/adapters/",
		"/internal/wechat/backend/darwin",
		"/internal/wechat/key/darwin",
		"/internal/wechatdb/datasource/wcdb",
		"/pkg/util/dat2img",
	}
	err := filepath.WalkDir(applicationRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, allowed := range allowedRoots {
			if strings.HasPrefix(path, allowed) {
				return nil
			}
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		for _, spec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			for _, fragment := range forbidden {
				if strings.Contains(importPath, fragment) {
					t.Errorf("%s imports concrete adapter %s", path, importPath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProductionGoFilesStayFocused(t *testing.T) {
	root := repositoryRoot(t)
	const maxLines = 800
	for _, relativeDir := range []string{"cmd", "internal", "pkg"} {
		dir := filepath.Join(root, relativeDir)
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			lines := 0
			for scanner.Scan() {
				lines++
			}
			if scanErr := scanner.Err(); scanErr != nil {
				return scanErr
			}
			if lines > maxLines {
				t.Errorf("%s has %d lines; split focused modules before exceeding %d", path, lines, maxLines)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestExtractionContextIsTypeSafe(t *testing.T) {
	root := repositoryRoot(t)
	allowed := filepath.Join(root, "internal", "wechat", "key", "request.go")
	for _, relativeDir := range []string{"internal/chatlog", "internal/wechat"} {
		dir := filepath.Join(root, relativeDir)
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || filepath.Clean(path) == filepath.Clean(allowed) {
				return nil
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(content), "context.WithValue") {
				t.Errorf("request metadata must use key.WithRequest: %s", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
