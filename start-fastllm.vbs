' Launches the headless fastllm API server with no visible console window.
' Double-click this (or a shortcut to it) to start the server like a regular
' app. For the interactive UI, run fastllm with no arguments in a terminal.
Set fso = CreateObject("Scripting.FileSystemObject")
Set shell = CreateObject("WScript.Shell")
scriptDir = fso.GetParentFolderName(WScript.ScriptFullName)
shell.Run """" & scriptDir & "\start.bat""", 0, False
