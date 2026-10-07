import html, itertools, json
from pathlib import Path
from playwright.sync_api import sync_playwright

root=Path(__file__).resolve().parent
fixture=json.loads((root/'dom_fixture.json').read_text())
def element(n):
    attrs=' '.join(f'{html.escape(k)}="{html.escape(str(v),quote=True)}"' for k,v in n['attrs'].items())
    body=html.escape(n['text'])+''.join(element(c)+html.escape(c['tail']) for c in n['children'])
    return f'<{n["tag"]} {attrs}>{body}</{n["tag"]}>'
markup='<!doctype html><meta charset="utf-8"><style>.d-none{display:none!important}button,.option{display:block;padding:12px;margin:4px;min-height:30px} .sale-price{display:block}</style>'+''.join(element(fixture[k]) for k in ['hero','configuration','cart'])
url='https://www.dell.com/en-us/shop/laptop-computers/spd/xps16da16260/da16260_reg_01'
script=(root/'../source/owned_dom.js').read_text()
with sync_playwright() as pw:
    browser=pw.chromium.launch(executable_path='/usr/bin/chromium',headless=True,args=['--no-sandbox'])
    page=browser.new_page(viewport={'width':1200,'height':900})
    page.route('**/*',lambda route:route.fulfill(status=200,content_type='text/html',body=markup) if route.request.url==url else route.abort())
    page.goto(url)
    page.evaluate('''() => {
      window.trustedClicks=0;
      for(const group of document.querySelectorAll('[data-module-id][role="group"]')) {
        const header=document.getElementById('label-module'+group.dataset.moduleId);
        if(header)header.setAttribute('aria-expanded','true');
        for(const option of group.querySelectorAll('[data-option-id][data-is-selected]')) {
          option.addEventListener('click',event=>{
            if(!event.isTrusted)throw Error('untrusted fixture click');
            window.trustedClicks++;
            for(const sibling of group.querySelectorAll('[data-option-id][data-is-selected]')) {
              sibling.dataset.isSelected=String(sibling===option);
              let name=sibling.getAttribute('aria-label').replace(/(?:[.]\\s*)?(?:Selected|[+−–-]?\\s*\\$\\s*[0-9][0-9,]*(?:\\.[0-9]{1,2})?)\\s*$/i,'').trim();
              sibling.setAttribute('aria-label',name+(sibling===option?'. Selected':'. + $100.00'));
            }
            const price=2400+window.trustedClicks*3.17;
            for(const scope of ['hero-section','add-to-cart-stack'])for(const total of document.getElementById(scope).querySelectorAll('.sale-price'))total.textContent='$'+price.toFixed(2);
          });
        }
      }
    }''')
    def read(**kw):return json.loads(page.evaluate(script,{'action':'snapshot','url':url,'family':True,**kw}))
    initial=read()
    groups={}
    for entry in initial['configuration']['children']:
        if entry['id'].startswith('label-'):continue
        names=[(x['id'],x['name'])for x in entry['children']]
        if entry['id'] in ['modulePJ3K2R','moduleR75JGN','moduleW21P6T']:groups[entry['id']]=names
    assert [len(groups[k])for k in ['modulePJ3K2R','moduleR75JGN','moduleW21P6T']]==[5,4,2]
    session=page.context.new_cdp_session(page)
    count=0
    for choices in itertools.product(*(groups[k]for k in ['modulePJ3K2R','moduleR75JGN','moduleW21P6T'])):
        for group,(oid,name) in zip(['modulePJ3K2R','moduleR75JGN','moduleW21P6T'],choices):
            req={'action':'select','group_id':group,'option_id':oid,'option_name':name,'dom_phase':'prepare'}
            try:
                plan=read(**req)
            except Exception:
                print('failed request',req)
                print(page.locator('[data-option-id="'+oid+'"]').evaluate("e=>({rect:e.getBoundingClientRect().toJSON(),ancestors:(()=>{let a=[];for(let n=e;n;n=n.parentElement)a.push({id:n.id,cls:n.className,display:getComputedStyle(n).display});return a})()})"))
                raise
            if not plan['already']:
                x,y=plan['point']['x'],plan['point']['y']
                session.send('Input.dispatchMouseEvent',{'type':'mouseMoved','x':x,'y':y})
                hit=read(**{**req,'dom_phase':'hit','point_x':x,'point_y':y})
                assert hit['targetID']==oid
                for kind in ['mousePressed','mouseReleased']:
                    session.send('Input.dispatchMouseEvent',{'type':kind,'x':x,'y':y,'button':'left','clickCount':1})
            snapshot=read()
            group_state=next(g for g in snapshot['configuration']['children']if g['id']==group)
            assert [o['id']for o in group_state['children']if o['selected']]==[oid]
        count+=1
    cart=page.locator('#add-to-cart-stack')
    cart.evaluate("e=>e.style.display='none'")
    snapshot=read()
    assert snapshot['stock']=='有货' and snapshot['stockOfferID']=='da16260_reg_01'
    button=page.locator('#add-to-cart-stack [data-user-action="AddToCart"]')
    button.evaluate("e=>e.setAttribute('disabled','')")
    assert read()['stock']==''
    button.evaluate("e=>{e.removeAttribute('disabled');e.setAttribute('data-offer-id','wrong_fixed_offer')}")
    assert read()['stock']==''
    report={'status':'PASS','browser':'Chromium','fixture':'local captured DOM with simulated website handlers','combinations':count,'trusted_browser_clicks':page.evaluate('window.trustedClicks'),'hidden_sticky_cart_bound_stock':True,'disabled_or_wrong_offer_rejected':True,'live_Dell_scan':False,'Windows_UIAutomation':False}
    (root/'browser_core_results.json').write_text(json.dumps(report,indent=2))
    print(json.dumps(report))
    browser.close()
