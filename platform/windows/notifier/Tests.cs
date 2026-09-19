using System;
using System.IO;
using System.Runtime.InteropServices;
using Windows.Data.Xml.Dom;

// Native protocol tests use fake delegates exclusively. They never register an
// application, create a scheduled task or invoke ToastNotifier.Show.
internal static class NotifierProtocolTests {
    static int assertions;
    static void Assert(bool value,string message){assertions++;if(!value)throw new Exception(message);}
    static void Reject(Action action,string message){bool rejected=false;try{action();}catch(ArgumentException){rejected=true;}Assert(rejected,message);}
    static int Main(){
        try{
            string root=AppDomain.CurrentDomain.BaseDirectory.TrimEnd(Path.DirectorySeparatorChar);
            string[] args=Program.Configure(new string[]{"--state-dir",root,"--test-template"});
            Assert(args.Length==1&&args[0]=="--test-template","state argument parsing");
            Reject(()=>Program.Configure(new string[]{"--send"}),"state root is mandatory");
            foreach(string bad in new string[]{"","\r\n","SYNTHETIC secret content","秘密",new string('a',129)})Reject(()=>Program.ValidateEventId(bad),"reject invalid event ID");
            Program.ValidateEventId(new string('a',128));
            string[] kinds={"test","snapshot-history","upload-attempt","upload-accepted","snapshot-workspace","workspace-upload-attempt","workspace-upload-accepted","snapshot-config","config-upload-attempt","config-upload-accepted","tool-output-sensitive","hook-coverage-degraded","coverage-degraded"};
            foreach(string kind in kinds){
                XmlDocument content=Program.Content(kind,"synthetic-id");
                Assert(content.DocumentElement.GetAttribute("launch")=="status","fixed COM activation");
                Assert(content.GetElementsByTagName("text").Count==2,"two fixed text fields");
                Assert(content.GetElementsByTagName("binding")[0].Attributes.GetNamedItem("template").NodeValue.ToString()=="ToastGeneric","supported desktop template");
                Assert(content.GetElementsByTagName("image").Count==1,"product logo present");
                Assert(content.InnerText.IndexOf("synthetic-id",StringComparison.Ordinal)<0,"event ID never becomes notice copy");
            }
            Reject(()=>Program.Content("untrusted title","synthetic-id"),"arbitrary text rejected");
            int showCalls=0;
            NotificationOutcome accepted=NotificationEngine.Deliver(()=>"enabled",()=>showCalls++);
            Assert(showCalls==1&&accepted.OK&&accepted.Delivery=="accepted_by_os","acceptance follows successful API call");
            foreach(string disabled in new string[]{"disabled_for_application","disabled_for_user","disabled_by_policy","disabled_by_manifest"}){
                showCalls=0;NotificationOutcome outcome=NotificationEngine.Deliver(()=>disabled,()=>showCalls++);
                Assert(showCalls==0&&!outcome.OK&&outcome.Delivery=="not_delivered"&&outcome.Authorization==disabled,"disabled state does not invoke Show");
            }
            var projected=new ProjectedPlatformException(unchecked((int)0x80070490));
            NotificationOutcome unknown=NotificationEngine.QuerySetting(()=>{throw projected;});
            Assert(unknown.OK&&unknown.Authorization=="unknown"&&unknown.AuthorizationError=="0x80070490"&&unknown.Delivery=="not_requested","WinRT System.Exception retained as unknown");
            showCalls=0;
            NotificationOutcome unknownAccepted=NotificationEngine.Deliver(()=>{throw projected;},()=>showCalls++);
            Assert(showCalls==1&&unknownAccepted.OK&&unknownAccepted.Authorization=="unknown"&&unknownAccepted.AuthorizationError=="0x80070490","unknown lookup never masquerades as enabled");
            NotificationOutcome failed=NotificationEngine.Deliver(()=>"enabled",()=>{throw new ProjectedPlatformException(unchecked((int)0x80070005));});
            Assert(!failed.OK&&failed.Delivery=="not_delivered"&&failed.Error.EndsWith("0x80070005"),"failed Show never accepted");
            var factory=new NotificationFactory();Guid wanted=typeof(ILaodiNotificationActivation).GUID;IntPtr pointer;
            Assert(factory.CreateInstance(IntPtr.Zero,ref wanted,out pointer)==0&&pointer!=IntPtr.Zero,"COM callback interface available");Marshal.Release(pointer);
            Guid absent=Guid.NewGuid();Assert(factory.CreateInstance(IntPtr.Zero,ref absent,out pointer)!=0&&pointer==IntPtr.Zero,"unknown COM interface rejected");
            var callback=new NotificationActivation();
            Reject(()=>callback.Activate("foreign-app","status",IntPtr.Zero,0),"foreign AUMID rejected");
            Reject(()=>callback.Activate("dev.laodi.guardian","untrusted",IntPtr.Zero,0),"untrusted activation rejected");
            Reject(()=>callback.Activate("dev.laodi.guardian","status",new IntPtr(1),1),"input payload rejected without dereference");
            callback.Activate("dev.laodi.guardian","status",IntPtr.Zero,0);
            Assert(Program.Activated.WaitOne(0),"fixed callback completes");
            string ownClasses=@"\Registry\User\"+System.Security.Principal.WindowsIdentity.GetCurrent().User.Value+@"_Classes\";
            Assert(Program.IsExternalRegistryPath(ownClasses+@"AppUserModelId\dev.laodi.guardian",@"AppUserModelId\dev.laodi.guardian"),"ordinary user registry hive accepted");
            Assert(!Program.IsExternalRegistryPath(@"\Registry\User\S-1-5-18_Classes\CLSID\synthetic",@"CLSID\synthetic"),"foreign user registry hive rejected");
            Assert(!Program.IsExternalRegistryPath(@"\Registry\WC\SyntheticPackage\CLSID\synthetic",@"CLSID\synthetic"),"private virtual registry hive rejected");
            Console.WriteLine("{\"schema_version\":1,\"tests_passed\":true,\"assertions\":"+assertions+",\"notification_api_calls\":0,\"registration_changes\":0}");return 0;
        }catch(Exception e){Console.Error.WriteLine(e.GetType().Name+": "+e.Message);return 1;}
    }
    sealed class ProjectedPlatformException:Exception{internal ProjectedPlatformException(int code){HResult=code;}}
}
