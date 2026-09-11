//go:build !windows

package main

import "fmt"

// macOS ships inside a .app bundle (the bundle IS the install) and Linux
// is a plain portable binary, so there is nothing to self-install.

func desktopBeforeRun() bool { return false }

func runUninstall() int {
	dir, err := userDataDir()
	if err != nil {
		fmt.Println("Nothing to uninstall.")
		return 0
	}
	fmt.Printf("This app is portable — just delete the binary.\n")
	fmt.Printf("Your data lives in %s (delete it to remove everything).\n", dir)
	return 0
}
