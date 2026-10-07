import html,json,sys,tempfile
from pathlib import Path
from playwright.sync_api import sync_playwright

fixture=json.loads((Path(__file__).parent/'dom_fixture.json').read_text())
def element(n):
    attrs=' '.join(f'{html.escape(k)}="{html.escape(str(v),quote=True)}"'for k,v in n['attrs'].items())
    return f'<{n["tag"]} {attrs}>'+html.escape(n['text'])+''.join(element(c)+html.escape(c['tail'])for c in n['children'])+f'</{n["tag"]}>'
markup='<!doctype html><meta charset="utf-8"><style>.d-none{display:none!important}button,.option{display:block;min-height:30px;padding:12px;margin:4px}.sale-price{display:block}.option-selection-modal{position:fixed;inset:15%;background:white;border:2px solid black;z-index:9999}</style>'+''.join(element(fixture[k])for k in ['hero','configuration','cart'])
url=sys.argv[1] if len(sys.argv)>1 else 'https://www.dell.com/en-us/shop/laptop-computers/spd/xps16da16260/da16260_reg_01'
with tempfile.TemporaryDirectory(prefix='psm-coupled-') as profile,sync_playwright() as pw:
    context=pw.chromium.launch_persistent_context(profile,executable_path='/usr/bin/chromium',headless=True,viewport={'width':1200,'height':900},args=['--no-sandbox','--remote-debugging-port=0','--remote-allow-origins=http://localhost'])
    context.route('**/*',lambda route:route.fulfill(status=200,content_type='text/html',body=markup)if route.request.url==url else route.abort())
    page=context.pages[0];page.goto(url)
    page.evaluate('''() => {
      window.selectionClicks=0;window.confirmationClicks=0;window.coupledChanges=0;
      const set=(group,choice)=>{
        for(const x of group.querySelectorAll('[data-option-id][data-is-selected]')){
          const name=x.getAttribute('aria-label').replace(/(?:[.]\\s*)?(?:Selected|[+−–-]?\\s*\\$\\s*[0-9][0-9,]*(?:\\.[0-9]{1,2})?)\\s*$/i,'').trim().replace(/\\.$/,'');
          x.dataset.isSelected=String(x===choice);x.setAttribute('aria-label',name+(x===choice?'. Selected':'. + $100.00'));
        }
      };
      const cpu=document.getElementById('modulePJ3K2R'),ram=document.getElementById('moduleW38JHD');
      const selectBy=(g,id)=>{const x=g.querySelector('[data-option-id="'+id+'"]');if(x)set(g,x)};
      for(const g of document.querySelectorAll('[data-module-id][role="group"]')) {
        const header=document.getElementById('label-module'+g.dataset.moduleId);if(header)header.setAttribute('aria-expanded','true');
        for(const option of g.querySelectorAll('[data-option-id][data-is-selected]')) {
          option.addEventListener('click',event=>{
            if(!event.isTrusted)throw Error('untrusted click');window.selectionClicks++;
            const id='fixture-modal-'+window.selectionClicks;option.setAttribute('data-id',id);
            const modal=document.createElement('div');modal.id=id;modal.className='smart-popover-popup option-selection-modal';modal.setAttribute('role','dialog');
            modal.innerHTML='<p>This selection will also change other configuration options.</p><button id="selection-modal-change-btn" data-user-action="OptionSelectionChangeAccept">Accept</button>';
            document.body.append(modal);
            modal.querySelector('button').addEventListener('click',ev=>{
              if(!ev.isTrusted)throw Error('untrusted confirmation');window.confirmationClicks++;
              set(g,option);
              if(g.dataset.moduleId==='PJ3K2R'){selectBy(ram,option.dataset.optionId==='PJ3K2R-RGMYFN'?'W38JHD-T017PT':'W38JHD-YX3WHF');for(const x of ram.querySelectorAll('[data-option-id]')){x.removeAttribute('aria-disabled');x.removeAttribute('data-status')}window.coupledChanges++}
              if(g.dataset.moduleId==='W38JHD'){selectBy(cpu,'PJ3K2R-4X26RD');window.coupledChanges++}
              let price=2000;for(const x of document.querySelectorAll('#configuration-section [data-option-id][data-is-selected="true"]'))for(const c of x.dataset.optionId)price+=c.charCodeAt(0)/100;
              for(const id of ['hero-section','add-to-cart-stack'])for(const total of document.getElementById(id).querySelectorAll('.sale-price'))total.textContent='$'+price.toFixed(2);
              modal.remove();
            });
          });
        }
      }
    }''')
    port=Path(profile,'DevToolsActivePort').read_text().splitlines()[0]
    print(port,flush=True)
    sys.stdin.readline()
    context.close()
