' Launches fastllm with no visible console window, then opens the browser.
' Double-click this (or a shortcut to it) to start fastllm like a regular app.
Set fso = CreateObject("Scripting.FileSystemObject")
Set shell = CreateObject("WScript.Shell")
scriptDir = fso.GetParentFolderName(WScript.ScriptFullName)
shell.Run """" & scriptDir & "\start.bat""", 0, False
