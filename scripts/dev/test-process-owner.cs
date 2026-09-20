// Development harness only. Elevated runners can default new objects to the
// Administrators group. Match ordinary-user ownership without changing policy
// or weakening the product's current-user-only storage checks.
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

public sealed class LaodiTestProcessOwner : IDisposable
{
    [DllImport("kernel32.dll")] private static extern IntPtr GetCurrentProcess();
    [DllImport("kernel32.dll")] private static extern bool CloseHandle(IntPtr handle);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern bool GetTokenInformation(IntPtr token, int kind, IntPtr data, int size, out int required);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern bool SetTokenInformation(IntPtr token, int kind, IntPtr data, int size);
    [DllImport("advapi32.dll")] private static extern bool EqualSid(IntPtr first, IntPtr second);

    private IntPtr token;
    private IntPtr previous;
    private bool changed;
    public bool PreviousOwnerIsUser { get; private set; }

    private IntPtr ReadInformation(int kind)
    {
        int size;
        GetTokenInformation(token, kind, IntPtr.Zero, 0, out size);
        if (size <= 0) throw new Win32Exception(Marshal.GetLastWin32Error());
        IntPtr data = Marshal.AllocHGlobal(size);
        if (GetTokenInformation(token, kind, data, size, out size)) return data;
        int error = Marshal.GetLastWin32Error();
        Marshal.FreeHGlobal(data);
        throw new Win32Exception(error);
    }

    public LaodiTestProcessOwner()
    {
        // TOKEN_QUERY | TOKEN_ADJUST_DEFAULT; no privileges are enabled.
        if (!OpenProcessToken(GetCurrentProcess(), 0x0008 | 0x0080, out token))
            throw new Win32Exception(Marshal.GetLastWin32Error());
        IntPtr user = IntPtr.Zero;
        try
        {
            previous = ReadInformation(4); // TokenOwner: pointer to owner SID.
            user = ReadInformation(1); // TokenUser starts with the user SID pointer.
            PreviousOwnerIsUser = EqualSid(Marshal.ReadIntPtr(previous), Marshal.ReadIntPtr(user));
            if (!SetTokenInformation(token, 4, user, IntPtr.Size))
                throw new Win32Exception(Marshal.GetLastWin32Error());
            changed = true;
        }
        catch { Dispose(); throw; }
        finally { if (user != IntPtr.Zero) Marshal.FreeHGlobal(user); }
    }

    public void Dispose()
    {
        int error = 0;
        if (changed && !SetTokenInformation(token, 4, previous, IntPtr.Size))
            error = Marshal.GetLastWin32Error();
        changed = false;
        if (previous != IntPtr.Zero) { Marshal.FreeHGlobal(previous); previous = IntPtr.Zero; }
        if (token != IntPtr.Zero) { CloseHandle(token); token = IntPtr.Zero; }
        if (error != 0) throw new Win32Exception(error, "Could not restore test process default owner.");
    }
}
