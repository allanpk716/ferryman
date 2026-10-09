' Hidden full-suite test runner. ASCII-only, no quotes (2026-09-23 lesson:
' wscript reads ANSI .vbs; cmd /c quote-stripping traps). Junction C:\fw915
' aliases the Chinese-named worktree. Log: C:\fw915\.scratch\gotest-rel0915.log
Dim shell
Set shell = CreateObject("WScript.Shell")
shell.Run "cmd.exe /c go -C C:\fw915 test ./... -count=1 -timeout 15m > C:\fw915\.scratch\gotest-rel0915.log 2>&1 && echo TESTSUITE_PASS>> C:\fw915\.scratch\gotest-rel0915.log || echo TESTSUITE_FAIL>> C:\fw915\.scratch\gotest-rel0915.log", 0, False
