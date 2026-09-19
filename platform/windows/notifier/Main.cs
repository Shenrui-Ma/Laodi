using System;
using System.Collections.Generic;
using System.IO;
using System.Runtime.InteropServices;
using System.Runtime.InteropServices.ComTypes;
using System.Runtime.Versioning;
using System.Security;
using System.Security.AccessControl;
using System.Security.Cryptography;
using System.Security.Principal;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using System.Web.Script.Serialization;
using Microsoft.Win32;
using Microsoft.Win32.SafeHandles;
using Windows.Data.Xml.Dom;
using Windows.UI.Notifications;

[assembly: TargetFramework(".NETFramework,Version=v4.8")]

// Thin, short-lived system-runtime helper. It never receives notification copy
// or credential content from callers and never reads the monitor event store.
internal static class Program {
    const string Aumid = "dev.laodi.guardian";
    const string ActivationUri = "status";
    const string RegistryPath = @"Software\Classes\AppUserModelId\dev.laodi.guardian";
    static readonly Guid ActivatorClsid = new Guid("f5f9e6b4-e191-4d36-8f66-6423d1382e71");
    static readonly string Executable = System.Reflection.Assembly.GetExecutingAssembly().Location;
    static readonly string BaseDirectory = Path.GetDirectoryName(Executable);
    static readonly string ReceiptPath = Path.Combine(BaseDirectory, "notification-install.json");
    static readonly string Shortcut = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.Programs), "Laodi-skills.lnk");
    static readonly string Command = "\"" + Executable + "\" --com-server";
    static readonly string ComRegistryPath = @"Software\Classes\CLSID\{" + ActivatorClsid.ToString("D") + "}";
    static readonly SecurityIdentifier CurrentUser = WindowsIdentity.GetCurrent().User;
    static readonly SecurityIdentifier SystemUser = new SecurityIdentifier(WellKnownSidType.LocalSystemSid, null);
    static readonly JavaScriptSerializer Json = new JavaScriptSerializer { MaxJsonLength = 65536, RecursionLimit = 8 };
    static string OperationStage = "validation";

    [MTAThread]
    static int Main(string[] args) {
        try {
            Marshal.ThrowExceptionForHR(SetCurrentProcessExplicitAppUserModelID(Aumid));
            if (args.Length == 1 && args[0] == "--com-server") { VerifyOwnership(); RunActivator(); return 0; }
            if (args.Length == 2 && args[0] == "--activate") {
                if (args[1] != ActivationUri) throw new ArgumentException("invalid_activation_uri");
                VerifyOwnership();
                Write("activate", true, "not_requested", "activation_received", null);
                return 0;
            }
            if (args.Length == 1 && args[0] == "--test-template") {
                XmlDocument unused = Content("test", "synthetic-notice");
                Write("test-template", true, "not_requested", "template_valid", null);
                return 0;
            }
            if (args.Length == 1 && args[0] == "--register") {
                Register(); Write("register", true, "not_requested", "registered", null); return 0;
            }
            if (args.Length == 1 && args[0] == "--unregister") {
                Unregister(); Write("unregister", true, "not_requested", "unregistered", null); return 0;
            }
            if (args.Length == 1 && args[0] == "--status") {
                VerifyOwnership();
                OperationStage="notifier_create";
                ToastNotifier statusNotifier=ToastNotificationManager.CreateToastNotifier(Aumid);
                OperationStage="setting_query";
                string setting = Setting(statusNotifier.Setting);
                Write("status", true, "not_requested", setting, null); return 0;
            }
            if (args.Length == 5 && args[0] == "--send" && args[1] == "--id" && args[3] == "--kind") {
                if (!Regex.IsMatch(args[2], @"\A[A-Za-z0-9_-]{1,128}\z")) throw new ArgumentException("invalid_event_id");
                VerifyOwnership();
                OperationStage="notifier_create";
                ToastNotifier notifier = ToastNotificationManager.CreateToastNotifier(Aumid);
                string setting = "unknown";
                try{setting=Setting(notifier.Setting);}catch(COMException){/* A first-use status query can be unavailable. Show remains authoritative. */}
                if (setting != "enabled" && setting != "unknown") { Write("send", false, "not_delivered", setting, "notifications_disabled"); return 2; }
                ToastNotification toast = new ToastNotification(Content(args[4], args[2]));
                toast.Tag = Hash(Encoding.UTF8.GetBytes(args[2])).Substring(0, 16);
                toast.Group = "laodi";
                toast.ExpirationTime = DateTimeOffset.UtcNow.AddHours(12);
                OperationStage="show";notifier.Show(toast);
                Write("send", true, "accepted_by_os", setting, null);
                return 0;
            }
            throw new ArgumentException("invalid_arguments");
        } catch (Exception e) {
            string code = e is ArgumentException ? "invalid_notification_arguments" : "notification_operation_failed_"+OperationStage;
            string action="unknown";
            if(args.Length>0&&Array.Exists(new string[]{"--register","--unregister","--status","--send","--activate","--com-server","--test-template"},s=>s==args[0]))action=args[0].Substring(2);
            Write(action, false, "not_delivered", "unknown", code + ":0x" + e.HResult.ToString("X8"));
            return 1;
        }
    }

    static void Write(string action, bool ok, string delivery, string authorization, string error) {
        var value = new Dictionary<string,object> {
            {"schema_version",1},{"action",action},{"ok",ok},{"delivery",delivery},
            {"authorization",authorization},{"focus_assist","unknown"},{"visible_to_user","unconfirmed"},
            {"task_interrupted",false},{"settings_uri","ms-settings:notifications"}
        };
        uint packageLength=0;
        value.Add("process_has_package_identity",GetCurrentPackageFullName(ref packageLength,IntPtr.Zero)!=15700);
        if (error != null) value.Add("error_code",error);
        using (var writer = new StreamWriter(Console.OpenStandardOutput(),new UTF8Encoding(false))) { writer.WriteLine(Json.Serialize(value)); }
    }

    static string Setting(NotificationSetting value) {
        switch(value) {
            case NotificationSetting.Enabled: return "enabled";
            case NotificationSetting.DisabledForApplication: return "disabled_for_application";
            case NotificationSetting.DisabledForUser: return "disabled_for_user";
            case NotificationSetting.DisabledByGroupPolicy: return "disabled_by_policy";
            case NotificationSetting.DisabledByManifest: return "disabled_by_manifest";
            default:return "unknown";
        }
    }

    static XmlDocument Content(string kind,string id) {
        string title,body;
        switch(kind) {
            case "test": title="老底：合成通知测试"; body="这是一条无真实凭据的本机测试提醒。系统接受通知不等于您已看到；请确认中文、图标和通知是否可见。"; break;
            case "snapshot-history":title="老底：发现含 Git 历史的快照线索";body="清单包含历史对象；是否上传成功尚未确认。当前任务继续运行。";break;
            case "upload-attempt":title="老底：发现仓库上传尝试记录";body="客户端进入过上传尝试流程；是否发出请求或完成仍未知。当前任务继续运行。";break;
            case "upload-accepted":title="老底：发现上传接受记录";body="客户端记录表明上传获确认；远端保存情况未经核验。当前任务继续运行。";break;
            case "snapshot-workspace":title="老底：发现工作区快照清单";body="客户端生成了文件清单；是否上传或包含秘密尚未知。当前任务继续运行。";break;
            case "workspace-upload-attempt":title="老底：发现工作区快照上传尝试";body="客户端进入过相关快照的上传流程；是否发出请求或完成仍未知。当前任务继续运行。";break;
            case "workspace-upload-accepted":title="老底：发现工作区快照接受记录";body="客户端记录相关快照获 HTTP 成功响应；未据此确认含有秘密或远端留存。当前任务继续运行。";break;
            case "snapshot-config":title="老底：发现附加配置快照线索";body="附加清单列入全局配置；未读取其内容，是否包含秘密或完成上传尚未知。";break;
            case "config-upload-attempt":title="老底：发现附加配置上传尝试";body="客户端进入过相关快照的上传流程；是否发出请求或完成仍未知。当前任务继续运行。";break;
            case "config-upload-accepted":title="老底：发现附加配置接受记录";body="客户端记录相关快照获接受；具体内容及远端保存情况未经核验。当前任务继续运行。";break;
            case "tool-output-sensitive":title="老底：工具输出出现疑似凭据";body="已在本机记录风险，提醒不包含具体内容；是否进入模型请求或完成上传尚未确认。当前任务继续运行。";break;
            case "hook-coverage-degraded":title="老底：工具事件检测存在缺口";body="部分事件可能未被完整检查。当前任务继续运行。可在 Agent 中询问“检查老底状态”。";break;
            case "coverage-degraded":title="老底：部分监测暂不可用";body="已支持范围出现缺口。当前任务继续运行。可在 Agent 中询问“检查老底状态”。";break;
            default:throw new ArgumentException("unsupported_notice_kind");
        }
        string logo=Path.Combine(BaseDirectory,"laodi-logo.png");
        if (!File.Exists(logo)) throw new IOException("missing_logo");
        SafeRead(logo,4*1024*1024);
        string xml="<toast launch=\""+ActivationUri+"\"><visual><binding template=\"ToastGeneric\"><image placement=\"appLogoOverride\" src=\""+SecurityElement.Escape(new Uri(logo).AbsoluteUri)+"\"/><text>"+SecurityElement.Escape(title)+"</text><text>"+SecurityElement.Escape(body)+"</text></binding></visual></toast>";
        XmlDocument document=new XmlDocument();document.LoadXml(xml);return document;
    }

    static void Register() {
        CheckPrivateDirectory();
        if(File.Exists(ReceiptPath)){VerifyOwnership();return;}
        if(File.Exists(Shortcut))throw new IOException("foreign_shortcut");
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(RegistryPath)){if(key!=null)throw new IOException("foreign_notification_identity");}
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(ComRegistryPath)){if(key!=null)throw new IOException("foreign_activator");}
        string temp=Path.Combine(Path.GetDirectoryName(Shortcut),".laodi-"+Guid.NewGuid().ToString("N")+".lnk");
        string shortcutHash=null;
        try {
            CreateShortcut(temp);
            shortcutHash=Hash(SafeRead(temp,65536));
            File.Move(temp,Shortcut);
            using(RegistryKey key=Registry.CurrentUser.CreateSubKey(RegistryPath)){
                key.SetValue("DisplayName","老底",RegistryValueKind.String);
                key.SetValue("IconUri",Path.Combine(BaseDirectory,"laodi-logo.png"),RegistryValueKind.String);
                key.SetValue("CustomActivator","{"+ActivatorClsid.ToString("D")+"}",RegistryValueKind.String);
            }
            using(RegistryKey key=Registry.CurrentUser.CreateSubKey(ComRegistryPath))using(RegistryKey command=key.CreateSubKey("LocalServer32")){command.SetValue("",Command,RegistryValueKind.String);}
            VerifyProtocol();
            var receipt=new Dictionary<string,object>{{"schema_version",1},{"aumid",Aumid},{"helper",Executable},{"shortcut_hash",shortcutHash},{"activator_command",Command},{"activator_clsid",ActivatorClsid.ToString("D")}};
            WritePrivate(ReceiptPath,Encoding.UTF8.GetBytes(Json.Serialize(receipt)+"\n"));
        } catch {
            if(shortcutHash!=null&&File.Exists(Shortcut)&&Hash(SafeRead(Shortcut,65536))==shortcutHash)File.Delete(Shortcut);
            try{VerifyProtocol();Registry.CurrentUser.DeleteSubKeyTree(RegistryPath);Registry.CurrentUser.DeleteSubKeyTree(ComRegistryPath);}catch{}
            throw;
        } finally{if(File.Exists(temp))File.Delete(temp);}
    }

    static void VerifyOwnership() {
        OperationStage="private_directory";
        CheckPrivateDirectory();
        OperationStage="receipt_read";
        var receipt=Json.Deserialize<Dictionary<string,object>>(Encoding.UTF8.GetString(SafeRead(ReceiptPath,65536)));
        OperationStage="registration_compare";
        if(receipt==null||receipt.Count!=6||Convert.ToInt32(receipt["schema_version"])!=1||(string)receipt["aumid"]!=Aumid||(string)receipt["helper"]!=Executable||(string)receipt["activator_command"]!=Command||(string)receipt["activator_clsid"]!=ActivatorClsid.ToString("D")||(string)receipt["shortcut_hash"]!=Hash(SafeRead(Shortcut,65536)))throw new IOException("changed_notification_registration");
        OperationStage="registry_verify";VerifyProtocol();
    }

    static void Unregister() {
        VerifyOwnership();
        // Only our own AUMID history is removed. Windows user settings are kept.
        try { ToastNotificationManager.History.Clear(Aumid); } catch(COMException e) { if(e.HResult!=unchecked((int)0x80070490))throw; }
        VerifyOwnership();
        File.Delete(Shortcut);
        Registry.CurrentUser.DeleteSubKeyTree(RegistryPath);
        Registry.CurrentUser.DeleteSubKeyTree(ComRegistryPath);
        File.Delete(ReceiptPath);
    }

    static void VerifyProtocol() {
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(RegistryPath)){
            if(key==null||key.GetValueNames().Length!=3||key.GetSubKeyNames().Length!=0||(string)key.GetValue("DisplayName")!="老底"||(string)key.GetValue("IconUri")!=Path.Combine(BaseDirectory,"laodi-logo.png")||(string)key.GetValue("CustomActivator")!="{"+ActivatorClsid.ToString("D")+"}")throw new IOException("changed_notification_identity");
            foreach(string name in key.GetValueNames()){if(key.GetValueKind(name)!=RegistryValueKind.String)throw new IOException("changed_notification_value_type");}
        }
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(ComRegistryPath)){
            if(key==null||key.GetValueNames().Length!=0||key.GetSubKeyNames().Length!=1||key.GetSubKeyNames()[0]!="LocalServer32")throw new IOException("changed_notification_activator");
            using(RegistryKey command=key.OpenSubKey("LocalServer32")){if(command.GetSubKeyNames().Length!=0||command.GetValueNames().Length!=1||command.GetValueKind("")!=RegistryValueKind.String||(string)command.GetValue("")!=Command)throw new IOException("changed_notification_activator_command");}
        }
    }

    internal static readonly ManualResetEvent Activated=new ManualResetEvent(false);
    static void RunActivator(){
        var factory=new NotificationFactory();
        IntPtr pointer=Marshal.GetComInterfaceForObject(factory,typeof(INotificationClassFactory));
        uint cookie;
        Guid id=ActivatorClsid;
        try{Marshal.ThrowExceptionForHR(CoRegisterClassObject(ref id,pointer,4,1,out cookie));}finally{Marshal.Release(pointer);}
        try{Activated.WaitOne(TimeSpan.FromSeconds(15));}finally{CoRevokeClassObject(cookie);}
        GC.KeepAlive(factory);
    }
    [DllImport("ole32.dll")]static extern int CoRegisterClassObject(ref Guid clsid,IntPtr factory,uint context,uint flags,out uint cookie);
    [DllImport("ole32.dll")]static extern int CoRevokeClassObject(uint cookie);

    static void CheckPrivateDirectory() {
        CheckAncestors(BaseDirectory);
        DriveInfo drive=new DriveInfo(Path.GetPathRoot(BaseDirectory));
        if(drive.DriveType!=DriveType.Fixed||(drive.DriveFormat!="NTFS"&&drive.DriveFormat!="ReFS"))throw new IOException("private_local_volume_required");
        DirectorySecurity security=Directory.GetAccessControl(BaseDirectory);
        if(!security.GetOwner(typeof(SecurityIdentifier)).Equals(CurrentUser)||!security.AreAccessRulesProtected)throw new IOException("private_install_directory_required");
        foreach(FileSystemAccessRule rule in security.GetAccessRules(true,true,typeof(SecurityIdentifier))){if(rule.AccessControlType==AccessControlType.Allow&&!rule.IdentityReference.Equals(CurrentUser)&&!rule.IdentityReference.Equals(SystemUser))throw new IOException("private_install_dacl_required");}
    }

    static void CheckAncestors(string path) {
        if(!Path.IsPathRooted(path)||path.StartsWith(@"\\",StringComparison.Ordinal)||path.Substring(2).Contains(":"))throw new IOException("local_path_required");
        for(string p=path;!String.IsNullOrEmpty(p);p=Path.GetDirectoryName(p)){if((File.GetAttributes(p)&FileAttributes.ReparsePoint)!=0)throw new IOException("reparse_path_refused");}
    }

    static byte[] SafeRead(string path,int limit) {
        CheckAncestors(path);
        using(FileStream stream=new FileStream(path,FileMode.Open,FileAccess.Read,FileShare.Read)){
            ByHandleFileInformation info;
            if(!GetFileInformationByHandle(stream.SafeFileHandle,out info)||info.NumberOfLinks!=1||(info.FileAttributes&0x400)!=0||stream.Length>limit)throw new IOException("unsafe_or_oversized_file");
            byte[] data=new byte[(int)stream.Length];int offset=0;
            while(offset<data.Length){int n=stream.Read(data,offset,data.Length-offset);if(n==0)throw new EndOfStreamException();offset+=n;}return data;
        }
    }

    static void WritePrivate(string path,byte[] data) {
        FileSecurity security=new FileSecurity();security.SetOwner(CurrentUser);security.SetAccessRuleProtection(true,false);
        security.AddAccessRule(new FileSystemAccessRule(CurrentUser,FileSystemRights.FullControl,AccessControlType.Allow));
        security.AddAccessRule(new FileSystemAccessRule(SystemUser,FileSystemRights.FullControl,AccessControlType.Allow));
        using(FileStream stream=new FileStream(path,FileMode.CreateNew,FileSystemRights.Write|FileSystemRights.ReadPermissions,FileShare.None,4096,FileOptions.WriteThrough,security)){stream.Write(data,0,data.Length);stream.Flush(true);}
    }

    static string Hash(byte[] data){using(SHA256 sha=SHA256.Create()){return BitConverter.ToString(sha.ComputeHash(data)).Replace("-","").ToLowerInvariant();}}

    static void CreateShortcut(string path) {
        object instance=new ShellLink();
        try{
            IShellLinkW link=(IShellLinkW)instance;
            link.SetPath(Executable);link.SetArguments("--activate \""+ActivationUri+"\"");link.SetWorkingDirectory(BaseDirectory);link.SetDescription("老底本地监测提醒");link.SetIconLocation(Executable,0);
            IPropertyStore store=(IPropertyStore)instance;
            PropertyKey id=new PropertyKey(new Guid("9f4c2855-9f79-4b39-a8d0-e1d42de1d5f3"),5);
            PropertyKey clsid=new PropertyKey(id.FormatId,26);
            using(PropVariant value=PropVariant.String(Aumid)){store.SetValue(ref id,value);}
            using(PropVariant value=PropVariant.Guid(ActivatorClsid)){store.SetValue(ref clsid,value);}
            store.Commit();((IPersistFile)instance).Save(path,true);
        }finally{Marshal.FinalReleaseComObject(instance);}
    }

    [StructLayout(LayoutKind.Sequential)] struct ByHandleFileInformation {public uint FileAttributes;public System.Runtime.InteropServices.ComTypes.FILETIME CreationTime,LastAccessTime,LastWriteTime;public uint VolumeSerialNumber,FileSizeHigh,FileSizeLow,NumberOfLinks,FileIndexHigh,FileIndexLow;}
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetFileInformationByHandle(SafeFileHandle handle,out ByHandleFileInformation info);
    [DllImport("shell32.dll",CharSet=CharSet.Unicode)] static extern int SetCurrentProcessExplicitAppUserModelID(string appId);
    [DllImport("kernel32.dll",CharSet=CharSet.Unicode)] static extern int GetCurrentPackageFullName(ref uint length,IntPtr name);
    [ComImport,Guid("00021401-0000-0000-C000-000000000046")] class ShellLink{}
    [ComImport,Guid("000214F9-0000-0000-C000-000000000046"),InterfaceType(ComInterfaceType.InterfaceIsIUnknown)] interface IShellLinkW {
        void GetPath([Out,MarshalAs(UnmanagedType.LPWStr)]StringBuilder file,int count,IntPtr data,uint flags);void GetIDList(out IntPtr id);void SetIDList(IntPtr id);
        void GetDescription([Out,MarshalAs(UnmanagedType.LPWStr)]StringBuilder text,int count);void SetDescription([MarshalAs(UnmanagedType.LPWStr)]string text);
        void GetWorkingDirectory([Out,MarshalAs(UnmanagedType.LPWStr)]StringBuilder text,int count);void SetWorkingDirectory([MarshalAs(UnmanagedType.LPWStr)]string text);
        void GetArguments([Out,MarshalAs(UnmanagedType.LPWStr)]StringBuilder text,int count);void SetArguments([MarshalAs(UnmanagedType.LPWStr)]string text);
        void GetHotkey(out short value);void SetHotkey(short value);void GetShowCmd(out int value);void SetShowCmd(int value);void GetIconLocation([Out,MarshalAs(UnmanagedType.LPWStr)]StringBuilder text,int count,out int index);void SetIconLocation([MarshalAs(UnmanagedType.LPWStr)]string text,int index);void SetRelativePath([MarshalAs(UnmanagedType.LPWStr)]string text,uint reserved);void Resolve(IntPtr window,uint flags);void SetPath([MarshalAs(UnmanagedType.LPWStr)]string text);
    }
    [StructLayout(LayoutKind.Sequential)] struct PropertyKey {public Guid FormatId;public uint PropertyId;public PropertyKey(Guid format,uint id){FormatId=format;PropertyId=id;}}
    [ComImport,Guid("886D8EEB-8CF2-4446-8D02-CDBA1DBDCF99"),InterfaceType(ComInterfaceType.InterfaceIsIUnknown)] interface IPropertyStore {void GetCount(out uint count);void GetAt(uint index,out PropertyKey key);void GetValue(ref PropertyKey key,[Out]PropVariant value);void SetValue(ref PropertyKey key,[In]PropVariant value);void Commit();}
    [StructLayout(LayoutKind.Explicit,Size=24)] sealed class PropVariant:IDisposable {
        [FieldOffset(0)]ushort type;[FieldOffset(8)]IntPtr pointer;
        public static PropVariant String(string s){return new PropVariant{type=31,pointer=Marshal.StringToCoTaskMemUni(s)};}
        public static PropVariant Guid(Guid guid){IntPtr p=Marshal.AllocCoTaskMem(16);Marshal.StructureToPtr(guid,p,false);return new PropVariant{type=72,pointer=p};}
        public void Dispose(){if(pointer!=IntPtr.Zero){Marshal.FreeCoTaskMem(pointer);pointer=IntPtr.Zero;}}
    }
}

