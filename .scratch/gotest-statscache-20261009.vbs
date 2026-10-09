' Hidden full-suite test runner (stats-cache branch 2026-10-09). ASCII-only,
' zero quotes in command string, junction C:\fwstat aliases the Chinese-named
' worktree (2026-09-23 lesson, same shape as gotest-rel0915.vbs).
Dim shell
Set shell = CreateObject("WScript.Shell")
shell.Run "cmd.exe /c go -C C:\fwstat test ./... -count=1 -timeout 15m > C:\fwstat\.scratch\gotest-statscache-20261009.log 2>&1 && echo TESTSUITE_PASS>> C:\fwstat\.scratch\gotest-statscache-20261009.log || echo TESTSUITE_FAIL>> C:\fwstat\.scratch\gotest-statscache-20261009.log", 0, False
