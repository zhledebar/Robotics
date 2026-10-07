(req => {
  const clean = s => String(s || '').replace(/\s+/g, ' ').trim();
  const matches = () => {
    const wanted = new URL(req.url), actual = new URL(location.href);
    const platform = u => u.pathname.match(/\/en-us\/shop\/.*?\/spd\/([^/]+)/)?.[1];
    return actual.protocol === 'https:' && actual.hostname === 'www.dell.com' &&
      platform(wanted) && platform(wanted) === platform(actual) &&
      (req.family || actual.pathname === wanted.pathname);
  };
  if (!matches()) throw new Error('商品网址已变化；未操作其他机型');
  if (/access denied|verify you are human|checking your browser/i.test(document.title + '\n' + document.body.innerText.slice(0, 1000)))
    throw new Error('网页访问被限制（access denied）');
  const config = document.getElementById('configuration-section');
  const pending = (reason, diagnostic) => JSON.stringify({url:location.href, pending:true, reason, diagnostic});
  if (!config && req.action === 'snapshot') return pending('核心配置区域尚未加载');
  if (!config) throw new Error('核心配置区域尚未加载');
  const groups = [...config.querySelectorAll('[data-module-id][role="group"]')];
  const expanded = (header, scope) => {
    const aria = header.getAttribute('aria-expanded');
    if (aria !== null) return aria === 'true';
    if (scope.classList.contains('collapse')) return scope.classList.contains('show');
    return !header.classList.contains('collapsed');
  };
  const optionKey = s => {
    let value = clean(s);
    for(let i=0;i<3;i++) value=value.replace(/(?:\.\s*)?(?:Selected|[+−–-]?\s*\$\s*[0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*$/i,'').trim().replace(/\.$/,'').trim();
    return value;
  };
  if (req.action !== 'snapshot') {
    if (!/^module[a-zA-Z0-9]+$/.test(req.group_id || '')) throw new Error('无效的核心区域');
    const scope = document.getElementById(req.group_id);
    const header = document.getElementById('label-' + req.group_id);
    if (!scope || !header || !config.contains(scope) || !config.contains(header) ||
        !/^(Processor|Graphics Card|Memory|Storage|Displays?)$/i.test(clean(header.querySelector('.module-title')?.textContent || header.textContent)))
      throw new Error('不是核心配置区域；未操作附件或购买控件');
    let target, already = false;
    if (req.action === 'expand' || req.action === 'collapse') {
      already = ((req.action === 'expand') === expanded(header, scope));
      target = header;
    } else if (req.action === 'select') {
      const wanted = [...scope.querySelectorAll('[data-option-id][data-is-selected]')]
        .filter(e => req.option_id ? e.dataset.optionId === req.option_id : e.getAttribute('aria-label') === req.option_name);
      if (wanted.length !== 1) throw new Error('核心选项已改变或不唯一');
      const e = wanted[0];
      if (optionKey(e.getAttribute('aria-label')) !== optionKey(req.option_name)) throw new Error('核心选项ID对应名称已改变');
      already = e.dataset.isSelected === 'true';
      if (e.dataset.isSelected !== 'true') {
        if (e.getAttribute('aria-disabled') === 'true' || /^(unavailable|disabled)$/.test(e.dataset.status || ''))
          throw new Error('该核心选项不可选');
        if (!expanded(header, scope)) throw new Error('核心区域未展开；未点击隐藏选项');
      }
      target = e;
    } else throw new Error('未知配置操作');
    const selectedIDs = [...scope.querySelectorAll('[data-option-id][data-is-selected]')]
      .filter(e=>e.dataset.isSelected==='true').map(e=>e.dataset.optionId);
    const identity = {url:location.href, groupID:req.group_id, optionID:req.option_id||'',
      targetID:target===header?header.id:target.dataset.optionId, selectedIDs, already};
    if (!/^(prepare|hit)$/.test(req.dom_phase||'')) throw new Error('配置操作缺少浏览器点击阶段');
    if (already) return JSON.stringify(identity);
    if (req.dom_phase === 'prepare') target.scrollIntoView({behavior:'instant', block:'center', inline:'center'});
    const rect = target.getBoundingClientRect();
    const left = Math.max(0,rect.left), top = Math.max(0,rect.top),
      right = Math.min(innerWidth,rect.right), bottom = Math.min(innerHeight,rect.bottom);
    if (!(right > left && bottom > top)) throw new Error('核心选项没有可点击的可见区域');
    const point = req.dom_phase==='hit' ? {x:req.point_x, y:req.point_y} : {x:(left+right)/2, y:(top+bottom)/2};
    if (!Number.isFinite(point.x)||!Number.isFinite(point.y)||point.x<=left||point.x>=right||point.y<=top||point.y>=bottom)
      throw new Error('核心选项点击位置已变化');
    const hit = document.elementFromPoint(point.x,point.y);
    if (!hit || !target.contains(hit) || (hit!==target && /^(A|BUTTON|INPUT|SELECT|TEXTAREA)$/.test(hit.tagName)))
      throw new Error('核心选项被其他控件遮挡；没有点击');
    if (!matches()) throw new Error('点击前商品网址已变化');
    return JSON.stringify({...identity, point});
  }
  // Read all module selections directly, including collapsed/non-core modules.
  // Their IDs and selected state come from the same DOM snapshot as each other.
  const cart = document.getElementById('add-to-cart-stack');
  // Bootstrap's d-none expresses Dell's loading state even before CSS arrives.
  // A descendant's computed display alone does not reflect hidden ancestors.
  const visible = e => {
    if (!e) return false;
    for (let n=e; n; n=n.parentElement) {
      const style = getComputedStyle(n);
      if (n.hasAttribute('hidden') || n.classList.contains('d-none') ||
          style.display === 'none' || /^(hidden|collapse)$/.test(style.visibility) ||
          style.contentVisibility === 'hidden' || style.opacity === '0') return false;
    }
    return [...e.getClientRects()].some(r => r.width > 0 && r.height > 0);
  };
  const productData = document.getElementById('upd-product-data');
  const errorAlert = document.getElementById('upd-error-alert');
  const selectionStatus = {restricted:productData?.getAttribute('data-is-restricted') === 'true',
    errorVisible:visible(errorAlert), errorText:visible(errorAlert)?clean(errorAlert.textContent).slice(0,500):''};
  if (selectionStatus.restricted || selectionStatus.errorVisible)
    return pending('Dell配置报价报错或暂时限制配置切换；未接受旧报价',{selectionStatus});
  if (!cart) return pending('整机报价区域尚未加载');
  if (cart.getAttribute('aria-busy') === 'true' ||
      [...cart.querySelectorAll('.purchase-path-loading-icon')].some(visible))
    return pending('整机报价仍在加载；未接受旧报价');
  // Dell repeats the same current total in the product hero and sticky cart.
  // Responsive layouts may hide the sticky total. Uniqueness applies to the
  // amount across these two current-product scopes, not to the element count.
  const amount = e => {
    const text = clean(e.textContent);
    return /^\$\s*[\d,]+(?:\.\d{2})?$/.test(text) ? Number(text.replace(/[$,\s]/g,'')) : 0;
  };
  const priceScopes = ['hero-section', 'add-to-cart-stack'].map(id => {
    const scope = document.getElementById(id);
    const all = [...(scope?.querySelectorAll('.sale-price') || [])];
    const shown = all.filter(visible);
    return {id, candidates:all.length, visible:shown.length, values:shown.map(amount),
      elements:all.slice(0,6).map(e => ({text:clean(e.textContent).slice(0,80), visible:visible(e),
        ancestors:(() => {const a=[]; for(let n=e;n&&a.length<5;n=n.parentElement){
          const s=getComputedStyle(n);a.push({id:n.id||'',class:n.className,display:s.display,visibility:s.visibility,
            rects:n.getClientRects().length});}return a;})()}))};
  });
  const totals = priceScopes.flatMap(s => s.values);
  const unique = [...new Set(totals)];
  if (!totals.length || totals.some(p => !(p > 0)))
    return pending('商品首屏及购买栏的当前整机总价尚未加载', {priceScopes});
  if (unique.length !== 1)
    return pending('商品首屏与购买栏整机总价不同步', {priceScopes});
  const price = unique[0];
  const root = {kind:'Group', id:'configuration-section', children:[]};
  const selectedOfferIDs=new Set();
  for (const g of groups) {
    const id = 'module' + g.dataset.moduleId;
    const h = document.getElementById('label-' + id);
    if (!h || !config.contains(h)) throw new Error('配置标题与模块未对应');
    const label = clean(g.getAttribute('aria-label'));
    const isExpanded = expanded(h, document.getElementById(id));
    const options = [...g.querySelectorAll('[data-option-id][data-is-selected]')];
    const seen = new Set();
    const children = options.map(e => {
      const optionID = e.dataset.optionId;
      if(e.dataset.isSelected==='true' && e.dataset.offerId) selectedOfferIDs.add(e.dataset.offerId.toLowerCase());
      if (!optionID || seen.has(optionID)) throw new Error('配置选项ID重复');
      seen.add(optionID);
      return {kind:'Button', name:e.getAttribute('aria-label') || clean(e.textContent),
        id:optionID, class:e.className, selected:e.dataset.isSelected === 'true',
        enabled:e.getAttribute('aria-disabled') !== 'true' && !/^(unavailable|disabled)$/.test(e.dataset.status || ''), children:[]};
    });
    root.children.push({kind:'Button',id:'label-'+id,name:label,enabled:true,expanded:isExpanded,children:[]},
      {kind:'Group',id,children});
  }
  if (!matches()) throw new Error('读取途中商品网址已变化');
  const offerID = selectedOfferIDs.size===1?[...selectedOfferIDs][0]:'';
  // The responsive sticky cart may itself be hidden while editing options.
  // Its uniquely bound enabled purchase control is still availability evidence,
  // provided the control is not itself hidden/disabled or busy. Never click it.
  const purchase = [...cart.querySelectorAll('[data-user-action="AddToCart"]')];
  const loading = [...cart.querySelectorAll('.purchase-path-loading-icon')]
    .some(e=>!e.hasAttribute('hidden')&&!e.classList.contains('d-none')&&getComputedStyle(e).display!=='none');
  const button = purchase.length===1?purchase[0]:null;
  const stockOfferID = clean(button?.getAttribute('data-offer-id')).toLowerCase();
  const unavailable = /out of stock|currently unavailable/i.test(clean(cart.textContent));
  const available = offerID && stockOfferID===offerID && button?.tagName==='BUTTON' &&
    !button.hasAttribute('disabled') && button.getAttribute('aria-disabled')!=='true' &&
    !button.hasAttribute('hidden') && !button.classList.contains('d-none') &&
    getComputedStyle(button).display!=='none' && !loading && !unavailable && cart.getAttribute('aria-busy')!=='true';
  return JSON.stringify({url:location.href, configuration:root, price, offerID,
    stock:available?'有货':'', stockOfferID:available?stockOfferID:'', diagnostic:{priceScopes, selectionStatus, availability:{offerID,stockOfferID,purchaseControls:purchase.length,loading,available:!!available}}});
})