[ComVisible(true),Guid("53E31837-6600-4A81-9395-75CFFE746F94"),InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
public interface ILaodiNotificationActivation {
    void Activate([MarshalAs(UnmanagedType.LPWStr)]string appUserModelId,[MarshalAs(UnmanagedType.LPWStr)]string arguments,IntPtr userInput,uint count);
}

[ComVisible(true),ClassInterface(ClassInterfaceType.None)]
public sealed class NotificationActivation:ILaodiNotificationActivation {
    public void Activate(string appUserModelId,string arguments,IntPtr userInput,uint count){
        // Do not dereference or persist arbitrary activation payloads.
        if(appUserModelId=="dev.laodi.guardian"&&arguments=="status"&&count==0)Program.Activated.Set();
    }
}

[ComVisible(true),Guid("00000001-0000-0000-C000-000000000046"),InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
public interface INotificationClassFactory {
    [PreserveSig]int CreateInstance(IntPtr outer,ref Guid interfaceId,out IntPtr instance);
    [PreserveSig]int LockServer([MarshalAs(UnmanagedType.Bool)]bool locked);
}

[ComVisible(true),ClassInterface(ClassInterfaceType.None)]
public sealed class NotificationFactory:INotificationClassFactory {
    public int CreateInstance(IntPtr outer,ref Guid interfaceId,out IntPtr instance){
        instance=IntPtr.Zero;
        if(outer!=IntPtr.Zero)return unchecked((int)0x80040110);
        object activation=new NotificationActivation();
        IntPtr unknown=Marshal.GetIUnknownForObject(activation);
        try{return Marshal.QueryInterface(unknown,ref interfaceId,out instance);}finally{Marshal.Release(unknown);}
    }
    public int LockServer(bool locked){return 0;}
}
