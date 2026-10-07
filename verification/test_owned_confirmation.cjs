const fs=require('fs'),vm=require('vm'),assert=require('assert');
const captured=JSON.parse(fs.readFileSync(__dirname+'/dom_fixture.json','utf8'));
let underPointer=null,overlay=null,modal=null;
class Element {
 constructor(n){this.tag=n.tag;this.attrs={...n.attrs};this.text=n.text||'';this.tail=n.tail||'';this.children=(n.children||[]).map(x=>new Element(x));for(const c of this.children)c.parent=this}
 get id(){return this.attrs.id||''} get tagName(){return this.tag.toUpperCase()}
 get textContent(){return this.text+this.children.map(c=>c.textContent+c.tail).join('')}
 get innerText(){return this.textContent} get className(){return this.attrs.class||''}
 get classList(){return {contains:x=>this.className.split(/\s+/).includes(x)}}
 get dataset(){const o={};for(const[k,v]of Object.entries(this.attrs))if(k.startsWith('data-'))o[k.slice(5).replace(/-([a-z])/g,(_,x)=>x.toUpperCase())]=v;return o}
 getAttribute(k){return this.attrs[k]??null} hasAttribute(k){return k in this.attrs}
 setAttribute(k,v){this.attrs[k]=String(v)} get parentElement(){return this.parent||null}
 getBoundingClientRect(){return {left:10,right:110,top:20,bottom:50}}
 getClientRects(){return this.hiddenRect?[]:[{width:100,height:30}]}
 scrollIntoView(){underPointer=this} contains(e){return this===e||this.children.some(c=>c.contains(e))}
 matches(s){if(s[0]==='#')return this.id===s.slice(1);if(s[0]==='.')return s.slice(1).split('.').every(c=>this.classList.contains(c));const parts=[...s.matchAll(/\[([^=\]]+)(?:="([^"]*)")?\]/g)];return parts.length&&parts.every(m=>m[2]===undefined?m[1]in this.attrs:this.attrs[m[1]]===m[2])}
 querySelectorAll(s){let a=[];for(const c of this.children){if(c.matches(s))a.push(c);a.push(...c.querySelectorAll(s))}return a}
 querySelector(s){return this.querySelectorAll(s)[0]||null}
}
const config=new Element(captured.configuration),product=new Element({tag:'div',attrs:{id:'upd-product-data'}});
const roots=()=>[config,product,...(modal?[modal]:[])];
const document={title:'Dell XPS 16',body:{innerText:'Dell XPS 16'},
 getElementById:id=>roots().flatMap(r=>[r,...r.querySelectorAll('[id]')]).find(e=>e.id===id)||null,
 querySelectorAll:s=>roots().flatMap(r=>[...(r.matches(s)?[r]:[]),...r.querySelectorAll(s)]),
 elementFromPoint:()=>overlay||underPointer};
const url='https://www.dell.com/en-us/shop/laptop-computers/spd/xps16da16260/da16260_reg_01';
const env=vm.createContext({innerWidth:1200,innerHeight:900,document,location:{href:url},URL,getComputedStyle:e=>({display:e.classList.contains('d-none')?'none':'block',visibility:'visible',opacity:e.opacity||'1'})});
const inspect=vm.runInContext('('+fs.readFileSync(__dirname+'/../source/owned_confirm.js','utf8')+')',env);
const read=r=>JSON.parse(inspect(r));
const applySelection=(group,choice)=>{for(const e of group.querySelectorAll('[data-option-id][data-is-selected]')){
 const title=e.getAttribute('aria-label').replace(/(?:\.\s*)?(?:Selected|[+−–-]\s*\$\s*[\d,.]+)\s*$/i,'').trim().replace(/\.$/,'');
 e.setAttribute('data-is-selected',e===choice?'true':'false');e.setAttribute('aria-label',title+(e===choice?'. Selected':'. + $100.00'));
}};
let serial=0;
function openModal(owner,kind,commit){
 const id='bound-modal-'+(++serial);owner.setAttribute('data-id',id);
 const select=kind==='select';
 modal=new Element({tag:'div',attrs:{id,role:'dialog',class:'smart-popover-popup '+(select?'delta-price-modal option-selection-modal':'sc-reset-modal '+(kind==='ordinary'?'sc-reset-modal-from-custom-order':''))},children:[
  {tag:'button',attrs:select?{id:'selection-modal-change-btn','data-user-action':'OptionSelectionChangeAccept'}:{class:'scrm-continue','data-user-action':'ResetSelections'},text:select?'Accept':'Continue'}]});
 modal.children[0].onBrowserClick=()=>{commit();modal=null};return modal;
}
function confirm(r){const p=read({...r,dom_phase:'prepare'});if(!p.needed)return false;
 const q=read({...r,dom_phase:'hit',point_x:p.point.x,point_y:p.point.y});assert.equal(q.modalID,p.modalID);assert.equal(q.targetID,p.targetID);
 underPointer.onBrowserClick();return true;
}
const groups=config.querySelectorAll('[data-module-id][role="group"]');
const group=label=>groups.find(g=>g.getAttribute('aria-label')===label);
const cpu=group('Processor'),ssd=group('Storage'),display=group('Displays');
let combinations=0,confirmations=0;
for(const a of cpu.querySelectorAll('[data-option-id]'))for(const b of ssd.querySelectorAll('[data-option-id]'))for(const c of display.querySelectorAll('[data-option-id]')){
 for(const [g,e]of [[cpu,a],[ssd,b],[display,c]]){
  const req={url,family:true,action:'select',group_id:'module'+g.dataset.moduleId,option_id:e.dataset.optionId,option_name:e.getAttribute('aria-label')};
  if(e.dataset.isSelected!=='true'){
   const previous=g.querySelectorAll('[data-option-id]').find(x=>x.dataset.isSelected==='true');
   openModal(e,'select',()=>applySelection(g,e));
   assert.equal(previous.dataset.isSelected,'true','opening dialog must not fabricate selection');
   assert.equal(e.dataset.isSelected,'false');assert(confirm(req));confirmations++;
  }
  assert.equal(g.querySelectorAll('[data-option-id]').filter(x=>x.dataset.isSelected==='true').length,1);
  assert.equal(e.dataset.isSelected,'true','actual selection required after confirmation');
 }
 combinations++;
}
// Reproduce 08:34:48: the graphics label loses its unsigned zero-price
// suffix immediately after a successful click, before the popup check.
const gpu=group('Graphics Card'),template=gpu.querySelectorAll('[data-option-id]')[0];
const arc=new Element({tag:'div',attrs:{...template.attrs,'data-option-id':'T814RM-4PHFG1','data-is-selected':'false'},text:'Intel® Arc™ graphics'});
arc.parent=gpu;gpu.children.push(arc);
arc.setAttribute('aria-label','Intel® Arc™ graphics. $0.00');
const zeroReq={url,family:true,action:'select',group_id:'module'+gpu.dataset.moduleId,option_id:arc.dataset.optionId,option_name:arc.getAttribute('aria-label')};
applySelection(gpu,arc);
arc.setAttribute('aria-label','Intel® Arc™ graphics. Selected');
assert.equal(confirm(zeroReq),false,'accepted zero delta must survive subsequent no-modal check');
openModal(arc,'select',()=>{});assert(confirm(zeroReq),'bound modal must accept zero-price to Selected label transition');
openModal(arc,'select',()=>{});assert.throws(()=>confirm({...zeroReq,option_name:'Wrong graphics. $0.00'}));modal=null;
const e=cpu.querySelectorAll('[data-option-id]').find(x=>x.dataset.isSelected!=='true');
const req={url,family:true,action:'select',group_id:'module'+cpu.dataset.moduleId,option_id:e.dataset.optionId,option_name:e.getAttribute('aria-label')};
assert.equal(confirm(req),false,'no dialog must not click');
const rejects=r=>assert.throws(()=>confirm(r));
openModal(e,'select',()=>{});e.setAttribute('data-id','unrelated');rejects(req);
e.setAttribute('data-id',modal.id);overlay=product;rejects(req);overlay=null;
modal.children[0].setAttribute('data-user-action','AddToCart');rejects(req);modal.children[0].setAttribute('data-user-action','OptionSelectionChangeAccept');
modal.children[0].setAttribute('disabled','');rejects(req);delete modal.children[0].attrs.disabled;
// Repeated IDs in the same confirmation scope must not be accepted.
const duplicate=new Element({tag:'button',attrs:{id:'selection-modal-change-btn'},text:'Confirm changes'});duplicate.parent=modal;modal.children.push(duplicate);rejects(req);modal.children.pop();
rejects({...req,option_name:'wrong CPU'});rejects({...req,url:'https://www.dell.com/en-us/shop/laptop-computers/spd/other/offer_reg_01'});
modal=null;
for(const action of ['ordinary','custom']){
 let committed=false;openModal(product,action,()=>committed=true);assert(confirm({url,family:true,action}));assert(committed);
}
openModal(product,'ordinary',()=>{});rejects({url,family:true,action:'custom'});modal=null;
const report={status:'PASS',unsigned_zero_graphics_confirmed:true,wrong_graphics_name_rejected:true,confirmed_combinations:combinations,selection_confirmation_count:confirmations,modal_linked_to_requested_option:true,old_selection_not_accepted_before_confirmation:true,view_reset_confirmation_model:true,unbound_modal_rejected:true,covered_button_rejected:true,purchase_button_rejected:true,duplicate_confirmation_rejected:true,wrong_model_rejected:true,external_Dell_scan:false,Windows_UIAutomation_executed:false};
fs.writeFileSync(__dirname+'/confirmation_v825.json',JSON.stringify(report,null,2));console.log(JSON.stringify(report));
