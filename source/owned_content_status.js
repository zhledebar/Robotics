function(expectedURL) {
 if(location.href!==expectedURL)throw new Error('后台诊断商品网址已变化');
 const visible=e=>{if(!e)return false;for(let n=e;n;n=n.parentElement){const s=getComputedStyle(n);if(n.hidden||s.display==='none'||s.visibility==='hidden'||s.visibility==='collapse'||s.contentVisibility==='hidden'||Number(s.opacity)===0)return false;}return Array.from(e.getClientRects()).some(r=>r.width>0&&r.height>0);};
 const regions={};for(const id of ['hero-section','configuration-section','offers-container','add-to-cart-stack']){const e=document.getElementById(id);regions[id]={present:!!e,visible:visible(e)};}
 const title=(document.title||'').slice(0,160);
 const text=title+' '+(document.body?document.body.innerText:'').slice(0,1000);
 return JSON.stringify({url:location.href,readyState:document.readyState,title,blocked:/access denied|verify you are human|checking your browser|just a moment|you don.t have permission to access/i.test(text),regions});
}
