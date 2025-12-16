package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/pterm/pterm"
	"github.com/schollz/progressbar/v3"
)

var systemBinDir string
var libDir string
var binDir string
var libsDir string
var exe string
var tmpDir string

func init() {
	if runtime.GOOS == "windows" {
		exe = ".exe"
		systemBinDir = `C:\\Program Files\\Vira\\bin`
		libDir = `C:\\Program Files\\Vira\\lib`
		binDir = libDir + `\\bin`
		libsDir = libDir + `\\libs`
		tmpDir = os.Getenv("TEMP")
	} else {
		exe = ""
		systemBinDir = "/usr/bin"
		libDir = "/usr/lib/vira-lang"
		binDir = libDir + "/bin"
		libsDir = libDir + "/libs"
		tmpDir = "/tmp"
	}
}

type Library struct {
	Name     string    `json:"name"`
	Versions []Version `json:"versions"`
}

type Version struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

type Index struct {
	Libraries []Library `json:"libraries"`
}

func main() {
	if len(os.Args) < 2 {
		pterm.DefaultHeader.WithFullWidth().Println("Virus - Vira Library Manager")
		pterm.Info.Println("Usage:")
		pterm.Info.Println("  virus install <lib> [version]  - Install library (default: latest)")
		pterm.Info.Println("  virus list                     - List installed libraries")
		pterm.Info.Println("  virus remove <lib>             - Remove library")
		return
	}

	cmd := os.Args[1]
	switch cmd {
	case "install":
		if len(os.Args) < 3 {
			pterm.Error.Println("Missing library name")
			return
		}
		lib := os.Args[2]
		version := ""
		if len(os.Args) > 3 {
			version = os.Args[3]
		}
		installLib(lib, version)
	case "list":
		listLibs()
	case "remove":
		if len(os.Args) < 3 {
			pterm.Error.Println("Missing library name")
			return
		}
		lib := os.Args[2]
		removeLib(lib)
	default:
		pterm.Error.Println("Unknown command")
	}
}

func installLib(libName, version string) {
	pterm.Info.Printf("Installing %s %s...\n", libName, version)

	// Download index
	indexURL := "https://raw.githubusercontent.com/vira-language/vira/main/repository/virus.json"
	indexPath := filepath.Join(tmpDir, "virus.json")
	err := downloadFile(indexURL, indexPath)
	if err != nil {
		pterm.Error.Printf("Failed to download index: %v\n", err)
		return
	}

	data, err := os.ReadFile(indexPath)
	if err != nil {
		pterm.Error.Printf("Failed to read index: %v\n", err)
		return
	}

	var index Index
	err = json.Unmarshal(data, &index)
	if err != nil {
		pterm.Error.Printf("Invalid index: %v\n", err)
		return
	}

	var lib Library
	found := false
	for _, l := range index.Libraries {
		if l.Name == libName {
			lib = l
			found = true
			break
		}
	}
	if !found {
		pterm.Error.Printf("Library %s not found\n", libName)
		return
	}

	if version == "" {
		// Get latest
		if len(lib.Versions) == 0 {
			pterm.Error.Println("No versions available")
			return
		}
		version = lib.Versions[len(lib.Versions)-1].Version
	}

	var ver Version
	found = false
	for _, v := range lib.Versions {
		if v.Version == version {
			ver = v
			found = true
			break
		}
	}
	if !found {
		pterm.Error.Printf("Version %s not found for %s\n", version, libName)
		return
	}

	// Download lib
	libPath := filepath.Join(libsDir, fmt.Sprintf("%s-%s.vira", libName, version))
	err = downloadFile(ver.URL, libPath)
	if err != nil {
		pterm.Error.Printf("Failed to download library: %v\n", err)
		return
	}

	pterm.Success.Printf("Installed %s %s\n", libName, version)
}

func listLibs() {
	entries, err := os.ReadDir(libsDir)
	if err != nil {
		pterm.Error.Printf("Failed to list libraries: %v\n", err)
		return
	}

	if len(entries) == 0 {
		pterm.Info.Println("No libraries installed")
		return
	}

	pterm.Info.Println("Installed libraries:")
	for _, entry := range entries {
		if !entry.IsDir() {
			pterm.Println(entry.Name())
		}
	}
}

func removeLib(libName string) {
	entries, err := os.ReadDir(libsDir)
	if err != nil {
		pterm.Error.Printf("Failed to list libraries: %v\n", err)
		return
	}

	found := false
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), libName+"-") && strings.HasSuffix(entry.Name(), ".vira") {
			path := filepath.Join(libsDir, entry.Name())
			err = os.Remove(path)
			if err != nil {
				pterm.Error.Printf("Failed to remove %s: %v\n", entry.Name(), err)
			} else {
				pterm.Success.Printf("Removed %s\n", entry.Name())
				found = true
			}
		}
	}

	if !found {
		pterm.Warning.Printf("No library matching %s found\n", libName)
	}
}

func downloadFile(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	bar := progressbar.DefaultBytes(
		resp.ContentLength,
		"Downloading",
	)

	_, err = io.Copy(io.MultiWriter(out, bar), resp.Body)
	return err
}
