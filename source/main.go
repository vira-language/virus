package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pterm/pterm"
	"github.com/schollz/progressbar/v3"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	indexURL      = "https://raw.githubusercontent.com/vira-language/vira/main/repository/virus.json"
	depsDir       = ".virus_deps"
	projectTOML   = "Project.toml"
	projectYAML   = "Project.yaml"
	projectJSON   = "Project.json"
)

var binPath string

type Config struct {
	Package     Package     `toml:"package" yaml:"package" json:"package"`
	Dependencies map[string]string `toml:"dependencies" yaml:"dependencies" json:"dependencies"`
}

type Package struct {
	Name    string `toml:"name" yaml:"name" json:"name"`
	Version string `toml:"version" yaml:"version" json:"version"`
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

	config.Dependencies[lib] = "*" // Default to latest

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

	index, err := downloadIndex()
	if err != nil {
		pterm.Error.Println("Failed to download index:", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(depsDir, 0755); err != nil {
		pterm.Error.Println("Failed to create deps dir:", err)
		os.Exit(1)
	}

	includePaths := []string{}
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

		depPath := filepath.Join(depsDir, name, version.Version)
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

		includePaths = append(includePaths, depPath)
	}

	// Assume main file is src/main.vira
	inputFile := "src/main.vira"
	outputPre := inputFile + ".pre"
	outputObj := inputFile + ".o"

	// For isolation, simulate with chdir or env, but for now local
	// TODO: Implement wolfi-like isolation, perhaps using os.Chdir to a temp dir with mounts

	pterm.DefaultSection.Println("Preprocessing")
	preprocessor := filepath.Join(binPath, "preprocessor")
	if runtime.GOOS == "windows" {
		preprocessor += ".exe"
	}
	cmdPreArgs := []string{inputFile, outputPre}
	for _, path := range includePaths {
		cmdPreArgs = append(cmdPreArgs, "-I", path) // Assume preprocessor supports -I
	}
	cmdPre := exec.Command(preprocessor, cmdPreArgs...)
	if out, err := cmdPre.CombinedOutput(); err != nil {
		handleError(inputFile, string(out))
		os.Exit(1)
	}
	pterm.Success.Println("Preprocessing done")

	pterm.DefaultSection.Println("Parsing and Checking")
	plsa := filepath.Join(binPath, "plsa")
	if runtime.GOOS == "windows" {
		plsa += ".exe"
	}
	cmdPlsa := exec.Command(plsa, outputPre)
	if out, err := cmdPlsa.CombinedOutput(); err != nil {
		handleError(outputPre, string(out))
		os.Exit(1)
	}
	pterm.Success.Println("PLSA done")

	pterm.DefaultSection.Println("Compiling")
	compiler := filepath.Join(binPath, "compiler")
	if runtime.GOOS == "windows" {
		compiler += ".exe"
	}
	cmdComp := exec.Command(compiler, outputPre, outputObj)
	if out, err := cmdComp.CombinedOutput(); err != nil {
		handleError(outputPre, string(out))
		os.Exit(1)
	}
	pterm.Success.Println("Compilation done")

	// Linking similar to virac
	pterm.DefaultSection.Println("Linking")
	linker := "gcc"
	outputExe := "bin/" + config.Package.Name
	if runtime.GOOS == "windows" {
		linker = "link.exe"
		outputExe += ".exe"
		cmdLink := exec.Command(linker, "/OUT:"+outputExe, outputObj)
		if out, err := cmdLink.CombinedOutput(); err != nil {
			pterm.Error.Println(string(out))
			os.Exit(1)
		}
	} else {
		if err := os.MkdirAll("bin", 0755); err != nil {
			pterm.Error.Println(err)
			os.Exit(1)
		}
		cmdLink := exec.Command(linker, outputObj, "-o", outputExe)
		if out, err := cmdLink.CombinedOutput(); err != nil {
			pterm.Error.Println(string(out))
			os.Exit(1)
		}
	}
	pterm.Success.Println("Linking done")
}

func loadConfig() (Config, error) {
	var config Config
	var data []byte
	var err error

	if _, err = os.Stat(projectTOML); err == nil {
		data, err = os.ReadFile(projectTOML)
		if err != nil {
			return config, err
		}
		err = toml.Unmarshal(data, &config)
	} else if _, err = os.Stat(projectYAML); err == nil {
		data, err = os.ReadFile(projectYAML)
		if err != nil {
			return config, err
		}
		err = yaml.Unmarshal(data, &config)
	} else if _, err = os.Stat(projectJSON); err == nil {
		data, err = os.ReadFile(projectJSON)
		if err != nil {
			return config, err
		}
		err = json.Unmarshal(data, &config)
	} else {
		return config, fmt.Errorf("no project file found")
	}

	if err != nil {
		return config, err
	}

	return config, nil
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
	// Simple resolution: if "*", take latest; if exact, match; if ^, semver prefix
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

	// Mock parsing error for line, column, message
	lines := strings.Split(errorMsg, "\n")
	var message string
	line := 1
	column := 1
	if len(lines) > 0 {
		message = lines[0]
		// Parse if format like "line X, column Y: msg"
		if strings.Contains(message, "line") {
			// Simple parse
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
