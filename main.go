package main

import (
	"embed"
	"fmt"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	_ "smatec/port-manager/internal/cgofix" // link framework UniformTypeIdentifiers (thiếu ở wails v2 + CLT mới)
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed ports.yaml
var embeddedPortsYAML []byte

//go:embed VERSION
var embeddedVersion []byte

func main() {
	if len(os.Args) > 1 && isCLIArg(os.Args[1]) {
		runCLI(os.Args[1:])
		return
	}
	runGUI()
}

func isCLIArg(a string) bool {
	switch a {
	case "-scan", "-start", "-stop", "-branches", "-switch", "-version":
		return true
	}
	return false
}

func runGUI() {
	app, err := NewApp(strings.TrimSpace(string(embeddedVersion)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "khởi tạo app lỗi:", err)
		os.Exit(1)
	}
	err = wails.Run(&options.App{
		Title:            "SMATEC Port Manager",
		Width:            1100,
		Height:           800,
		MinWidth:         940,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 15, G: 23, B: 42, A: 1},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		Bind:             []interface{}{app},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
