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
    static string StateDirectory;
    static string ReceiptPath { get { return Path.Combine(StateDirectory, "notification-install.json"); } }
    static string PendingReceiptPath { get { return Path.Combine(StateDirectory, "notification-install.pending.json"); } }
    static string Launcher { get { return Path.Combine(StateDirectory, "laodi-host.exe"); } }
    static string StableLogo { get { return Path.Combine(StateDirectory, "laodi-logo.png"); } }
    static string ShortcutIcon { get { return Path.Combine(StateDirectory, "notification-icon.ico"); } }
    static readonly string Shortcut = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.Programs), "Laodi-skills.lnk");
    static string Command { get { return "\"" + Launcher + "\" --notification-activate"; } }
    static readonly string ComRegistryPath = @"Software\Classes\CLSID\{" + ActivatorClsid.ToString("D") + "}";
    static readonly SecurityIdentifier CurrentUser = WindowsIdentity.GetCurrent().User;
    static readonly SecurityIdentifier SystemUser = new SecurityIdentifier(WellKnownSidType.LocalSystemSid, null);
    static readonly JavaScriptSerializer Json = new JavaScriptSerializer { MaxJsonLength = 65536, RecursionLimit = 8 };
    static string OperationStage = "validation";
    static bool RegistrationVerified;

    [MTAThread]
    static int Main(string[] args) {
        try {
            args = Configure(args);
            Marshal.ThrowExceptionForHR(SetCurrentProcessExplicitAppUserModelID(Aumid));
            if (args.Length == 1 && args[0] == "--com-server") { VerifyOwnership(); RunActivator(); return 0; }
            if (args.Length == 1 && args[0] == "--test-activation") {
                VerifyOwnership();
                TestActivation();
                WriteOutcome("test-activation",new NotificationOutcome{OK=true,Activation="callback_completed"});
                return 0;
            }
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
                using(RegistrationMutex.Acquire()){Register();} RegistrationVerified=true;Write("register", true, "not_requested", "registered", null); return 0;
            }
            if (args.Length == 1 && args[0] == "--unregister") {
                using(RegistrationMutex.Acquire()){Unregister();} Write("unregister", true, "not_requested", "unregistered", null); return 0;
            }
            if (args.Length == 1 && args[0] == "--status") {
                VerifyOwnership();
                OperationStage="notifier_create";
                ToastNotifier statusNotifier=ToastNotificationManager.CreateToastNotifier(Aumid);
                OperationStage="setting_query";
                NotificationOutcome status=NotificationEngine.QuerySetting(()=>Setting(statusNotifier.Setting));
                WriteOutcome("status",status);return 0;
            }
            if (args.Length == 5 && args[0] == "--send" && args[1] == "--id" && args[3] == "--kind") {
                ValidateEventId(args[2]);
                VerifyOwnership();
                XmlDocument document=Content(args[4],args[2]);
                OperationStage="notifier_create";
                ToastNotifier notifier = ToastNotificationManager.CreateToastNotifier(Aumid);
                ToastNotification toast = new ToastNotification(document);
                toast.Tag = Hash(Encoding.UTF8.GetBytes(args[2])).Substring(0, 16);
                toast.Group = "laodi";
                toast.ExpirationTime = DateTimeOffset.UtcNow.AddHours(12);
                OperationStage="show";
                NotificationOutcome delivery=NotificationEngine.Deliver(()=>Setting(notifier.Setting),()=>notifier.Show(toast));
                WriteOutcome("send",delivery);return delivery.OK?0:2;
            }
            throw new ArgumentException("invalid_arguments");
        } catch (Exception e) {
            string code = e is ArgumentException ? "invalid_notification_arguments" : "notification_operation_failed_"+OperationStage;
            if(e is IOException&&(e.Message=="notification_registration_redirected_use_normal_windows_terminal"||e.Message=="notification_files_redirected_use_normal_windows_terminal"))code=e.Message;
            string action="unknown";
            if(args.Length>0&&Array.Exists(new string[]{"--register","--unregister","--status","--send","--activate","--com-server","--test-template","--test-activation"},s=>s==args[0]))action=args[0].Substring(2);
            Write(action, false, "not_delivered", "unknown", code + ":0x" + e.HResult.ToString("X8"));
            return 1;
        }
    }

    internal static string[] Configure(string[] args) {
        if(args.Length>=3&&args[0]=="--state-dir"){
            StateDirectory=Path.GetFullPath(args[1]);
            if(!String.Equals(StateDirectory,args[1],StringComparison.OrdinalIgnoreCase)||args[1].IndexOfAny(new char[]{'\0','\r','\n'})>=0)throw new ArgumentException("invalid_state_path");
            string[] rest=new string[args.Length-2];Array.Copy(args,2,rest,0,rest.Length);return rest;
        }
        if(args.Length==1&&args[0]=="--test-template"){StateDirectory=BaseDirectory;return args;}
        throw new ArgumentException("state_directory_required");
    }

    internal static void ValidateEventId(string value){if(!Regex.IsMatch(value,@"\A[A-Za-z0-9_-]{1,128}\z"))throw new ArgumentException("invalid_event_id");}

    static void Write(string action, bool ok, string delivery, string authorization, string error) {
        WriteOutcome(action,new NotificationOutcome{OK=ok,Delivery=delivery,Authorization=authorization,Error=error});
    }

    static void WriteOutcome(string action,NotificationOutcome result) {
        var value = new Dictionary<string,object> {
            {"schema_version",1},{"action",action},{"ok",result.OK},{"delivery",result.Delivery},
            {"authorization",result.Authorization},{"focus_assist","unknown"},{"visible_to_user","unconfirmed"},
            {"registration",action=="unregister"&&result.OK?"removed":RegistrationVerified?"verified":"unverified"},
            {"task_interrupted",false},{"settings_uri","ms-settings:notifications"}
        };
        uint packageLength=0;
        int packageResult=GetCurrentPackageFullName(ref packageLength,IntPtr.Zero);
        value.Add("process_has_package_identity",packageResult==15700?(object)false:packageResult==0||packageResult==122?(object)true:null);
        if (result.Error != null) value.Add("error_code",result.Error);
        if (result.Error != null && result.Error.Contains("redirected_use_normal_windows_terminal"))value.Add("setup_hint","Run installation from an ordinary Windows terminal; this caller redirects files or registry registration.");
        if (result.AuthorizationError != null) {value.Add("authorization_error_code",result.AuthorizationError);value.Add("authorization_error_stage","setting_query");}
        if (result.Activation != null) value.Add("activation",result.Activation);
        if (action == "test-activation") value.Add("notification_api_calls",0);
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

    internal static XmlDocument Content(string kind,string id) {
        ValidateEventId(id);
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
        string logo=StableLogo;
        if (!File.Exists(logo)) throw new IOException("missing_logo");
        SafeRead(logo,4*1024*1024);
        string xml="<toast launch=\""+ActivationUri+"\"><visual><binding template=\"ToastGeneric\"><image placement=\"appLogoOverride\" src=\""+SecurityElement.Escape(new Uri(logo).AbsoluteUri)+"\"/><text>"+SecurityElement.Escape(title)+"</text><text>"+SecurityElement.Escape(body)+"</text></binding></visual></toast>";
        XmlDocument document=new XmlDocument();document.LoadXml(xml);return document;
    }

    static void Register() {
        CheckPrivateDirectory();
        if(File.Exists(ReceiptPath)){VerifyOwnership();return;}
        if(File.Exists(PendingReceiptPath)){RecoverPendingRegistration();}
        if(File.Exists(Shortcut))throw new IOException("foreign_shortcut");
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(RegistryPath)){if(key!=null)throw new IOException("foreign_notification_identity");}
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(ComRegistryPath)){if(key!=null)throw new IOException("foreign_activator");}
        string temp=Path.Combine(Path.GetDirectoryName(Shortcut),".laodi-"+Guid.NewGuid().ToString("N")+".lnk");
        string shortcutHash=null;
        try {
            if(File.Exists(ShortcutIcon))throw new IOException("unowned_notification_icon");
            byte[] iconBytes;
            using(System.Drawing.Icon icon=System.Drawing.Icon.ExtractAssociatedIcon(Executable))using(MemoryStream output=new MemoryStream()){
                if(icon==null)throw new IOException("missing_embedded_logo");icon.Save(output);iconBytes=output.ToArray();
            }
            CreateShortcut(temp);
            shortcutHash=Hash(SafeRead(temp,65536));
            var receipt=NewReceipt(shortcutHash,Hash(iconBytes));
            WritePrivate(PendingReceiptPath,Encoding.UTF8.GetBytes(Json.Serialize(receipt)+"\n"));
            WritePrivate(ShortcutIcon,iconBytes);
            File.Move(temp,Shortcut);
            using(RegistryKey key=Registry.CurrentUser.CreateSubKey(RegistryPath)){
                key.SetValue("DisplayName","老底",RegistryValueKind.String);
                key.SetValue("IconUri",StableLogo,RegistryValueKind.String);
                key.SetValue("CustomActivator","{"+ActivatorClsid.ToString("D")+"}",RegistryValueKind.String);
            }
            using(RegistryKey key=Registry.CurrentUser.CreateSubKey(ComRegistryPath))using(RegistryKey command=key.CreateSubKey("LocalServer32")){command.SetValue("",Command,RegistryValueKind.String);}
            VerifyProtocol();
            File.Move(PendingReceiptPath,ReceiptPath);
        } catch {
            // Keep recovery material if exact rollback cannot be proved. A
            // later explicit register/unregister can inspect the same receipt.
            if(File.Exists(PendingReceiptPath)){
                try{RecoverPendingRegistration();}catch{}
            }
            throw;
        } finally{if(File.Exists(temp))File.Delete(temp);}
    }

    static void VerifyOwnership() {
        OperationStage="private_directory";
        CheckPrivateDirectory();
        OperationStage="receipt_read";
        var receipt=Json.Deserialize<Dictionary<string,object>>(Encoding.UTF8.GetString(SafeRead(ReceiptPath,65536)));
        OperationStage="registration_compare";
        ValidateReceipt(receipt);
        if((string)receipt["shortcut_hash"]!=Hash(SafeRead(Shortcut,65536)))throw new IOException("changed_notification_shortcut");
        OperationStage="registry_verify";VerifyProtocol();RegistrationVerified=true;
    }

    static void Unregister() {
        if(!File.Exists(ReceiptPath)&&File.Exists(PendingReceiptPath)){CheckPrivateDirectory();RecoverPendingRegistration();return;}
        VerifyOwnership();
        // Only our own AUMID history is removed. Windows user settings are kept.
        try { ToastNotificationManager.History.Clear(Aumid); } catch(Exception e) { if(e.HResult!=unchecked((int)0x80070490))throw; }
        VerifyOwnership();
        // The same recovery receipt handles interruption during removal.
        File.Move(ReceiptPath,PendingReceiptPath);
        RecoverPendingRegistration();
    }

    static void VerifyProtocol() {
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(RegistryPath)){
            if(key==null||key.GetValueNames().Length!=3||key.GetSubKeyNames().Length!=0||(string)key.GetValue("DisplayName")!="老底"||(string)key.GetValue("IconUri")!=StableLogo||(string)key.GetValue("CustomActivator")!="{"+ActivatorClsid.ToString("D")+"}")throw new IOException("changed_notification_identity");
            CheckExternalRegistryKey(key,@"AppUserModelId\dev.laodi.guardian");
            foreach(string name in key.GetValueNames()){if(key.GetValueKind(name)!=RegistryValueKind.String)throw new IOException("changed_notification_value_type");}
        }
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(ComRegistryPath)){
            if(key==null||key.GetValueNames().Length!=0||key.GetSubKeyNames().Length!=1||key.GetSubKeyNames()[0]!="LocalServer32")throw new IOException("changed_notification_activator");
            CheckExternalRegistryKey(key,@"CLSID\{"+ActivatorClsid.ToString("D")+"}");
            using(RegistryKey command=key.OpenSubKey("LocalServer32")){if(command.GetSubKeyNames().Length!=0||command.GetValueNames().Length!=1||command.GetValueKind("")!=RegistryValueKind.String||(string)command.GetValue("")!=Command)throw new IOException("changed_notification_activator_command");}
        }
    }

    internal static bool IsExternalRegistryPath(string actual,string relative){
        return String.Equals(actual,@"\Registry\User\"+CurrentUser.Value+@"_Classes\"+relative,StringComparison.OrdinalIgnoreCase);
    }

    static void CheckExternalRegistryKey(RegistryKey key,string relative){
        // Reading an existing parent key is insufficient: MSIX may redirect
        // only newly written child keys, even with no process package identity.
        OperationStage="registration_registry_mapping";
        uint needed;
        NtQueryKey(key.Handle,3,IntPtr.Zero,0,out needed);
        if(needed<4||needed>65536)throw new IOException("registry_mapping_query_failed");
        IntPtr buffer=Marshal.AllocHGlobal((int)needed);
        try{
            int status=NtQueryKey(key.Handle,3,buffer,needed,out needed);
            int bytes=status==0?Marshal.ReadInt32(buffer):-1;
            if(status!=0||bytes<0||bytes>needed-4||(bytes&1)!=0)throw new IOException("registry_mapping_query_failed");
            string actual=Marshal.PtrToStringUni(IntPtr.Add(buffer,4),bytes/2);
            if(!IsExternalRegistryPath(actual,relative))throw new IOException("notification_registration_redirected_use_normal_windows_terminal");
        }finally{Marshal.FreeHGlobal(buffer);}
    }

    static Dictionary<string,object> NewReceipt(string shortcutHash,string iconHash) {
        return new Dictionary<string,object>{{"schema_version",2},{"aumid",Aumid},{"launcher",Launcher},{"launcher_hash",Hash(SafeRead(Launcher,64*1024*1024))},{"logo_hash",Hash(SafeRead(StableLogo,4*1024*1024))},{"icon_hash",iconHash},{"shortcut_hash",shortcutHash},{"activator_command",Command},{"activator_clsid",ActivatorClsid.ToString("D")}};
    }

    static void ValidateReceipt(Dictionary<string,object> receipt,bool allowMissingIcon=false) {
        if(receipt==null||receipt.Count!=9||Convert.ToInt32(receipt["schema_version"])!=2||(string)receipt["aumid"]!=Aumid||(string)receipt["launcher"]!=Launcher||(string)receipt["launcher_hash"]!=Hash(SafeRead(Launcher,64*1024*1024))||(string)receipt["logo_hash"]!=Hash(SafeRead(StableLogo,4*1024*1024))||(!allowMissingIcon||File.Exists(ShortcutIcon))&&(string)receipt["icon_hash"]!=Hash(SafeRead(ShortcutIcon,1024*1024))||(string)receipt["activator_command"]!=Command||(string)receipt["activator_clsid"]!=ActivatorClsid.ToString("D")||!Regex.IsMatch((string)receipt["shortcut_hash"],@"\A[0-9a-f]{64}\z")||!Regex.IsMatch((string)receipt["icon_hash"],@"\A[0-9a-f]{64}\z"))throw new IOException("changed_notification_registration");
    }

    static void RecoverPendingRegistration() {
        if(File.Exists(ReceiptPath))throw new IOException("ambiguous_notification_receipts");
        var receipt=Json.Deserialize<Dictionary<string,object>>(Encoding.UTF8.GetString(SafeRead(PendingReceiptPath,65536)));
        ValidateReceipt(receipt,true);
        if(File.Exists(Shortcut)&&(string)receipt["shortcut_hash"]!=Hash(SafeRead(Shortcut,65536)))throw new IOException("changed_pending_shortcut");
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(RegistryPath)){
            if(key!=null){
                if(key.GetSubKeyNames().Length!=0)throw new IOException("changed_pending_identity");
                var expected=new Dictionary<string,string>{{"DisplayName","老底"},{"IconUri",StableLogo},{"CustomActivator","{"+ActivatorClsid.ToString("D")+"}"}};
                foreach(string name in key.GetValueNames()){if(!expected.ContainsKey(name)||key.GetValueKind(name)!=RegistryValueKind.String||(string)key.GetValue(name)!=expected[name])throw new IOException("changed_pending_identity");}
            }
        }
        using(RegistryKey key=Registry.CurrentUser.OpenSubKey(ComRegistryPath)){
            if(key!=null){
                if(key.GetValueNames().Length!=0||key.GetSubKeyNames().Length>1||key.GetSubKeyNames().Length==1&&key.GetSubKeyNames()[0]!="LocalServer32")throw new IOException("changed_pending_activator");
                using(RegistryKey command=key.OpenSubKey("LocalServer32")){if(command!=null&&(command.GetSubKeyNames().Length!=0||command.GetValueNames().Length>1||command.GetValueNames().Length==1&&(command.GetValueNames()[0]!=""||command.GetValueKind("")!=RegistryValueKind.String||(string)command.GetValue("")!=Command)))throw new IOException("changed_pending_command");}
            }
        }
        // Every surviving artifact was checked before any deletion. A partial
        // owned install can be rolled back without removing foreign siblings.
        if(File.Exists(Shortcut))File.Delete(Shortcut);
        Registry.CurrentUser.DeleteSubKeyTree(RegistryPath,false);
        Registry.CurrentUser.DeleteSubKeyTree(ComRegistryPath,false);
        if(File.Exists(ShortcutIcon))File.Delete(ShortcutIcon);
        File.Delete(PendingReceiptPath);
    }

    internal static readonly ManualResetEvent Activated=new ManualResetEvent(false);
    static void RunActivator(){
        using(ComApartment.Enter()){
        var factory=new NotificationFactory();
        IntPtr pointer=Marshal.GetComInterfaceForObject(factory,typeof(INotificationClassFactory));
        uint cookie;
        Guid id=ActivatorClsid;
        try{Marshal.ThrowExceptionForHR(CoRegisterClassObject(ref id,pointer,4,1,out cookie));}finally{Marshal.Release(pointer);}
        // Keep the local server alive for its entire bounded activation window.
        // Exiting immediately when Activate signals would race the RPC return.
        try{using(var lifetime=new ManualResetEvent(false)){lifetime.WaitOne(TimeSpan.FromSeconds(15));}}finally{CoRevokeClassObject(cookie);}
        GC.KeepAlive(factory);
        }
    }
    static void TestActivation(){
        // Only the already verified owned CLSID is activated. This exercises
        // SCM -> stable launcher -> selected payload -> COM callback, without
        // creating a ToastNotifier, querying permissions or submitting a toast.
        using(ComApartment.Enter()){
            OperationStage="activation_create";
            Guid id=ActivatorClsid,wanted=typeof(ILaodiNotificationActivation).GUID;
            IntPtr pointer;
            Marshal.ThrowExceptionForHR(CoCreateInstance(ref id,IntPtr.Zero,4,ref wanted,out pointer));
            object instance=null;
            try{
                instance=Marshal.GetObjectForIUnknown(pointer);
                OperationStage="activation_callback";
                ((ILaodiNotificationActivation)instance).Activate(Aumid,ActivationUri,IntPtr.Zero,0);
            }finally{if(instance!=null&&Marshal.IsComObject(instance))Marshal.FinalReleaseComObject(instance);Marshal.Release(pointer);}
        }
    }
    [DllImport("ole32.dll")]static extern int CoRegisterClassObject(ref Guid clsid,IntPtr factory,uint context,uint flags,out uint cookie);
    [DllImport("ole32.dll")]static extern int CoRevokeClassObject(uint cookie);
    [DllImport("ole32.dll")]static extern int CoCreateInstance(ref Guid clsid,IntPtr outer,uint context,ref Guid interfaceId,out IntPtr instance);

    static void CheckPrivateDirectory() {
        CheckAncestors(StateDirectory);
        DriveInfo drive=new DriveInfo(Path.GetPathRoot(StateDirectory));
        if(drive.DriveType!=DriveType.Fixed||(drive.DriveFormat!="NTFS"&&drive.DriveFormat!="ReFS"))throw new IOException("private_local_volume_required");
        DirectorySecurity security=Directory.GetAccessControl(StateDirectory);
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
            StringBuilder finalPath=new StringBuilder(32768);
            uint length=GetFinalPathNameByHandle(stream.SafeFileHandle,finalPath,32768,0);
            if(length==0||length>=32768)throw new IOException("notification_file_mapping_query_failed");
            if(!String.Equals(finalPath.ToString(),@"\\?\"+Path.GetFullPath(path),StringComparison.OrdinalIgnoreCase)){
                OperationStage="notification_file_mapping";
                throw new IOException("notification_files_redirected_use_normal_windows_terminal");
            }
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
            link.SetPath(Launcher);link.SetArguments("--notification-open");link.SetWorkingDirectory(StateDirectory);link.SetDescription("老底本地监测提醒");link.SetIconLocation(ShortcutIcon,0);
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
    [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern uint GetFinalPathNameByHandle(SafeFileHandle handle,StringBuilder name,uint size,uint flags);
    [DllImport("ntdll.dll")] static extern int NtQueryKey(SafeRegistryHandle key,int information,IntPtr output,uint length,out uint needed);
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
        if(appUserModelId!="dev.laodi.guardian"||arguments!="status"||count!=0)throw new ArgumentException("invalid_notification_activation");
        Program.Activated.Set();
    }
}

internal sealed class ComApartment:IDisposable {
    internal static ComApartment Enter(){Marshal.ThrowExceptionForHR(CoInitializeEx(IntPtr.Zero,0));return new ComApartment();}
    public void Dispose(){CoUninitialize();}
    [DllImport("ole32.dll")]static extern int CoInitializeEx(IntPtr reserved,uint flags);
    [DllImport("ole32.dll")]static extern void CoUninitialize();
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

internal sealed class RegistrationMutex:IDisposable {
    readonly Mutex mutex;
    RegistrationMutex(Mutex value){mutex=value;}
    internal static RegistrationMutex Acquire(){
        SecurityIdentifier user=WindowsIdentity.GetCurrent().User;
        var security=new MutexSecurity();security.SetAccessRuleProtection(true,false);
        security.AddAccessRule(new MutexAccessRule(user,MutexRights.FullControl,AccessControlType.Allow));
        security.AddAccessRule(new MutexAccessRule(new SecurityIdentifier(WellKnownSidType.LocalSystemSid,null),MutexRights.FullControl,AccessControlType.Allow));
        bool created;
        Mutex mutex=new Mutex(false,@"Local\Laodi.Notifications."+user.Value,out created,security);
        bool acquired=false;
        try{try{acquired=mutex.WaitOne(5000);}catch(AbandonedMutexException){acquired=true;}
            if(!acquired)throw new IOException("notification_management_busy");return new RegistrationMutex(mutex);
        }catch{mutex.Dispose();throw;}
    }
    public void Dispose(){mutex.ReleaseMutex();mutex.Dispose();}
}

internal sealed class NotificationOutcome {
    internal bool OK;
    internal string Delivery="not_requested",Authorization="unknown",Error,AuthorizationError,Activation;
}

// Tested without calling notification APIs. WinRT can project a failing
// HRESULT as System.Exception rather than COMException; do not silently treat
// a failed permission lookup as disabled or as a successful lookup.
internal static class NotificationEngine {
    internal static NotificationOutcome QuerySetting(Func<string> query){
        var result=new NotificationOutcome{OK=true};
        try{result.Authorization=query();}
        catch(Exception e){RethrowFatal(e);result.AuthorizationError="0x"+e.HResult.ToString("X8");}
        return result;
    }
    internal static NotificationOutcome Deliver(Func<string> query,Action show){
        NotificationOutcome result=QuerySetting(query);
        result.OK=false;result.Delivery="not_delivered";
        if(result.Authorization!="enabled"&&result.Authorization!="unknown") {result.Error="notifications_disabled";return result;}
        try{show();result.OK=true;result.Delivery="accepted_by_os";}
        catch(Exception e){RethrowFatal(e);result.Error="notification_show_failed:0x"+e.HResult.ToString("X8");}
        return result;
    }
    static void RethrowFatal(Exception e){if(e is OutOfMemoryException||e is StackOverflowException||e is ThreadAbortException)throw e;}
}
