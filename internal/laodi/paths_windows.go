package laodi

import (
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// Resolve LocalAppData through the shell API. UserConfigDir is Roaming on
// Windows; environment variables alone do not establish the current user's
// local state location. Directory ownership is checked separately on opening.
func DefaultStateDir(home string) (string, error) {
	folder := syscall.GUID{Data1: 0xf1b32785, Data2: 0x6fba, Data3: 0x4fcf, Data4: [8]byte{0x9d, 0x55, 0x7b, 0x8e, 0x7f, 0x15, 0x70, 0x91}}
	var value *uint16
	r, _, _ := syscall.NewLazyDLL("shell32.dll").NewProc("SHGetKnownFolderPath").Call(uintptr(unsafe.Pointer(&folder)), 0, 0, uintptr(unsafe.Pointer(&value)))
	if value != nil {
		defer syscall.NewLazyDLL("ole32.dll").NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(value)))
	}
	if r != 0 || value == nil {
		return "", errors.New("cannot resolve current user LocalAppData")
	}
	// SHGetKnownFolderPath returns a NUL-terminated allocated Windows path.
	var chars []uint16
	for i := uintptr(0); i < 32768; i++ {
		ch := *(*uint16)(unsafe.Add(unsafe.Pointer(value), i*2))
		if ch == 0 {
			root := syscall.UTF16ToString(chars)
			if !localAbsoluteWindowsPath(root) {
				return "", errors.New("LocalAppData must be a clean local drive path")
			}
			return filepath.Join(root, "Laodi-skills"), nil
		}
		chars = append(chars, ch)
	}
	return "", errors.New("LocalAppData path exceeds the Windows path limit")
}

func localAbsoluteWindowsPath(value string) bool {
	valid := len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && value[2] == '\\' && filepath.IsAbs(value) && filepath.Clean(value) == value &&
		!strings.ContainsAny(value[2:], ":\x00\r\n")
	if !valid {
		return false
	}
	for _, part := range strings.Split(value[3:], `\`) {
		if strings.TrimRight(part, ". ") != part {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return false
		}
	}
	return true
}

// No Windows snapshot schema/root has passed a real-client contract check.
// Empty defaults prevent accidentally examining a guessed user directory.
func DefaultClientApp(home string) string    { return "" }
func DefaultEvidenceRoot(home string) string { return "" }
func clientMetadataPath(app string) string   { return app }
