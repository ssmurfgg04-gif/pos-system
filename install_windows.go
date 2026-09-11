//go:build windows

package main

import (
        "fmt"
        "io"
        "log"
        "os"
        "os/exec"
        "path/filepath"
        "strings"

        "golang.org/x/sys/windows/registry"
)

// Windows desktop distribution: the downloaded exe is fully portable, but
// the expected UX is "double-click the download and it installs itself".
// When launched from anywhere other than the install directory the exe:
//   1. copies itself to %LOCALAPPDATA%\Programs\LedgerPOS\LedgerPOS.exe
//   2. creates Desktop + Start Menu shortcuts (PowerShell/WScript.Shell)
//   3. registers an Add/Remove Programs uninstall entry (HKCU)
//   4. relaunches the installed copy and exits
// Re-running a newer download refreshes the install the same way.

func installDir() string {
        base := os.Getenv("LOCALAPPDATA")
        if base == "" {
                base = os.Getenv("APPDATA")
        }
        return filepath.Join(base, "Programs", appName)
}

func installedExe() string { return filepath.Join(installDir(), appName+".exe") }

func desktopShortcut() string {
        return filepath.Join(os.Getenv("USERPROFILE"), "Desktop", appName+".lnk")
}

func startMenuShortcut() string {
        return filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", appName+".lnk")
}

// desktopBeforeRun self-installs when launched away from the install dir.
// Returns true when the caller should exit (installed copy is running).
func desktopBeforeRun() bool {
        exe, err := os.Executable()
        if err != nil {
                return false
        }
        if real, rerr := filepath.EvalSymlinks(exe); rerr == nil {
                exe = real
        }
        if strings.EqualFold(filepath.Dir(exe), installDir()) {
                return false // already the installed copy
        }

        dst, err := selfInstall(exe)
        if err != nil {
                log.Printf("self-install failed (%v) — continuing portable", err)
                return false // portable run still works fine
        }
        log.Printf("installed to %s — relaunching", dst)
        relaunch(dst)
        return true
}

// selfInstall copies the binary, creates shortcuts, and writes the
// uninstall registry entry. Returns the installed exe path.
func selfInstall(exe string) (string, error) {
        dir := installDir()
        if err := os.MkdirAll(dir, 0o755); err != nil {
                return "", fmt.Errorf("create %s: %w", dir, err)
        }
        dst := installedExe()
        if !strings.EqualFold(exe, dst) {
                if err := copyFile(exe, dst); err != nil {
                        return "", fmt.Errorf("copy binary: %w", err)
                }
        }
        if err := createShortcuts(dst, dir); err != nil {
                return dst, err // app still works without shortcuts
        }
        writeUninstallRegistry(dir, dst)
        return dst, nil
}

func copyFile(src, dst string) error {
        s, err := os.Open(src)
        if err != nil {
                return err
        }
        defer s.Close()
        _ = os.Remove(dst) // allow refresh while not running
        d, err := os.Create(dst)
        if err != nil {
                return err
        }
        defer d.Close()
        if _, err := io.Copy(d, s); err != nil {
                return err
        }
        return d.Sync()
}

// createShortcuts builds .lnk files via the Windows Script Host COM API
// (WScript.Shell), reached through PowerShell — zero extra dependencies.
func createShortcuts(exe, workdir string) error {
        ps := fmt.Sprintf(`$w = New-Object -ComObject WScript.Shell
foreach ($p in @('%s', '%s')) {
  $s = $w.CreateShortcut($p)
  $s.TargetPath = '%s'
  $s.WorkingDirectory = '%s'
  $s.IconLocation = '%s,0'
  $s.Description = '%s point of sale'
  $s.Save()
}`, escapePS(desktopShortcut()), escapePS(startMenuShortcut()), escapePS(exe), escapePS(workdir), escapePS(exe), appName)

        tmp, err := os.CreateTemp("", "ledgerpos-shortcut-*.ps1")
        if err != nil {
                return err
        }
        defer os.Remove(tmp.Name())
        if _, err := tmp.WriteString(ps); err != nil {
                tmp.Close()
                return err
        }
        tmp.Close()

        cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", tmp.Name())
        out, err := cmd.CombinedOutput()
        if err != nil {
                return fmt.Errorf("powershell: %v: %s", err, string(out))
        }
        return nil
}

func escapePS(s string) string { return strings.ReplaceAll(s, "'", "''") }

func writeUninstallRegistry(dir, exe string) {
        const key = `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + "LedgerPOS"
        k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
        if err != nil {
                return
        }
        defer k.Close()
        _ = k.SetStringValue("DisplayName", appName+" (Point of Sale)")
        _ = k.SetStringValue("DisplayVersion", version)
        _ = k.SetStringValue("InstallLocation", dir)
        _ = k.SetStringValue("UninstallString", `"`+exe+`" --uninstall`)
        _ = k.SetStringValue("DisplayIcon", exe+",0")
        _ = k.SetStringValue("Publisher", appName)
        _ = k.SetDWordValue("NoModify", 1)
        _ = k.SetDWordValue("NoRepair", 1)
}

// runUninstall removes shortcuts, the registry entry, and finally the
// install directory (a detached cmd deletes it a second after we exit so
// the running exe is no longer locked).
func runUninstall() int {
        fmt.Println("Uninstalling " + appName + "…")
        _ = os.Remove(desktopShortcut())
        _ = os.Remove(startMenuShortcut())
        _ = registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\LedgerPOS`)

        dir := installDir()
        fmt.Printf("Removing %s\n", dir)
        // Classic self-delete: a detached cmd waits ~2s (parent has exited and
        // unlocked the exe) then removes the directory tree.
        _ = exec.Command("cmd", "/C", "ping -n 3 127.0.0.1 >NUL & rmdir /S /Q \""+dir+"\"").Start()
        fmt.Println("Done. (Your sales data in %APPDATA%\\LedgerPOS is kept.)")
        return 0
}

func relaunch(exe string) {
        cmd := exec.Command(exe)
        cmd.Dir = filepath.Dir(exe)
        if err := cmd.Start(); err != nil {
                log.Printf("relaunch failed: %v", err)
        }
}
