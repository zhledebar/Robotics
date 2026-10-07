$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
$request = $env:PRICE_MONITOR_UIA_REQUEST | ConvertFrom-Json
function Write-ReaderStage([string]$name) {
 $bytes=[System.Text.Encoding]::UTF8.GetBytes($name)
 [Console]::WriteLine('PSM_UIA_STAGE:'+$env:PRICE_MONITOR_UIA_TOKEN+':'+[Convert]::ToBase64String($bytes))
}
$stage = '加载'
Write-ReaderStage '加载 Windows 读取组件' 
try {
 Add-Type -AssemblyName UIAutomationClient
 Add-Type -AssemblyName UIAutomationTypes
 Add-Type -AssemblyName WindowsBase
 Add-Type -AssemblyName Accessibility
 $refs = @([System.Windows.Automation.AutomationElement].Assembly.Location,[System.Windows.Automation.ControlType].Assembly.Location,[System.Windows.Point].Assembly.Location,[System.Diagnostics.Process].Assembly.Location,[System.Collections.Generic.HashSet[int]].Assembly.Location,[Accessibility.IAccessible].Assembly.Location)
 $stage = '编译'
 Write-ReaderStage '编译内置读取组件'
 $readerSource = @'
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using System.Windows.Automation;
using Accessibility;
public class PMNode {
 public string name, id, kind, reading_text;
 public string @class;
 public bool enabled, selected;
 public bool? expanded;
 public List<PMNode> children = new List<PMNode>();
}
public class PMPage {
 public long handle;
 public string url, title, error, diagnostic;
 public bool can_ordinary, can_custom, launched;
 public Dictionary<string,PMNode> regions = new Dictionary<string,PMNode>();
}
public static class PMReader {
 private static void Stage(string name) {
  Console.WriteLine("PSM_UIA_STAGE:"+Environment.GetEnvironmentVariable("PRICE_MONITOR_UIA_TOKEN")+":"+Convert.ToBase64String(Encoding.UTF8.GetBytes(name)));
 }
 private delegate bool EnumProc(IntPtr h, IntPtr p);
 private delegate bool PropEnumProc(IntPtr h,IntPtr name,IntPtr value,IntPtr data);
 [DllImport("user32.dll")] private static extern bool EnumWindows(EnumProc fn, IntPtr p);
 [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern int GetClassName(IntPtr h,StringBuilder s,int max);
 [DllImport("user32.dll")] private static extern bool IsWindow(IntPtr h);
 [DllImport("user32.dll")] private static extern bool IsIconic(IntPtr h);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern bool SetProp(IntPtr h,string key,IntPtr value);
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern IntPtr GetProp(IntPtr h,string key);
 [DllImport("user32.dll",EntryPoint="EnumPropsExW",ExactSpelling=true)] private static extern int EnumPropsEx(IntPtr h,PropEnumProc fn,IntPtr data);
 [DllImport("user32.dll",EntryPoint="PostMessageW",ExactSpelling=true)] private static extern bool PostMessage(IntPtr h,uint message,IntPtr w,IntPtr l);
 [DllImport("user32.dll")] private static extern IntPtr GetThreadDesktop(uint thread);
 [DllImport("kernel32.dll")] private static extern uint GetCurrentThreadId();
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] private static extern bool GetUserObjectInformation(IntPtr h,int index,StringBuilder text,uint length,out uint needed);
 [DllImport("kernel32.dll")] private static extern IntPtr GetStdHandle(int which);
 [DllImport("kernel32.dll")] private static extern bool SetHandleInformation(IntPtr h,uint mask,uint flags);
 [DllImport("kernel32.dll",EntryPoint="OpenJobObjectW",ExactSpelling=true,CharSet=CharSet.Unicode,SetLastError=true)] private static extern IntPtr OpenJobObject(uint access,bool inherit,string name);
 [DllImport("kernel32.dll",SetLastError=true)] private static extern IntPtr OpenProcess(uint access,bool inherit,uint pid);
 [DllImport("kernel32.dll",SetLastError=true)] private static extern bool IsProcessInJob(IntPtr process,IntPtr job,out bool inside);
 [DllImport("kernel32.dll")] private static extern bool CloseHandle(IntPtr h);
 [DllImport("oleacc.dll")] private static extern int AccessibleObjectFromWindow(IntPtr h,uint id,ref Guid iid,[MarshalAs(UnmanagedType.Interface)] out IAccessible accessible);
 [DllImport("oleacc.dll")] private static extern int AccessibleChildren(IAccessible parent,int start,int count,[Out,MarshalAs(UnmanagedType.LPArray,ArraySubType=UnmanagedType.Struct,SizeParamIndex=2)] object[] children,out int obtained);
 private static IntPtr ownedHandle;
 private static string expectedURL;
 private static bool expectedFamily;
 private static void PrivateDesktop() {
  string expected=Environment.GetEnvironmentVariable("PRICE_MONITOR_PRIVATE_DESKTOP");
  StringBuilder name=new StringBuilder(256);uint needed;
  if(String.IsNullOrEmpty(expected)||!expected.StartsWith("PriceStockMonitor_",StringComparison.Ordinal)||!GetUserObjectInformation(GetThreadDesktop(GetCurrentThreadId()),2,name,512,out needed)||name.ToString()!=expected)
   throw new Exception("后台采集桌面未核对；没有打开或操作前台浏览器");
 }
 // Tabs belong to this application only after both the private desktop and the
 // exact private job have been checked. A window tag alone is insufficient.
 private static void PrivateWindow(IntPtr h,string tag) {
  PrivateDesktop();
  if(!IsWindow(h)||!BrowserWindow(h)||String.IsNullOrEmpty(tag)||GetProp(h,tag)!=new IntPtr(1))throw new Exception("后台采集窗口身份未确认；没有操作其他窗口");
  string desktop=Environment.GetEnvironmentVariable("PRICE_MONITOR_PRIVATE_DESKTOP"),jobName=Environment.GetEnvironmentVariable("PRICE_MONITOR_PRIVATE_JOB");
  if(String.IsNullOrEmpty(jobName)||jobName!=desktop+"_Job")throw new Exception("后台进程组身份未确认；没有操作其他窗口");
  uint pid;GetWindowThreadProcessId(h,out pid);
  IntPtr job=OpenJobObject(4,false,jobName),process=OpenProcess(0x1000,false,pid);
  try {
   bool inside=false;
   if(job==IntPtr.Zero||process==IntPtr.Zero||!IsProcessInJob(process,job,out inside)||!inside)throw new Exception("窗口不属于本软件后台进程组；没有读取或关闭其他窗口");
  }finally {if(process!=IntPtr.Zero)CloseHandle(process);if(job!=IntPtr.Zero)CloseHandle(job);}
 }
 private class MSChoice {public IAccessible obj;public object child;}
 private static bool SameBounds(IAccessible obj,object child,System.Windows.Rect r) {
  int x,y,w,h;obj.accLocation(out x,out y,out w,out h,child);
  return w>0&&h>0&&Math.Abs(x-r.X)<=2&&Math.Abs(y-r.Y)<=2&&Math.Abs(w-r.Width)<=2&&Math.Abs(h-r.Height)<=2;
 }
 private static void FindMSAA(IAccessible obj,object child,string name,System.Windows.Rect rect,int depth,List<MSChoice> matches,ref int count) {
  Budget();if(++count>12000||depth>55)throw new Exception("后台控件兼容读取超过范围");
  try {
   int state=Convert.ToInt32(obj.get_accState(child)),role=Convert.ToInt32(obj.get_accRole(child));
   if(role==43&&(state&0x8001)==0&&obj.get_accName(child)==name&&!String.IsNullOrEmpty(obj.get_accDefaultAction(child))&&SameBounds(obj,child,rect)) matches.Add(new MSChoice{obj=obj,child=child});
  }catch(COMException) {}
  if(!(child is int)||((int)child)!=0)return;
  int n=obj.accChildCount;if(n<0||n>12000)throw new Exception("后台控件子项超过范围");
  if(n==0)return;object[] children=new object[n];int obtained;
  if(AccessibleChildren(obj,0,n,children,out obtained)<0)throw new Exception("后台控件兼容读取失败");
  for(int i=0;i<obtained;i++) {
   IAccessible a=children[i] as IAccessible;
   if(a!=null)FindMSAA(a,0,name,rect,depth+1,matches,ref count);
   else if(children[i] is int)FindMSAA(obj,children[i],name,rect,depth+1,matches,ref count);
  }
 }
 private static bool DefaultAction(AutomationElement e) {
  Stage("执行后台控件兼容操作");PrivateDesktop();Validate(ownedHandle,expectedURL,expectedFamily);
  System.Windows.Rect rect=e.Current.BoundingRectangle;string name=Name(e);
  if(rect.IsEmpty||rect.Width<=0||rect.Height<=0||String.IsNullOrEmpty(name))return false;
  Guid iid=new Guid("618736E0-3C3D-11CF-810C-00AA00389B71");IAccessible root;
  if(AccessibleObjectFromWindow(ownedHandle,0xFFFFFFFC,ref iid,out root)<0||root==null)return false;
  List<MSChoice> matches=new List<MSChoice>();int count=0;FindMSAA(root,0,name,rect,0,matches,ref count);
  if(matches.Count!=1)return false;
  MSChoice target=matches[0];
  // Revalidate the exact element, window, URL and private desktop immediately
  // before the default action. No cursor, keyboard or foreground-window APIs.
  Validate(ownedHandle,expectedURL,expectedFamily);PrivateDesktop();
  if(Name(e)!=name||!e.Current.IsEnabled||(Convert.ToInt32(target.obj.get_accState(target.child))&0x8001)!=0||target.obj.get_accName(target.child)!=name||!SameBounds(target.obj,target.child,e.Current.BoundingRectangle))return false;
  target.obj.accDoDefaultAction(target.child);return true;
 }
 private static TreeWalker walker = TreeWalker.ControlViewWalker;
 private static Stopwatch timer;
 private static int nodeCount;
 private static string ownerKey;
 private static void Budget() { if(timer.Elapsed.TotalSeconds > 43) throw new Exception("普通浏览器界面读取超时"); }
 private static bool BrowserWindow(IntPtr h) {
  StringBuilder s=new StringBuilder(100);GetClassName(h,s,100);
  if(!s.ToString().StartsWith("Chrome_WidgetWin_")) return false;
  uint pid;GetWindowThreadProcessId(h,out pid);
  try { string n=Process.GetProcessById((int)pid).ProcessName.ToLowerInvariant();return n=="chrome"||n=="msedge"; } catch {return false;}
 }
 private static List<IntPtr> Windows() {
  List<IntPtr> list=new List<IntPtr>(); EnumWindows(delegate(IntPtr h,IntPtr p) {if(BrowserWindow(h))list.Add(h);return true;},IntPtr.Zero);return list;
 }
 private static string MonitorTag(IntPtr h) {
  string tag=null;
  EnumPropsEx(h,delegate(IntPtr window,IntPtr name,IntPtr value,IntPtr data) {
   if(value!=new IntPtr(1)||name.ToInt64()<65536)return true;
   string key=Marshal.PtrToStringUni(name);
   if(key!=null&&key.StartsWith("PriceStockMonitor_",StringComparison.Ordinal)) {tag=key;return false;}return true;
  },IntPtr.Zero);return tag;
 }
 private static void CloseTagged(IntPtr h,string tag,string expected) {
  PrivateDesktop();if(!IsWindow(h))return;
  if(!BrowserWindow(h)||GetProp(h,tag)!=new IntPtr(1))throw new Exception("采集窗口标识已改变；没有关闭其他窗口");
  PrivateWindow(h,tag);
  AutomationElement root=AutomationElement.FromHandle(h);
  string actual=Address(root);
  if(!SameURL(expected,actual,true))throw new Exception("采集窗口已切换其他商品；没有关闭或另开窗口");
  if(!IsWindow(h))return;
  PrivateWindow(h,tag);
  if(GetProp(h,tag)!=new IntPtr(1)||!PostMessage(h,0x0010,IntPtr.Zero,IntPtr.Zero))throw new Exception("无法关闭旧采集窗口；没有打开更多窗口");
  for(int i=0;i<50&&IsWindow(h);i++) {Thread.Sleep(100);Budget();}
  if(IsWindow(h))throw new Exception("旧采集窗口尚未关闭；没有打开更多窗口");
 }
 private static void CleanupOldWindows(bool currentOwnerOnly) {
  foreach(IntPtr h in Windows()) {
   Budget();string tag=MonitorTag(h);if(tag==null)continue;
   if(currentOwnerOnly&&tag!=ownerKey)continue;
   // Reclaim only a window in this application's exact private job. Daily
   // browser windows remain outside the private desktop and job.
   try {PrivateWindow(h,tag);}catch {continue;}
   AutomationElement root=AutomationElement.FromHandle(h);
   string actual=Address(root);if(!SameURL(actual,actual,true))continue;
   CloseTagged(h,tag,actual);
  }
 }
 private static AutomationElement Child(AutomationElement e) {return walker.GetFirstChild(e);}
 private static AutomationElement Next(AutomationElement e) {return walker.GetNextSibling(e);}
 private static string Name(AutomationElement e) {return e.Current.Name??"";}
 private static string ID(AutomationElement e) {return e.Current.AutomationId??"";}
 private static List<AutomationElement> BrowserControls(AutomationElement root) {
  List<AutomationElement> found=new List<AutomationElement>();Stack<AutomationElement> todo=new Stack<AutomationElement>();todo.Push(root);int count=0;
  while(todo.Count>0) {Budget();AutomationElement e=todo.Pop();if(++count>1500)throw new Exception("普通浏览器工具栏超过读取范围");if(e.Current.ControlType==ControlType.Document)continue;found.Add(e);for(AutomationElement c=Child(e);c!=null;c=Next(c))todo.Push(c);}
  return found;
 }
 private static string Address(AutomationElement root) {
  foreach(AutomationElement e in BrowserControls(root)) {
   if(e.Current.ControlType!=ControlType.Edit)continue;
   string n=Name(e),id=ID(e);if(!Regex.IsMatch(n+" "+id,"address|search bar|地址|omnibox",RegexOptions.IgnoreCase))continue;
   object p;string value="";
   if(e.TryGetCurrentPattern(ValuePattern.Pattern,out p)) value=((ValuePattern)p).Current.Value;
   value=(value??"").Trim();if(value.StartsWith("www.dell.com/",StringComparison.OrdinalIgnoreCase))value="https://"+value;
   if(value.StartsWith("dell.com/",StringComparison.OrdinalIgnoreCase))value="https://www."+value;
   if(!String.IsNullOrEmpty(value))return value;
  }
  throw new Exception("普通浏览器地址栏未识别；没有使用其他窗口内容");
 }
 private static bool SameURL(string expected,string actual,bool family) {
  Uri a,b;if(!Uri.TryCreate(expected,UriKind.Absolute,out a)||!Uri.TryCreate(actual,UriKind.Absolute,out b))return false;
  if(a.Scheme!="https"||b.Scheme!="https"||a.Host!="www.dell.com"||b.Host!=a.Host||b.UserInfo!=""||!b.IsDefaultPort)return false;
  if(!a.AbsolutePath.StartsWith("/en-us/shop/")||!b.AbsolutePath.StartsWith("/en-us/shop/"))return false;
  Match am=Regex.Match(a.AbsolutePath,@"/spd/([^/]+)"),bm=Regex.Match(b.AbsolutePath,@"/spd/([^/]+)");
  if(!am.Success||!bm.Success||!am.Groups[1].Value.Equals(bm.Groups[1].Value,StringComparison.OrdinalIgnoreCase))return false;
  if(family)return true;string ar=Regex.Match(a.AbsolutePath,@"[a-z0-9]+_(?:fixed|reg|so|cto|sb|custom)_[a-z0-9_]+",RegexOptions.IgnoreCase).Value;
  string br=Regex.Match(b.AbsolutePath,@"[a-z0-9]+_(?:fixed|reg|so|cto|sb|custom)_[a-z0-9_]+",RegexOptions.IgnoreCase).Value;
  return ar!=""&&ar.Equals(br,StringComparison.OrdinalIgnoreCase);
 }
 private static AutomationElement Validate(IntPtr h,string expected,bool family) {
  Budget();if(!IsWindow(h))throw new Exception("监控商品窗口已关闭");if(!BrowserWindow(h)||GetProp(h,ownerKey)!=new IntPtr(1))throw new Exception("监控窗口标识已改变；没有操作其他窗口");
  PrivateWindow(h,ownerKey);
  if(IsIconic(h))throw new Exception("后台采集窗口意外最小化；未恢复任何前台窗口");
  AutomationElement root=AutomationElement.FromHandle(h);
  if(!SameURL(expected,Address(root),family))throw new Exception("监控商品窗口已切换网页；没有读取其他页面");return root;
 }
 private static AutomationElement Document(AutomationElement root) {
  // Only one exposed top-level web document may supply the quote. Stop at
  // each Document: embedded frames and webpage tab widgets are not siblings.
  AutomationElement document=null;Stack<AutomationElement> todo=new Stack<AutomationElement>();todo.Push(root);int count=0;
  while(todo.Count>0) {
   Budget();AutomationElement e=todo.Pop();if(++count>1500)throw new Exception("后台文档范围超过读取预算");
   if(e.Current.ControlType==ControlType.Document) {
    if(document!=null)throw new Exception("后台当前商品文档不唯一；本轮没有接受报价");
    document=e;continue;
   }
   for(AutomationElement c=Child(e);c!=null;c=Next(c))todo.Push(c);
  }
  return document;
 }
 private static void CheckBlocked(AutomationElement document) {
  if(document==null)throw new Exception("普通浏览器商品页面尚未加载");
  string s=Name(document);
  if(Regex.IsMatch(s,"access denied|verify you are human|checking your browser|just a moment",RegexOptions.IgnoreCase))throw new Exception("普通浏览器网页访问被限制（access denied）；已停止本轮");
  AutomationElementCollection texts=document.FindAll(TreeScope.Children,new PropertyCondition(AutomationElement.ControlTypeProperty,ControlType.Text));
  for(int i=0;i<Math.Min(texts.Count,15);i++)s+=" "+Name(texts[i]);
  if(Regex.IsMatch(s,"access denied|you don.t have permission to access|verify you are human|checking your browser",RegexOptions.IgnoreCase))throw new Exception("普通浏览器网页访问被限制（access denied）；已停止本轮");
 }
 private static AutomationElement ByID(AutomationElement document,string id) {
  CacheRequest lookup=new CacheRequest();lookup.TreeScope=TreeScope.Element;lookup.TreeFilter=Automation.RawViewCondition;
  using(lookup.Activate()) {return document.FindFirst(TreeScope.Descendants,new PropertyCondition(AutomationElement.AutomationIdProperty,id));}
 }
 private static string RegionText(AutomationElement doc,AutomationElement region) {
  // Never read DocumentRange: fallback text must remain inside this own region.
  try {
   object pattern;if(!doc.TryGetCurrentPattern(TextPattern.Pattern,out pattern))return "";
   var range=((TextPattern)pattern).RangeFromChild(region);
   string text=range.GetText(32769)??"";
   if(text.Length>32768)return ""; // A truncated price block is not evidence.
   return text;
  }catch {return "";} // Unsupported range must not disable the existing tree path.
 }
 private static PMNode SnapshotNode(AutomationElement e,int depth) {
  Budget();if(++nodeCount>9000||depth>48)throw new Exception("商品区域读取不完整；没有接受截断报价");
  var c=e.Cached;
  // Raw view retains author-ID containers. Only control-visible content may
  // supply text/availability: hidden previous views must not supply old quotes.
  PMNode n=new PMNode();n.name=c.IsControlElement?(c.Name??""):"";n.id=c.AutomationId??"";n.@class=c.ClassName??"";n.enabled=c.IsControlElement&&c.IsEnabled;n.kind=c.ControlType.ProgrammaticName.Replace("ControlType.","");
  // Dell's configuration cards use HTML role="button". Chromium can expose
  // them as ControlType.Custom, and the visible name is not guaranteed to
  // contain either "Selected" or a price delta. Use the control patterns as
  // the primary semantic test; the name/class suffixes remain fallbacks for
  // Chromium/UIA builds that do not expose a selection pattern.
  bool semanticChoice=c.ControlType==ControlType.Button||c.ControlType==ControlType.CheckBox||c.ControlType==ControlType.RadioButton;
  if(n.kind=="Custom") {
   object choicePattern;bool hasChoicePattern=e.TryGetCurrentPattern(InvokePattern.Pattern,out choicePattern);
   if(!hasChoicePattern)hasChoicePattern=e.TryGetCurrentPattern(SelectionItemPattern.Pattern,out choicePattern);
   if(!hasChoicePattern)hasChoicePattern=e.TryGetCurrentPattern(TogglePattern.Pattern,out choicePattern);
   if(hasChoicePattern||Regex.IsMatch(n.name, @"(?:^|[.\s])(?:Selected|[+−–-]\s*\$\s*[0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*$", RegexOptions.IgnoreCase)||Regex.IsMatch(n.@class, @"(?:^|[\s_-])(selected|active|checked)(?:$|[\s_-])", RegexOptions.IgnoreCase)) {
    n.kind="Button";semanticChoice=true;
   }
  }
  if(c.IsPassword||c.ControlType==ControlType.Edit)return null;
  if(!c.IsControlElement&&(semanticChoice||c.ControlType==ControlType.Text))return null;
  object p;
  if(n.id.StartsWith("label-module")) {
   if(e.TryGetCurrentPattern(ExpandCollapsePattern.Pattern,out p))n.expanded=((ExpandCollapsePattern)p).Current.ExpandCollapseState==ExpandCollapseState.Expanded;
   else if(Regex.IsMatch(n.@class,@"\baccordion-button\b"))n.expanded=!Regex.IsMatch(n.@class,@"\bcollapsed\b");
  }
  if(semanticChoice) {
   if(e.TryGetCurrentPattern(TogglePattern.Pattern,out p))n.selected=((TogglePattern)p).Current.ToggleState==ToggleState.On;
   if(e.TryGetCurrentPattern(SelectionItemPattern.Pattern,out p))n.selected=n.selected||((SelectionItemPattern)p).Current.IsSelected;
  }
  if(semanticChoice&&(Regex.IsMatch(n.name,@"(?:^|[.\s])Selected\s*$",RegexOptions.IgnoreCase)||Regex.IsMatch(n.@class,@"(?:^|[\s_-])(selected|active|checked)(?:$|[\s_-])",RegexOptions.IgnoreCase)))n.selected=true;
  foreach(AutomationElement child in e.CachedChildren) {PMNode q=SnapshotNode(child,depth+1);if(q!=null)n.children.Add(q);}
  if(!c.IsControlElement&&n.children.Count==0)return null;
  return n;
 }
 private static AutomationElement ActionButton(AutomationElement doc,string action) {
  AutomationElement scope=ByID(doc,"configuration-section");if(scope==null)scope=ByID(doc,"offers-container");if(scope==null)return null;
  if(action=="custom") {
   // Dell uses a clickable div for this entry, rather than a semantic button.
   AutomationElement customize=ByID(scope,"btn-customize-now");
   if(customize!=null&&customize.Current.IsControlElement&&customize.Current.IsEnabled) {
    return customize;
   }
  }
  AutomationElementCollection buttons=scope.FindAll(TreeScope.Descendants,new AndCondition(Automation.ControlViewCondition,new PropertyCondition(AutomationElement.ControlTypeProperty,ControlType.Button)));
  foreach(AutomationElement e in buttons) {string name=Name(e).Trim();if(!e.Current.IsEnabled)continue;
   if(action=="ordinary"&&Regex.IsMatch(name,@"^View other configurations(?:\s|$)",RegexOptions.IgnoreCase))return e;
   if(action=="custom"&&Regex.IsMatch(name,@"\bCustomize now\b",RegexOptions.IgnoreCase))return e;
  }return null;
 }
 private static void Invoke(AutomationElement e) {
  object p;if(e.TryGetCurrentPattern(InvokePattern.Pattern,out p)){((InvokePattern)p).Invoke();return;}
  if(e.TryGetCurrentPattern(SelectionItemPattern.Pattern,out p)){((SelectionItemPattern)p).Select();return;}
  if(e.TryGetCurrentPattern(TogglePattern.Pattern,out p)) {var toggle=(TogglePattern)p;if(toggle.Current.ToggleState==ToggleState.On)return;if(toggle.Current.ToggleState==ToggleState.Off){toggle.Toggle();return;}throw new Exception("后台控件切换状态不明确；未猜测所选值");}
  if(DefaultAction(e))return;
  throw new Exception("后台控件未提供可核对的操作接口；未操作前台键盘或鼠标");
 }
 private static PMPage PendingPage(IntPtr h,string expected,bool family,string reason) {
  // Missing content is retryable only after the private window and URL have
  // been verified. Preserve that identity instead of returning an empty URL.
  AutomationElement root=Validate(h,expected,family);
  return new PMPage{handle=h.ToInt64(),url=Address(root),error=reason,diagnostic="已核对独立桌面、窗口标记、专用进程组和商品网址；"+reason};
 }
 private static PMPage Capture(IntPtr h,string expected,bool family) {
  Stage("核对后台窗口和商品页面");
  AutomationElement root=Validate(h,expected,family),doc=Document(root);
  if(doc==null)return PendingPage(h,expected,family,"普通浏览器商品页面尚未加载");
  CheckBlocked(doc);string before=Address(root);
  PMPage page=new PMPage();page.handle=h.ToInt64();page.url=before;page.title=Name(doc);page.diagnostic="已核对独立桌面、窗口标记、专用进程组和唯一当前商品文档";nodeCount=0;
  CacheRequest cache=new CacheRequest();cache.TreeScope=TreeScope.Subtree;cache.TreeFilter=Automation.RawViewCondition;
  foreach(AutomationProperty prop in new AutomationProperty[]{AutomationElement.NameProperty,AutomationElement.AutomationIdProperty,AutomationElement.ClassNameProperty,AutomationElement.IsEnabledProperty,AutomationElement.ControlTypeProperty,AutomationElement.IsPasswordProperty,AutomationElement.IsControlElementProperty})cache.Add(prop);
  foreach(string id in new string[]{"hero-section","configuration-section","offers-container","add-to-cart-stack"}) {
   Stage("读取商品区域："+id);
   AutomationElement region=ByID(doc,id);if(region==null)continue;
   PMNode snapshot=SnapshotNode(region.GetUpdatedCache(cache),0);
   if(snapshot==null) snapshot=new PMNode();
   if(id=="hero-section"||id=="add-to-cart-stack")snapshot.reading_text=RegionText(doc,region);
   page.regions[id]=snapshot;
  }
  page.can_ordinary=ActionButton(doc,"ordinary")!=null;page.can_custom=ActionButton(doc,"custom")!=null;
  root=Validate(h,expected,family);if(!Address(root).Equals(before,StringComparison.Ordinal))throw new Exception("读取途中商品网址发生变化；本轮报价作废");
  if(page.regions.Count==0)return PendingPage(h,expected,family,"普通浏览器没有提供可核对的商品区域");return page;
 }
 public static PMPage Run(string action,long handle,string expected,bool family,string exe,string owner,string groupID,string optionName) {
  timer=Stopwatch.StartNew();ownerKey="PriceStockMonitor_"+owner;
  ownedHandle=new IntPtr(handle);expectedURL=expected;expectedFamily=family;
  IntPtr h=new IntPtr(handle);
  bool launched=false;
  try {
   Stage("核对独立后台桌面");PrivateDesktop();
   foreach(int std in new int[]{-10,-11,-12})SetHandleInformation(GetStdHandle(std),1,0);
   if(String.IsNullOrEmpty(owner))throw new Exception("缺少采集窗口标识");
   if(action=="cleanup") {CleanupOldWindows(true);return new PMPage();}
   if(String.IsNullOrEmpty(owner)||!SameURL(expected,expected,family)||expected.IndexOf('"')>=0||expected.IndexOf('\\')>=0||expected.IndexOf('\r')>=0||expected.IndexOf('\n')>=0)throw new Exception("无效的商品窗口请求；没有启动浏览器");
   if(action=="close") {CloseTagged(h,ownerKey,expected);return new PMPage();}
   if(action=="open") {
    if(String.IsNullOrEmpty(exe))throw new Exception("未找到本机 Chrome/Edge；不能自动打开普通商品窗口");
    CleanupOldWindows(false);
    HashSet<long> before=new HashSet<long>();foreach(IntPtr old in Windows())before.Add(old.ToInt64());
    string profile=Environment.GetEnvironmentVariable("PRICE_MONITOR_BROWSER_PROFILE");
    if(String.IsNullOrEmpty(profile)||profile.IndexOf('"')>=0)throw new Exception("后台浏览器目录无效；没有启动前台浏览器");
    ProcessStartInfo pi=new ProcessStartInfo(exe,"--user-data-dir=\""+profile+"\" --remote-debugging-port=0 --remote-debugging-address=127.0.0.1 --remote-allow-origins=http://localhost --no-first-run --no-default-browser-check --disable-background-mode --force-renderer-accessibility --new-window \""+expected+"\"");pi.UseShellExecute=false;pi.RedirectStandardOutput=true;pi.RedirectStandardError=true;pi.CreateNoWindow=true;
    Stage("启动后台商品浏览器");
    Process browserProcess=Process.Start(pi);launched=true;browserProcess.BeginOutputReadLine();browserProcess.BeginErrorReadLine();
    Stage("等待后台商品窗口");
    bool found=false;
    while(timer.Elapsed.TotalSeconds<30&&!found) {
     Thread.Sleep(500);foreach(IntPtr candidate in Windows()) {if(before.Contains(candidate.ToInt64()))continue;
      try {Stage("读取后台窗口地址栏");AutomationElement root=AutomationElement.FromHandle(candidate);if(SameURL(expected,Address(root),family)) {if(!SetProp(candidate,ownerKey,new IntPtr(1)))throw new Exception("无法记录监控窗口标识");h=candidate;found=true;break;}}catch{}
     }
    }
    ownedHandle=h;
    if(!found)throw new Exception("普通浏览器商品窗口打开超时；没有接管你的原有窗口");
   }else {
    Stage("读取后台商品界面");
    AutomationElement root=Validate(h,expected,family),doc=Document(root);
    if(doc==null)return PendingPage(h,expected,family,"普通浏览器商品页面尚未加载");
    CheckBlocked(doc);
    if(action=="validate")return new PMPage{handle=h.ToInt64(),url=Address(root),title=Name(doc)};
    if(action=="refresh") {
     AutomationElement reload=null;foreach(AutomationElement e in BrowserControls(root)) {if(e.Current.ControlType!=ControlType.Button||!e.Current.IsEnabled)continue;
      if(Regex.IsMatch(Name(e).Trim(),@"^(Reload|Refresh|重新加载|刷新)(\b|\s|$|此|页面|网页|这)",RegexOptions.IgnoreCase)){reload=e;break;}}
     if(reload==null)throw new Exception("普通浏览器刷新按钮未识别；未把旧页面当新报价");Validate(h,expected,family);Invoke(reload);Thread.Sleep(1000);
    }else if(action=="ordinary"||action=="custom") {
     AutomationElement button=ActionButton(doc,action);if(button==null)throw new Exception("对应配置入口未加载");Validate(h,expected,family);Invoke(button);Thread.Sleep(800);
    }else if(action=="expand"||action=="collapse"||action=="select") {
     if(!Regex.IsMatch(groupID??"",@"^module[a-zA-Z0-9]+$"))throw new Exception("无效的核心配置区域");
     AutomationElement header=ByID(doc,"label-"+groupID);if(header==null)throw new Exception("核心配置标题未加载");
     if(!Regex.IsMatch(Name(header).Trim(),@"^(Processor|Graphics Card|Memory|Storage|Displays?)(?:\s|$)",RegexOptions.IgnoreCase))throw new Exception("不是核心配置区域；没有操作附件或购买控件");
     Validate(h,expected,family);
     if(action=="expand"||action=="collapse") {
      object pattern;
      if(header.TryGetCurrentPattern(ExpandCollapsePattern.Pattern,out pattern)) {
       var expand=(ExpandCollapsePattern)pattern;
       if(action=="collapse") {
        if(expand.Current.ExpandCollapseState==ExpandCollapseState.Expanded||expand.Current.ExpandCollapseState==ExpandCollapseState.PartiallyExpanded)expand.Collapse();
       }else if(expand.Current.ExpandCollapseState==ExpandCollapseState.Collapsed||expand.Current.ExpandCollapseState==ExpandCollapseState.PartiallyExpanded)expand.Expand();
      }else {
       string cls=header.Current.ClassName??"";
       if(!Regex.IsMatch(cls,@"\baccordion-button\b"))throw new Exception("核心选项展开状态未识别");
       bool collapsed=Regex.IsMatch(cls,@"\bcollapsed\b");
       if((action=="expand"&&collapsed)||(action=="collapse"&&!collapsed))Invoke(header);
      }
     }else {
      AutomationElement scope=ByID(doc,groupID);if(scope==null)throw new Exception("核心配置区域未加载");
      AutomationElementCollection choices=scope.FindAll(TreeScope.Descendants,new AndCondition(Automation.ControlViewCondition,new OrCondition(new PropertyCondition(AutomationElement.ControlTypeProperty,ControlType.Button),new PropertyCondition(AutomationElement.ControlTypeProperty,ControlType.RadioButton),new PropertyCondition(AutomationElement.ControlTypeProperty,ControlType.CheckBox),new PropertyCondition(AutomationElement.ControlTypeProperty,ControlType.Custom))));
      AutomationElement target=null;
      foreach(AutomationElement choice in choices)if(Name(choice)==optionName&&choice.Current.IsEnabled) {if(target!=null)throw new Exception("核心配置选项不唯一");target=choice;}
      if(target==null)throw new Exception("核心配置选项已改变或不可选");
      Validate(h,expected,family);Invoke(target);
     }
     Thread.Sleep(800);
    }else if(action!="snapshot")throw new Exception("未知界面读取操作");
   }
   // Wait for normal navigation to finish, but never retry a denied page.
   Exception last=null;
   for(int i=0;i<10;i++) {try {return Capture(h,expected,family);}catch(Exception e) {last=e;if(Regex.IsMatch(e.Message,"access denied|网页访问被限制|已切换网页|其他标签页|标识已改变|已关闭|最小化",RegexOptions.IgnoreCase))throw;Thread.Sleep(500);Budget();}}
   throw last??new Exception("普通浏览器商品页未加载完成");
  }catch(Exception e) {PMPage error=new PMPage();error.handle=h.ToInt64();error.launched=launched;error.error=e.Message;return error;}
 }
}
'@
 $assembly = $env:PRICE_MONITOR_READER_ASSEMBLY
 if ($env:PRICE_MONITOR_READER_CACHE_READY -ne '1') {
  Add-Type -ReferencedAssemblies $refs -TypeDefinition $readerSource -OutputAssembly $assembly -OutputType Library
 }
 Add-Type -Path $assembly
 $stage = '运行'
 Write-ReaderStage '执行后台商品读取'
 $result = [PMReader]::Run([string]$request.action,[long]$request.handle,[string]$request.url,[bool]$request.family,[string]$request.exe,[string]$request.owner,[string]$request.group_id,[string]$request.option_name)
} catch {
 $result = @{error=('Windows 界面读取组件'+$stage+'失败；详细原因已记入程序日志');diagnostic=$_.Exception.Message}
}
Write-ReaderStage '生成本轮完整结果'
$json = $result | ConvertTo-Json -Depth 100 -Compress
$encoded = [Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($json))
[Console]::WriteLine()
[Console]::WriteLine('PSM_UIA_RESULT:'+$env:PRICE_MONITOR_UIA_TOKEN+':'+$encoded)
