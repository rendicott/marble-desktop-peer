package autostart

import "unicode/utf16"

// Pure helpers for the Windows Startup launcher; untagged so they are tested on every OS.

// windowsLauncherVBS is the Startup-folder launcher. A .cmd would leave a console
// window open for the whole session; WScript's Run with window style 0 starts the
// console-subsystem peer hidden (logs go to %USERPROFILE%\.marble-peer\peer.log).
func windowsLauncherVBS(exe string) string {
	return "' marble-peer login launcher (written by `marble-peer install-autostart`).\r\n" +
		"CreateObject(\"WScript.Shell\").Run \"\"\"\" & \"" + exe + "\" & \"\"\" run\", 0, False\r\n"
}

// utf16LEWithBOM encodes s the way wscript.exe reads Unicode script files, so a
// non-ASCII user name in the exe path survives (a UTF-8 .vbs is read as ANSI).
func utf16LEWithBOM(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+2*len(u))
	out = append(out, 0xFF, 0xFE)
	for _, c := range u {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}
