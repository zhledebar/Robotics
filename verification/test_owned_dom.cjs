const fs=require('fs'),vm=require('vm');
const captured=JSON.parse(fs.readFileSync(__dirname+'/dom_fixture.json','utf8'));
const source=fs.readFileSync(__dirname+'/../source/owned_dom.js','utf8');
// Minimal DOM fixture for the captured markup. No external browser is run.
let scrolledTarget=null,overlay=null;
class Element {
 constructor(n){this.tag=n.tag;this.attrs={...n.attrs};this.text=n.text;this.tail=n.tail;this.children=n.children.map(x=>new Element(x));for(const c of this.children)c.parent=this}
 get id(){return this.attrs.id||''}
 get tagName(){return this.tag.toUpperCase()}
 getBoundingClientRect(){return this.zeroRect?{left:0,right:0,top:0,bottom:0}:{left:10,right:110,top:20,bottom:50}}
 scrollIntoView(){scrolledTarget=this}
 get textContent(){return this.text+this.children.map(c=>c.textContent+c.tail).join('')}
 get innerText(){return this.textContent}
 get className(){return this.attrs.class||''}
 get classList(){return {contains:x=>this.className.split(/\s+/).includes(x)}}
 get dataset(){const o={};for(const [k,v]of Object.entries(this.attrs))if(k.startsWith('data-'))o[k.slice(5).replace(/-([a-z])/g,(_,x)=>x.toUpperCase())]=v;return o}
 getAttribute(k){return this.attrs[k]??null}
 hasAttribute(k){return k in this.attrs}
 get parentElement(){return this.parent||null}
 getClientRects(){return this.zeroRect?[]:[{width:100,height:30}]}
 setAttribute(k,v){this.attrs[k]=String(v)}
 contains(e){return this===e||this.children.some(c=>c.contains(e))}
 matches(s){if(s.startsWith('.'))return this.classList.contains(s.slice(1));const parts=[...s.matchAll(/\[([^=\]]+)(?:="([^"]*)")?\]/g)];return parts.length&&parts.every(m=>m[2]===undefined?m[1]in this.attrs:this.attrs[m[1]]===m[2])}
 querySelectorAll(s){let a=[];for(const c of this.children){if(c.matches(s))a.push(c);a.push(...c.querySelectorAll(s))}return a}
 querySelector(s){return this.querySelectorAll(s)[0]||null}
 click(){throw Error('page-script click must not be used')}
}
const config=new Element(captured.configuration),cart=new Element(captured.cart),hero=new Element(captured.hero);
const document={elementFromPoint:()=>overlay||scrolledTarget,title:'Dell XPS 16',body:{innerText:'Dell XPS 16'},getElementById:id=>[config,cart,hero,...config.querySelectorAll('[id]'),...cart.querySelectorAll('[id]'),...hero.querySelectorAll('[id]')].find(e=>e.attrs.id===id)||null};
const url='https://www.dell.com/en-us/shop/laptop-computers/spd/xps16da16260/da16260_reg_01';
const env=vm.createContext({innerWidth:1200,innerHeight:900,document,location:{href:url},URL,getComputedStyle:e=>({display:e.styleDisplay||(e.classList.contains('d-none')?'none':'block'),visibility:e.styleVisibility||'visible',opacity:e.styleOpacity||'1'})});
const bridge=vm.runInContext('('+source+')',env);
const request={action:'snapshot',url,family:true};
const read=r=>JSON.parse(bridge(r));
let browserClicks=0;
const operate=r=>{
 const plan=read({...r,dom_phase:'prepare'});
 if(!plan.already){
  const hit=read({...r,dom_phase:'hit',point_x:plan.point.x,point_y:plan.point.y});
  if(!hit.already){browserClicks++;if(scrolledTarget.onClick)scrolledTarget.onClick({isTrusted:true})}
 }
 return plan;
};
const initial=read(request),children=initial.configuration.children;
if(initial.offerID!=='da16260_reg_01')throw Error('DOM quote Offer ID not bound to selected configurations');
const originalLookup=document.getElementById;
const errorAlert=new Element({tag:'div',attrs:{id:'upd-error-alert',class:'d-none'},text:'Unable to price this configuration',tail:'',children:[]});
document.getElementById=id=>id==='upd-error-alert'?errorAlert:originalLookup(id);
errorAlert.setAttribute('class','');
const errorRead=read(request);
if(!errorRead.pending||!errorRead.diagnostic.selectionStatus.errorVisible)throw Error('visible Dell quote error accepted old quote');
errorAlert.setAttribute('class','d-none');
if(read(request).pending)throw Error('hidden old quote error blocked recovered quote');

const spinner=cart.querySelector('.purchase-path-loading-icon'),parent=spinner.parentElement;
const spinnerClass=spinner.className,parentClass=parent.className;
spinner.setAttribute('class',spinnerClass.replace(/\bd-none\b/g,''));
if(!read(request).pending)throw Error('visible loading indicator accepted old quote');
parent.setAttribute('class',parentClass+' d-none');
if(read(request).pending)throw Error('ancestor-hidden spinner considered loading');
parent.setAttribute('class',parentClass);
parent.styleDisplay='none';
if(read(request).pending)throw Error('CSS ancestor-hidden spinner considered loading');
parent.styleDisplay='';
parent.styleVisibility='hidden';
if(read(request).pending)throw Error('visibility-hidden spinner considered loading');
parent.styleVisibility='';
spinner.zeroRect=true;
if(read(request).pending)throw Error('zero rectangle spinner considered loading');
spinner.zeroRect=false;
spinner.setAttribute('class',spinnerClass);spinner.styleDisplay='block';
if(read(request).pending)throw Error('d-none spinner considered loading before CSS loads');
spinner.styleDisplay='';
cart.setAttribute('aria-busy','true');
if(!read(request).pending)throw Error('busy cart accepted old quote');
cart.setAttribute('aria-busy','false');
if(read(request).pending)throw Error('loading completion failed to recover');
const total=cart.querySelector('.sale-price'),oldTotal=total.text;
total.text='';
if(!read(request).pending)throw Error('missing total accepted');
total.text=oldTotal;
const heroTotal=hero.querySelector('.sale-price');
total.parentElement.styleDisplay='none';
let quote=read(request);
if(quote.pending||quote.price!==2399.99||quote.diagnostic.priceScopes.find(s=>s.id==='add-to-cart-stack').visible!==0)
 throw Error('hidden sticky cart blocked visible product hero total');
total.parentElement.styleDisplay='';
heroTotal.parentElement.styleDisplay='none';
if(read(request).pending)throw Error('hidden product hero blocked visible sticky cart total');
total.parentElement.styleDisplay='none';
quote=read(request);
if(!quote.pending||!quote.diagnostic.priceScopes.every(s=>s.visible===0))throw Error('no visible total accepted or missing diagnostic');
heroTotal.parentElement.styleDisplay='';total.parentElement.styleDisplay='';
const duplicate=new Element({tag:'span',attrs:{class:'sale-price'},text:oldTotal,tail:'',children:[]});
duplicate.parent=cart;cart.children.push(duplicate);
quote=read(request);
if(quote.pending||quote.price!==2399.99)throw Error('duplicate identical current total treated as ambiguous');
duplicate.text='$9,999.99';
if(!read(request).pending)throw Error('conflicting duplicate total accepted');
cart.children.pop();
heroTotal.text='$9,999.99';
if(!read(request).pending)throw Error('hero and cart disagreement accepted');
heroTotal.text=oldTotal;
const monthly=new Element({tag:'span',attrs:{class:'monthly-payment'},text:'$100.00',tail:'',children:[]});
monthly.parent=cart;cart.children.push(monthly);
if(read(request).price!==2399.99)throw Error('monthly price polluted current total');
cart.children.pop();
const find=label=>children.find(g=>children.find(h=>h.id==='label-'+g.id)?.name===label);
const cpu=find('Processor'),ssd=find('Storage'),display=find('Displays');
if(cpu.children.length!==5||ssd.children.length!==4||display.children.length!==2)throw Error('5x4x2 structure missing');
for(const g of config.querySelectorAll('[data-module-id][role="group"]'))for(const e of g.querySelectorAll('[data-option-id][data-is-selected]'))e.onClick=()=>{
 for(const q of g.querySelectorAll('[data-option-id][data-is-selected]')){
  const title=q.getAttribute('aria-label').replace(/(?:\.\s*)?(?:Selected|[+−–-]\s*\$\s*[\d,.]+)\s*$/i,'').trim().replace(/\.$/,'');
  q.setAttribute('data-is-selected',q===e?'true':'false');q.setAttribute('data-status',q===e?'selected':'available');
  q.setAttribute('aria-label',title+(q===e?'. Selected':'. + $100.00'));
 }
};
const gpu=children.find(g=>children.find(h=>h.id==='label-'+g.id)?.name==='Graphics Card');
const gpuScope=document.getElementById(gpu.id),template=gpuScope.querySelectorAll('[data-option-id]')[0];
const arc=new Element({tag:'div',attrs:{...template.attrs,'data-option-id':'T814RM-4PHFG1','data-is-selected':'false'},text:'Intel® Arc™ graphics',tail:'',children:[]});
arc.parent=gpuScope;gpuScope.children.push(arc);
document.getElementById('label-'+gpu.id).setAttribute('aria-expanded','true');
arc.onClick=()=>{for(const e of gpuScope.querySelectorAll('[data-option-id]'))e.setAttribute('data-is-selected',e===arc?'true':'false')};
arc.setAttribute('aria-label','Intel® Arc™ graphics. $0.00');
const zeroReq={...request,action:'select',group_id:gpu.id,option_id:arc.dataset.optionId,option_name:arc.getAttribute('aria-label')};
operate(zeroReq);arc.setAttribute('aria-label','Intel® Arc™ graphics. Selected');
if(!operate(zeroReq).already)throw Error('zero-price graphics name failed after actual selection');
let count=0;
for(const a of cpu.children)for(const b of ssd.children)for(const c of display.children){
 for(const [g,o]of [[cpu,a],[ssd,b],[display,c]]){
  const choice=read(request).configuration.children.find(n=>n.id===g.id).children.find(n=>n.id===o.id);
  operate({...request,action:'select',group_id:g.id,option_id:choice.id,option_name:choice.name});
 }
 const now=read(request);for(const [g,o]of [[cpu,a],[ssd,b],[display,c]]){
  const selected=now.configuration.children.find(n=>n.id===g.id).children.filter(n=>n.selected);
  if(selected.length!==1||selected[0].id!==o.id)throw Error('requested selection not matched');
 }
 count++;
}
for(const e of document.getElementById(cpu.id).querySelectorAll('[data-option-id]'))e.onClick=null;
let state=read(request),g=state.configuration.children.find(n=>n.id===cpu.id),before=g.children.find(n=>n.selected),other=g.children.find(n=>!n.selected);
operate({...request,action:'select',group_id:g.id,option_id:other.id,option_name:other.name});
if(read(request).configuration.children.find(n=>n.id===g.id).children.find(n=>n.selected).id!==before.id)throw Error('stale click fabricated state');
function rejects(r){let ok=false;try{operate(r)}catch(e){ok=true}if(!ok)throw Error('unsafe action accepted')}
const selectedRequest={...request,action:'select',group_id:g.id,option_id:other.id,option_name:other.name};
overlay=cart;rejects(selectedRequest);overlay=null;
const region=document.getElementById(g.id),heading=document.getElementById('label-'+g.id);
const regionClass=region.className;region.setAttribute('class','collapse');
rejects(selectedRequest);
heading.onClick=()=>region.setAttribute('class',regionClass);
operate({...request,action:'expand',group_id:g.id});
if(!read(request).configuration.children.find(n=>n.id==='label-'+g.id).expanded)throw Error('real accordion expansion not recognized');
rejects({...selectedRequest,option_name:'wrong processor'});
const processorElement=region.querySelectorAll('[data-option-id]').find(e=>e.dataset.optionId===other.id);
processorElement.zeroRect=true;rejects(selectedRequest);processorElement.zeroRect=false;
rejects({...request,action:'select',group_id:cpu.id,option_id:'wrong-id',option_name:other.name});
rejects({...request,action:'select',group_id:'moduleR5YXWW',option_name:'E5 Power Cord 1M for US. Selected'});
rejects({...request,url:'https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16/foo_reg_01'});
// Run the actual read-only diagnostic script against the same captured DOM.
const diagnosticSource=fs.readFileSync(__dirname+'/../source/owned_content_status.js','utf8');
const contentDiagnostic=vm.runInContext('('+diagnosticSource+')',env);
document.readyState='complete';
let d=JSON.parse(contentDiagnostic(url));
if(d.readyState!=='complete'||!d.regions['hero-section'].present||!d.regions['hero-section'].visible)throw Error('loaded diagnostic regions missing');
hero.styleDisplay='none';
d=JSON.parse(contentDiagnostic(url));
if(d.regions['hero-section'].visible||!d.regions['hero-section'].present)throw Error('hidden region diagnosed as visible');
hero.styleDisplay='';
document.readyState='loading';
if(JSON.parse(contentDiagnostic(url)).readyState!=='loading')throw Error('loading state lost');
const oldTitle=document.title;document.title='Access Denied';
if(!JSON.parse(contentDiagnostic(url)).blocked)throw Error('blocked DOM not diagnosed');
document.title=oldTitle;
let navigated=false;try{contentDiagnostic('https://example.com')}catch(e){navigated=true}
if(!navigated)throw Error('foreign document diagnosed');
if(['price','stock','confirmed','configuration'].some(k=>k in d))throw Error('diagnostic returned product evidence');
const report={read_only_loading_diagnostics:true,content_status_visibility:true,diagnostic_excludes_quote_and_stock:true,unsigned_zero_graphics_identity:true,browser_input_model:true,page_script_click_rejected:true,covered_option_rejected:true,collapsed_option_rejected:true,accordion_expansion_verified:true,status:'PASS',fixture:'captured Dell HTML parsed into an in-memory DOM model',confirmed_combinations:count,hidden_sticky_cart_hero_total_used:true,duplicate_same_amount_accepted:true,conflicting_current_totals_rejected:true,all_hidden_totals_rejected:true,scoped_price_diagnostics:true,monthly_price_excluded:true,loading_visibility_regressions:true,loading_completion_recovered:true,missing_total_rejected:true,stale_click_state_preserved:true,non_core_action_rejected:true,wrong_model_rejected:true,external_Dell_scan:false};
fs.writeFileSync(__dirname+'/dom_v825.json',JSON.stringify(report,null,2));
fs.writeFileSync(__dirname+'/dom_snapshot_v825.json',JSON.stringify(initial));
console.log(JSON.stringify(report));
