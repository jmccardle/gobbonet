package debugreport

import (
	"fmt"
	"syscall"
	"unsafe"
)

// collectLocale reads the three code pages and the user locale.
//
// These are the fields that decide whether a path printed to a console is what
// is actually on disk. The one that matters most is the ANSI code page: on a
// Japanese system it is 932, and in 932 the single byte 0x5C — the ASCII
// backslash — is rendered as ¥. A user reporting "my paths are full of Y
// symbols" on such a system is looking at ordinary backslashes, and the whole
// mojibake theory can be dismissed the moment this field says 932. Without it,
// nobody can tell that from a genuine encoding fault, and the report is a
// screenshot and a guess.
//
// Console output and input code pages are read separately from the ANSI one:
// a batch launcher that runs chcp changes only the console, so they disagree
// on precisely the machines where the disagreement causes trouble.
func collectLocale() Locale {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")

	call := func(name string) int {
		proc := kernel32.NewProc(name)
		r, _, _ := proc.Call()
		return int(r)
	}

	l := Locale{
		ANSICodePage:          call("GetACP"),
		OEMCodePage:           call("GetOEMCP"),
		ConsoleOutputCodePage: call("GetConsoleOutputCP"),
		ConsoleInputCodePage:  call("GetConsoleCP"),
	}

	// GetUserDefaultLocaleName writes into a caller-supplied buffer;
	// LOCALE_NAME_MAX_LENGTH is 85 wide characters.
	buf := make([]uint16, 85)
	proc := kernel32.NewProc("GetUserDefaultLocaleName")
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n > 0 {
		l.UserLocale = syscall.UTF16ToString(buf[:n])
	}

	l.Notes = localeNotes(l)
	return l
}

// localeNotes flags the combinations that are known to mislead a reader, so the
// interpretation travels with the data instead of depending on whoever picks
// up the ticket knowing this.
func localeNotes(l Locale) []string {
	var notes []string
	if l.ANSICodePage == 932 || l.ConsoleOutputCodePage == 932 {
		notes = append(notes,
			"code page 932 (Shift-JIS): byte 0x5C is the ASCII backslash but renders as ¥. "+
				"Paths shown with ¥ separators are normal backslashes, not corruption.")
	}
	if l.ANSICodePage == 949 || l.ConsoleOutputCodePage == 949 {
		notes = append(notes,
			"code page 949 (EUC-KR): byte 0x5C renders as ₩ for the same reason as 932.")
	}
	if l.ConsoleOutputCodePage != 0 && l.ANSICodePage != 0 &&
		l.ConsoleOutputCodePage != l.ANSICodePage {
		notes = append(notes, fmt.Sprintf(
			"console code page (%d) differs from the system ANSI code page (%d) — "+
				"text written by a batch script and text written by the Go binary "+
				"may not render the same way.",
			l.ConsoleOutputCodePage, l.ANSICodePage))
	}
	return notes
}
