//go:build windows

package handlers

import "golang.org/x/sys/windows"

// shellOpen asks Windows to open link with its registered app. Unlike
// rundll32, ShellExecute returns an error when nothing is registered for
// the protocol (WhatsApp Desktop not installed), so the caller can fall
// back to WhatsApp Web.
func shellOpen(link string) bool {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return false
	}
	file, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return false
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL) == nil
}
