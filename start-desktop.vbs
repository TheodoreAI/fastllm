' Launches the fastllm desktop app (Wails) with no visible console
' window. Double-click this (or a shortcut to it) to start it like a
' regular app — mirrors start-fastllm.vbs's role for the browser build.
Set fso = CreateObject("Scripting.FileSystemObject")
Set shell = CreateObject("WScript.Shell")
scriptDir = fso.GetParentFolderName(WScript.ScriptFullName)
shell.Run """" & scriptDir & "\start-desktop.bat""", 0, False
