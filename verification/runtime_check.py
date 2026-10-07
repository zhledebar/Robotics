import json, subprocess, tempfile, urllib.request
from pathlib import Path
from playwright.sync_api import sync_playwright

root = Path(__file__).resolve().parent
data = Path(tempfile.mkdtemp(prefix='runtime-', dir=root))
checks = []
def launch():
    p = subprocess.Popen([str(root/'monitor-linux'), '--port', '0', '--no-open', '--no-desktop-alerts', '--data-dir', str(data)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    url = p.stdout.readline().strip()
    assert url.startswith('http://127.0.0.1:')
    return p, url
def request(url, path, payload=None, origin=True):
    headers = {'Origin':url.rstrip('/')} if origin else {}
    if payload is not None: headers['Content-Type']='application/json'
    req=urllib.request.Request(url+path, data=json.dumps(payload).encode() if payload is not None else None, headers=headers)
    return urllib.request.urlopen(req, timeout=8)
def stop(p,url):
    request(url,'api/exit',{}).read()
    p.wait(timeout=10)
    assert p.returncode==0

p,url=launch()
try:
    assert json.load(request(url,'api/version'))['version']=='V8.26'
    checks.append('real Linux process startup/version')
    product={'name':'Dell XPS 16 test', 'url':'https://www.dell.com/en-us/shop/laptop-computers/spd/xps16da16260/da16260_reg_01','interval_min':10,'cooldown_min':30,'min_discount':10,'active':False,'dell_family_scan':True}
    saved=json.load(request(url,'api/save',product)); assert saved['ok']
    pid=saved['id']; product['id']=pid
    checks.append('product creation and persistence')
    with sync_playwright() as pw:
        browser=pw.chromium.launch(executable_path='/usr/bin/chromium',headless=True,args=['--no-sandbox'])
        page=browser.new_page(viewport={'width':1440,'height':1000})
        errors=[]
        page.on('pageerror',lambda e:errors.append(str(e)))
        page.goto(url)
        page.wait_for_function("document.querySelector('#rows').innerText.includes('Dell XPS 16 test')")
        page.evaluate('(id)=>dellView(id)',pid)
        assert '暂无可显示结果' in page.locator('#dr').inner_text()
        page.screenshot(path=str(root/'ui-empty-xps.png'),full_page=True)
        assert not errors,errors
        checks.append('Chromium rendered dashboard/configuration dialog; no JavaScript errors')
        page.evaluate('(id)=>edit(id)',pid)
        assert page.locator('#ds').input_value()=='quick'
        page.locator('#ds').select_option('full')
        page.evaluate('saveProduct()')
        assert json.load(request(url,'api/products'))[0]['dell_scan_mode']=='full'
        page.evaluate('(id)=>edit(id)',pid)
        page.locator('#ds').select_option('quick')
        page.evaluate('saveProduct()')
        assert json.load(request(url,'api/products'))[0]['dell_scan_mode']=='quick'
        checks.append('real browser quick/full mode editing and API persistence')
        browser.close()
    response=request(url,'api/diagnostics')
    assert 'V8.26.txt' in response.headers['Content-Disposition']
    assert 'V8.26 started' in response.read().decode()
    checks.append('diagnostic export')
    product['name']='Dell XPS 16 edited';json.load(request(url,'api/save',product))
    assert json.load(request(url,'api/products'))[0]['name']==product['name']
    checks.append('product editing')
    stop(p,url); p,url=launch()
    restored=json.load(request(url,'api/products'))
    assert len(restored)==1 and restored[0]['id']==pid and restored[0]['name']==product['name']
    checks.append('exit/restart restores saved data')
    json.load(request(url,'api/delete?id='+pid,{}))
    assert json.load(request(url,'api/products'))==[]
    checks.append('product deletion')
    stop(p,url)
    checks.append('clean shutdown')
    (root/'runtime-results.json').write_text(json.dumps({'status':'PASS','checks':checks,'Windows_executed':False,'live_Dell_scan':False},ensure_ascii=False,indent=2))
    print(json.dumps({'status':'PASS','checks':checks},ensure_ascii=False))
finally:
    if p.poll() is None: p.terminate();p.wait(timeout=10)
