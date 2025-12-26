package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/containers/podman/v5/pkg/api/handlers"
	"github.com/containers/podman/v5/pkg/bindings"
	"github.com/containers/podman/v5/pkg/bindings/containers"
	"github.com/containers/podman/v5/pkg/bindings/images"
	"github.com/containers/podman/v5/pkg/specgen"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pelletier/go-toml/v2"
	"github.com/pterm/pterm"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
)

const (
	indexURL    = "https://raw.githubusercontent.com/vira-language/vira/main/repository/virus.json"
	depsDir     = ".virus_deps"
	projectTOML = "Project.toml"
	wolfiImage  = "cgr.dev/chainguard/wolfi-base:latest"
)

var binPath string

type Config struct {
	Package      Package           `toml:"package"`
	Dependencies map[string]string `toml:"dependencies"`
}

type Package struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

type LibraryIndex struct {
	Libraries []Library `json:"libraries"`
}

type Library struct {
	Name     string    `json:"name"`
	Versions []Version `json:"versions"`
}

type Version struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

func init() {
	osName := runtime.GOOS
	if osName == "linux" {
		binPath = "/usr/lib/vira-lang/bin"
	} else if osName == "windows" {
		programFiles := os.Getenv("ProgramFiles")
		if programFiles == "" {
			programFiles = "C:\\Program Files"
		}
		binPath = filepath.Join(programFiles, "ViraLang", "bin")
	} else {
		pterm.Fatal.Println("Unsupported OS")
		os.Exit(1)
	}
}

func main() {
	var rootCmd = &cobra.Command{
		Use:   "virus",
		Short: "Vira package manager inspired by Cargo",
	}

	var initCmd = &cobra.Command{
		Use:   "init",
		Short: "Initialize a new Vira project",
		Run: func(cmd *cobra.Command, args []string) {
			initProject()
		},
	}

	var addCmd = &cobra.Command{
		Use:   "add [library]",
		Short: "Add a dependency",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			addDependency(args[0])
		},
	}

	var compileCmd = &cobra.Command{
		Use:   "compile",
		Short: "Compile the project",
		Run: func(cmd *cobra.Command, args []string) {
			compileProject()
		},
	}

	rootCmd.AddCommand(initCmd, addCmd, compileCmd)
	if err := rootCmd.Execute(); err != nil {
		pterm.Error.Println(err)
		os.Exit(1)
	}
}

