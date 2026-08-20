# Creates (or repairs) the taskbar-pinnable fastllm.lnk shortcut with its
# AppUserModelID set to match what cmd/desktop/main.go stamps onto the
# running process (see appUserModelID's doc comment there for the full
# story). Without this, launching via start-desktop.vbs's wscript.exe ->
# start-desktop.bat -> fastllm-desktop.exe chain produces a pinned icon
# Windows can never recognize as "the running app" — clicking it launches
# fastllm correctly, but a second, separate taskbar icon appears for the
# actual window instead of the pinned one activating in place.
#
# Run this once (from an ordinary PowerShell prompt, no admin needed) after
# cloning the repo, or again any time the shortcut needs to be recreated.
# It writes fastllm.lnk directly into the taskbar's pinned-shortcuts
# folder — if you already have a differently-pinned fastllm icon, unpin it
# first so you don't end up with two.

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$vbsPath = Join-Path $repoRoot 'start-desktop.vbs'
if (-not (Test-Path $vbsPath)) {
    throw "start-desktop.vbs not found at $vbsPath - run this script from inside the fastllm repo."
}

# Must match cmd/desktop/main.go's appUserModelID constant exactly.
$appUserModelID = 'fastllm.desktop'

$pinDir = Join-Path $env:APPDATA 'Microsoft\Internet Explorer\Quick Launch\User Pinned\TaskBar'
New-Item -ItemType Directory -Force -Path $pinDir | Out-Null
$shortcutPath = Join-Path $pinDir 'fastllm.lnk'

Add-Type -Language CSharp -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Runtime.InteropServices.ComTypes;

public static class AppUserModelIdShortcut
{
    [ComImport, Guid("00021401-0000-0000-C000-000000000046")]
    private class ShellLink { }

    [ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("000214F9-0000-0000-C000-000000000046")]
    private interface IShellLinkW
    {
        void GetPath([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszFile, int cchMaxPath, IntPtr pfd, uint fFlags);
        void GetIDList(out IntPtr ppidl);
        void SetIDList(IntPtr pidl);
        void GetDescription([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszName, int cchMaxName);
        void SetDescription([MarshalAs(UnmanagedType.LPWStr)] string pszName);
        void GetWorkingDirectory([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszDir, int cchMaxPath);
        void SetWorkingDirectory([MarshalAs(UnmanagedType.LPWStr)] string pszDir);
        void GetArguments([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszArgs, int cchMaxPath);
        void SetArguments([MarshalAs(UnmanagedType.LPWStr)] string pszArgs);
        void GetHotkey(out short pwHotkey);
        void SetHotkey(short wHotkey);
        void GetShowCmd(out int piShowCmd);
        void SetShowCmd(int iShowCmd);
        void GetIconLocation([Out, MarshalAs(UnmanagedType.LPWStr)] System.Text.StringBuilder pszIconPath, int cchIconPath, out int piIcon);
        void SetIconLocation([MarshalAs(UnmanagedType.LPWStr)] string pszIconPath, int iIcon);
        void SetRelativePath([MarshalAs(UnmanagedType.LPWStr)] string pszPathRel, uint dwReserved);
        void Resolve(IntPtr hwnd, uint fFlags);
        void SetPath([MarshalAs(UnmanagedType.LPWStr)] string pszFile);
    }

    private static readonly PropertyKey PKEY_AppUserModel_ID = new PropertyKey(
        new Guid("9F4C2855-9F79-4B39-A8D0-E1D42DE1D5F3"), 5);

    [StructLayout(LayoutKind.Sequential, Pack = 4)]
    private struct PropertyKey
    {
        public Guid fmtid;
        public int pid;
        public PropertyKey(Guid fmtid, int pid) { this.fmtid = fmtid; this.pid = pid; }
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct PropVariant
    {
        public ushort vt;
        public ushort wReserved1, wReserved2, wReserved3;
        public IntPtr p;
        public int p2;
    }

    // VT_LPWSTR — the PROPVARIANT.vt tag for a null-terminated Unicode
    // string stored in the union's pointer field.
    private const ushort VT_LPWSTR = 31;

    [DllImport("ole32.dll")]
    private static extern int PropVariantClear(ref PropVariant pvar);

    [ComImport, InterfaceType(ComInterfaceType.InterfaceIsIUnknown), Guid("886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99")]
    private interface IPropertyStore
    {
        void GetCount(out uint cProps);
        void GetAt(uint iProp, out PropertyKey pkey);
        void GetValue(ref PropertyKey key, out PropVariant pv);
        void SetValue(ref PropertyKey key, ref PropVariant pv);
        void Commit();
    }

    public static void CreateOrUpdate(string shortcutPath, string targetPath, string arguments, string workingDirectory, string appUserModelId)
    {
        IShellLinkW link = (IShellLinkW)new ShellLink();
        link.SetPath(targetPath);
        link.SetArguments(arguments);
        link.SetWorkingDirectory(workingDirectory);

        IPropertyStore store = (IPropertyStore)link;
        // Built by hand rather than via a shlwapi.dll helper — that
        // approach turned out to hit different entry-point names/ordinals
        // across Windows versions, where this raw struct layout is stable.
        PropVariant pv = new PropVariant();
        pv.vt = VT_LPWSTR;
        pv.p = Marshal.StringToCoTaskMemUni(appUserModelId);
        try
        {
            PropertyKey key = PKEY_AppUserModel_ID;
            store.SetValue(ref key, ref pv);
            store.Commit();
        }
        finally
        {
            PropVariantClear(ref pv);
        }

        IPersistFile file = (IPersistFile)link;
        file.Save(shortcutPath, true);
    }
}
'@

[AppUserModelIdShortcut]::CreateOrUpdate(
    $shortcutPath,
    'C:\Windows\System32\wscript.exe',
    ('"' + $vbsPath + '"'),
    $repoRoot,
    $appUserModelID
)

Write-Host "Wrote $shortcutPath with AppUserModelID '$appUserModelID'."
Write-Host "If fastllm was already pinned to the taskbar under the old shortcut, unpin it, then pin this one (right-click it in File Explorer -> Pin to taskbar)."