func initProject() {
	pterm.DefaultSection.Println("Initializing Vira project")
	config := Config{
		Package: Package{
			Name:    "myproject",
			Version: "0.1.0",
		},
		Dependencies: make(map[string]string),
	}
	data, err := toml.Marshal(config)
	if err != nil {
		pterm.Error.Println("Failed to marshal config:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(projectTOML, data, 0644); err != nil {
		pterm.Error.Println("Failed to write Project.toml:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll("src", 0755); err != nil {
		pterm.Error.Println("Failed to create src dir:", err)
		os.Exit(1)
	}
	mainCode := `int main() {
	return 0;
}
`
	if err := os.WriteFile("src/main.vira", []byte(mainCode), 0644); err != nil {
		pterm.Error.Println("Failed to write main.vira:", err)
		os.Exit(1)
	}
	pterm.Success.Println("Project initialized")
}

func addDependency(lib string) {
	pterm.DefaultSection.Println("Adding dependency:", lib)
	config, err := loadConfig()
	if err != nil {
		pterm.Error.Println(err)
		os.Exit(1)
	}
	config.Dependencies[lib] = "*"
	if err := saveConfig(config); err != nil {
		pterm.Error.Println(err)
		os.Exit(1)
	}
	pterm.Success.Println("Dependency added")
}

func compileProject() {
	pterm.DefaultSection.Println("Compiling Vira project")
	config, err := loadConfig()
	if err != nil {
		pterm.Error.Println(err)
		os.Exit(1)
	}
	tempDir, err := os.MkdirTemp("", "virus-build-*")
	if err != nil {
		pterm.Error.Println("Failed to create temp dir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)
	projectFile := findProjectFile()
	if projectFile == "" {
		pterm.Error.Println("No project file found")
		os.Exit(1)
	}
	if err := copyFile(projectFile, filepath.Join(tempDir, filepath.Base(projectFile))); err != nil {
		pterm.Error.Println("Failed to copy project file:", err)
		os.Exit(1)
	}
	if err := copyDir("src", filepath.Join(tempDir, "src")); err != nil {
		pterm.Error.Println("Failed to copy src dir:", err)
		os.Exit(1)
	}
	depsDirTemp := filepath.Join(tempDir, depsDir)
	if err := os.MkdirAll(depsDirTemp, 0755); err != nil {
		pterm.Error.Println("Failed to create deps dir:", err)
		os.Exit(1)
	}
	index, err := downloadIndex()
	if err != nil {
		pterm.Error.Println("Failed to download index:", err)
		os.Exit(1)
	}
	depPaths := []string{}
	objectFilesContainer := []string{}
	ctx := context.Background()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	socketPath := fmt.Sprintf("unix://%s/podman/podman.sock", runtimeDir)
	conn, err := bindings.NewConnection(ctx, socketPath)
	if err != nil {
		pterm.Error.Println("Failed to connect to Podman:", err)
		os.Exit(1)
	}
	_, err = images.Pull(conn, wolfiImage, nil)
	if err != nil {
		pterm.Error.Println("Failed to pull Wolfi image:", err)
		os.Exit(1)
	}
	s := specgen.NewSpecGenerator(wolfiImage, false)
	s.Mounts = []specs.Mount{
		{Type: "bind", Source: tempDir, Destination: "/work", Options: []string{"rw"}},
		{Type: "bind", Source: binPath, Destination: "/vira-bin", Options: []string{"ro"}},
	}
	s.WorkDir = "/work"
	s.Command = []string{"/bin/sh", "-c", "while true; do sleep 100000; done"}
	created, err := containers.CreateWithSpec(conn, s, nil)
	if err != nil {
		pterm.Error.Println("Failed to create container:", err)
		os.Exit(1)
	}
	containerID := created.ID
	if err := containers.Start(conn, containerID, nil); err != nil {
		pterm.Error.Println("Failed to start container:", err)
		os.Exit(1)
	}
	defer func() {
		containers.Stop(conn, containerID, nil)
		containers.Remove(conn, containerID, nil)
	}()
	out, exit, err := execInContainer(conn, containerID, []string{"apk", "update"})
	if err != nil || exit != 0 {
		pterm.Error.Println("apk update failed:", out, err)
		os.Exit(1)
	}
	out, exit, err = execInContainer(conn, containerID, []string{"apk", "add", "--no-cache", "build-base", "gcc", "g++"})
	if err != nil || exit != 0 {
		pterm.Error.Println("apk add failed:", out, err)
		os.Exit(1)
	}
	for name, versionSpec := range config.Dependencies {
		lib := findLibrary(index, name)
		if lib == nil {
			pterm.Error.Println("Library not found:", name)
			os.Exit(1)
		}
		version := resolveVersion(lib.Versions, versionSpec)
		if version == nil {
			pterm.Error.Println("No matching version for", name, versionSpec)
			os.Exit(1)
		}
		depPath := filepath.Join(depsDirTemp, name, version.Version)
		if err := os.MkdirAll(depPath, 0755); err != nil {
			pterm.Error.Println("Failed to create dep path:", err)
			os.Exit(1)
		}
		fileName := filepath.Base(version.URL)
		targetFile := filepath.Join(depPath, fileName)
		if _, err := os.Stat(targetFile); os.IsNotExist(err) {
			pterm.Info.Println("Downloading", name, version.Version)
			if err := downloadWithProgress(version.URL, targetFile); err != nil {
				pterm.Error.Println("Failed to download:", err)
				os.Exit(1)
			}
		}
		depPaths = append(depPaths, depPath)
		ext := strings.ToLower(filepath.Ext(fileName))
		containerInput := strings.Replace(targetFile, tempDir, "/work", 1)
		containerO := strings.Replace(depPath, tempDir, "/work", 1) + "/lib.o"
		if ext == ".vira" || ext == ".c" || ext == ".cpp" {
			if err := compileSourceInContainer(ctx, containerID, containerInput, containerO, []string{}, ext, tempDir); err != nil {
				os.Exit(1)
			}
			objectFilesContainer = append(objectFilesContainer, containerO)
		}
	}
	containerInput := "/work/src/main.vira"
	containerMainO := "/work/main.o"
	includeFlagsContainer := []string{}
	for _, depPath := range depPaths {
		containerDepPath := strings.Replace(depPath, tempDir, "/work", 1)
		includeFlagsContainer = append(includeFlagsContainer, "-I"+containerDepPath)
	}
	if err := compileSourceInContainer(ctx, containerID, containerInput, containerMainO, includeFlagsContainer, ".vira", tempDir); err != nil {
		os.Exit(1)
	}
	objectFilesContainer = append(objectFilesContainer, containerMainO)
	outputExeContainer := "/work/bin/" + config.Package.Name
	// TODO: windows support
	cmdLink := append([]string{"gcc"}, objectFilesContainer...)
	cmdLink = append(cmdLink, "-o", outputExeContainer)
	out, exit, err = execInContainer(conn, containerID, cmdLink)
	if err != nil || exit != 0 {
		pterm.Error.Println("Linking failed:", out)
		os.Exit(1)
	}
	pterm.Success.Println("Linking done")
	localBinDir := "bin"
	if err := os.MkdirAll(localBinDir, 0755); err != nil {
		pterm.Error.Println(err)
		os.Exit(1)
	}
	localExe := filepath.Join(localBinDir, config.Package.Name)
	if runtime.GOOS == "windows" {
		localExe += ".exe"
	}
	if err := copyFile(filepath.Join(tempDir, "bin", config.Package.Name), localExe); err != nil {
		pterm.Error.Println("Failed to copy executable:", err)
		os.Exit(1)
	}
	pterm.Success.Println("Compilation complete")
}

func compileSourceInContainer(conn context.Context, containerID, input, output string, includeFlags []string, ext string, tempDir string) error {
	pterm.DefaultSection.Println("Compiling source:", input)
	if ext == ".vira" {
		preOut := input + ".pre"
		cmdPre := append([]string{"preprocessor"}, includeFlags...)
		cmdPre = append(cmdPre, input, preOut)
		out, exit, err := execInContainer(conn, containerID, cmdPre)
		if err != nil || exit != 0 {
			handleError(strings.Replace(input, "/work", tempDir, 1), out)
			return fmt.Errorf("preprocess failed: %s", out)
		}
		pterm.Success.Println("Preprocessing done")
		cmdPlsa := []string{"plsa", preOut}
		out, exit, err = execInContainer(conn, containerID, cmdPlsa)
		if err != nil || exit != 0 {
			handleError(strings.Replace(preOut, "/work", tempDir, 1), out)
			return fmt.Errorf("plsa failed: %s", out)
		}
		pterm.Success.Println("PLSA done")
		cmdComp := []string{"compiler", preOut, output}
		out, exit, err = execInContainer(conn, containerID, cmdComp)
		if err != nil || exit != 0 {
			handleError(strings.Replace(preOut, "/work", tempDir, 1), out)
			return fmt.Errorf("compile failed: %s", out)
		}
		pterm.Success.Println("Compilation done")
	} else if ext == ".c" {
		cmd := append([]string{"gcc", "-c"}, includeFlags...)
		cmd = append(cmd, input, "-o", output)
		out, exit, err := execInContainer(conn, containerID, cmd)
		if err != nil || exit != 0 {
			handleError(strings.Replace(input, "/work", tempDir, 1), out)
			return fmt.Errorf("gcc failed: %s", out)
		}
		pterm.Success.Println("GCC compilation done")
	} else if ext == ".cpp" {
		cmd := append([]string{"g++", "-c"}, includeFlags...)
		cmd = append(cmd, input, "-o", output)
		out, exit, err := execInContainer(conn, containerID, cmd)
		if err != nil || exit != 0 {
			handleError(strings.Replace(input, "/work", tempDir, 1), out)
			return fmt.Errorf("g++ failed: %s", out)
		}
		pterm.Success.Println("G++ compilation done")
	}
	return nil
}

func execInContainer(conn context.Context, containerID string, cmd []string) (string, int, error) {
	execConfig := new(handlers.ExecCreateConfig)
	execConfig.Cmd = cmd
	execConfig.Env = []string{"PATH=/vira-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	execConfig.User = ""
	execConfig.WorkingDir = "/work"
	execConfig.AttachStderr = true
	execConfig.AttachStdout = true
	execConfig.AttachStdin = false
	execConfig.Tty = false
	execID, err := containers.ExecCreate(conn, containerID, execConfig)
	if err != nil {
		return "", -1, err
	}
	var buf bytes.Buffer
	var outputStream io.Writer = &buf
	var errorStream io.Writer = &buf
	attachOpt := containers.ExecStartAndAttachOptions{
		OutputStream: &outputStream,
		ErrorStream:  &errorStream,
		InputStream:  nil,
	}
	err = containers.ExecStartAndAttach(conn, execID, &attachOpt)
	if err != nil {
		return buf.String(), -1, err
	}
	inspect, err := containers.ExecInspect(conn, execID, nil)
	if err != nil {
		return buf.String(), -1, err
	}
	return buf.String(), inspect.ExitCode, nil
}

func findProjectFile() string {
	if _, err := os.Stat(projectTOML); err == nil {
		return projectTOML
	}
	return ""
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func copyDir(srcDir, dstDir string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dstDir, relPath)
		if info.IsDir() {
			return os.Mkdir(dstPath, info.Mode())
		}
		return copyFile(path, dstPath)
	})
}

func loadConfig() (Config, error) {
	var config Config
	projectFile := findProjectFile()
	if projectFile == "" {
		return config, fmt.Errorf("no project file found")
	}
	data, err := os.ReadFile(projectFile)
	if err != nil {
		return config, err
	}
	err = toml.Unmarshal(data, &config)
	return config, err
}

func saveConfig(config Config) error {
	data, err := toml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(projectTOML, data, 0644)
}

func downloadIndex() (LibraryIndex, error) {
	var index LibraryIndex
	resp, err := http.Get(indexURL)
	if err != nil {
		return index, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return index, err
	}
	err = json.Unmarshal(data, &index)
	return index, err
}

func findLibrary(index LibraryIndex, name string) *Library {
	for _, lib := range index.Libraries {
		if lib.Name == name {
			return &lib
		}
	}
	return nil
}

func resolveVersion(versions []Version, spec string) *Version {
	if spec == "*" {
		if len(versions) > 0 {
			return &versions[len(versions)-1]
		}
		return nil
	}
	if strings.HasPrefix(spec, "^") {
		prefix := spec[1:]
		for i := len(versions) - 1; i >= 0; i-- {
			if strings.HasPrefix(versions[i].Version, prefix) {
				return &versions[i]
			}
		}
		return nil
	}
	for i := len(versions) - 1; i >= 0; i-- {
		if versions[i].Version == spec {
			return &versions[i]
		}
	}
	return nil
}

func downloadWithProgress(url, target string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer f.Close()
	bar := progressbar.NewOptions64(
		resp.ContentLength,
		progressbar.OptionSetDescription("Downloading"),
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionSetWidth(30),
		progressbar.OptionThrottle(0),
		progressbar.OptionShowCount(),
		progressbar.OptionShowIts(),
		progressbar.OptionOnCompletion(func() {
			fmt.Fprint(os.Stderr, "\n")
		}),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionFullWidth(),
		progressbar.OptionSetRenderBlankState(true),
		progressbar.OptionUseANSICodes(true),
	)
	bar.RenderBlank()
	_, err = io.Copy(io.MultiWriter(f, bar), resp.Body)
	return err
}

func handleError(sourceFile, errorMsg string) {
	pterm.Error.Println("Error occurred. Running diagnostic...")
	lines := strings.Split(errorMsg, "\n")
	var message string
	line := 1
	column := 1
	if len(lines) > 0 {
		message = lines[0]
		if strings.Contains(message, "line") {
			fmt.Sscanf(message, "line %d, column %d", &line, &column)
		}
	}
	diagnostic := filepath.Join(binPath, "diagnostic")
	if runtime.GOOS == "windows" {
		diagnostic += ".exe"
	}
	cmdDiag := exec.Command(diagnostic,
		"--source", sourceFile,
		"--message", message,
		"--line", fmt.Sprintf("%d", line),
		"--column", fmt.Sprintf("%d", column),
	)
	out, err := cmdDiag.CombinedOutput()
	if err != nil {
		pterm.Error.Println(string(out))
	} else {
		pterm.Info.Println(string(out))
	}
}
