(req => {
  const clean = s => String(s || '').replace(/\s+/g, ' ').trim();
  const matches = () => {
    const wanted = new URL(req.url), actual = new URL(location.href);
    const platform = u => u.pathname.match(/\/en-us\/shop\/.*?\/spd\/([^/]+)/)?.[1];
    return actual.protocol === 'https:' && actual.hostname === 'www.dell.com' &&
      platform(wanted) && platform(wanted) === platform(actual) &&
      (req.family || actual.pathname === wanted.pathname);
  };
  if (!matches()) throw new Error('商品网址已变化；未确认其他机型');
  if (/access denied|verify you are human|checking your browser/i.test(document.title + '\n' + document.body.innerText.slice(0, 1000)))
    throw new Error('网页访问被限制（access denied）');
  const visible = e => {
    if (!e) return false;
    for (let n=e; n; n=n.parentElement) {
      const style = getComputedStyle(n);
      if (n.hasAttribute('hidden') || n.classList.contains('d-none') || style.display === 'none' ||
          /^(hidden|collapse)$/.test(style.visibility) || style.opacity === '0') return false;
    }
    return [...e.getClientRects()].some(r => r.width > 0 && r.height > 0);
  };
  const optionKey = s => {
    let value = clean(s);
    for(let i=0;i<3;i++) value=value.replace(/(?:\.\s*)?(?:Selected|[+−–-]?\s*\$\s*[0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*$/i,'').trim().replace(/\.$/,'').trim();
    return value;
  };
  let owner, selector, modalClass, selectedIDs = [];
  if (req.action === 'select') {
    const config = document.getElementById('configuration-section');
    const scope = document.getElementById(req.group_id), header = document.getElementById('label-' + req.group_id);
    if (!/^module[a-zA-Z0-9]+$/.test(req.group_id || '') || !config || !scope || !header ||
        !config.contains(scope) || !config.contains(header) ||
        !/^(Processor|Graphics Card|Memory|Storage|Displays?)$/i.test(clean(header.querySelector('.module-title')?.textContent || header.textContent)))
      throw new Error('确认目标不是核心配置区域');
    const wanted = [...scope.querySelectorAll('[data-option-id][data-is-selected]')].filter(e=>e.dataset.optionId===req.option_id);
    if (!req.option_id || wanted.length !== 1 || optionKey(wanted[0].getAttribute('aria-label')) !== optionKey(req.option_name))
      throw new Error('确认目标的选项ID或名称已变化');
    owner = wanted[0];
    selectedIDs = [...scope.querySelectorAll('[data-option-id][data-is-selected]')].filter(e=>e.dataset.isSelected==='true').map(e=>e.dataset.optionId);
    selector = '#selection-modal-change-btn';
    modalClass = 'option-selection-modal';
  } else if (req.action === 'ordinary' || req.action === 'custom') {
    owner = document.getElementById('upd-product-data');
    selector = '.scrm-continue';
    modalClass = 'sc-reset-modal';
  } else throw new Error('没有待确认的配置操作');
  const identity = {url:location.href, groupID:req.group_id||'', optionID:req.option_id||'', selectedIDs};
  const modalID = owner?.getAttribute('data-id') || '';
  const modal = modalID ? document.getElementById(modalID) : null;
  const candidates = [...document.querySelectorAll('.' + modalClass)].filter(visible);
  // Dell SmartUI sets data-id on the requesting option/product and moves the
  // corresponding popup into body. A generic visible "Continue" is not proof.
  if (!modal || !visible(modal)) {
    if (candidates.length) throw new Error('配置确认框与本次操作未绑定；没有猜测确认按钮');
    return JSON.stringify({...identity, needed:false});
  }
  if (!modal.classList.contains('smart-popover-popup') || !modal.classList.contains(modalClass) ||
      modal.getAttribute('role') !== 'dialog' || candidates.length !== 1 || candidates[0] !== modal)
    throw new Error('配置确认框归属或数量不明确');
  if (req.action === 'ordinary' && !modal.classList.contains('sc-reset-modal-from-custom-order'))
    throw new Error('普通配置确认框不是返回配置列表操作');
  if (req.action === 'custom' && modal.classList.contains('sc-reset-modal-from-custom-order'))
    throw new Error('定制配置确认框方向不对应');
  const buttons = [...modal.querySelectorAll(selector)].filter(visible);
  if (buttons.length !== 1 || buttons[0].tagName !== 'BUTTON' || buttons[0].hasAttribute('disabled') ||
      buttons[0].getAttribute('aria-disabled') === 'true') throw new Error('配置确认按钮未唯一识别');
  const target = buttons[0];
  const expectedAction = req.action === 'select' ? 'OptionSelectionChangeAccept' : 'ResetSelections';
  if (target.getAttribute('data-user-action') !== expectedAction) throw new Error('配置确认按钮动作不对应');
  if (/add.?to.?cart|checkout|purchase|quote/i.test(target.getAttribute('data-user-action') || '') ||
      /add to cart|buy now|checkout|add to quote/i.test(clean(target.textContent))) throw new Error('确认按钮属于购买操作；没有点击');
  if (!/^(prepare|hit)$/.test(req.dom_phase||'')) throw new Error('配置确认缺少点击阶段');
  if (req.dom_phase === 'prepare') target.scrollIntoView({behavior:'instant',block:'center',inline:'center'});
  const rect = target.getBoundingClientRect(), left=Math.max(0,rect.left), top=Math.max(0,rect.top),
    right=Math.min(innerWidth,rect.right), bottom=Math.min(innerHeight,rect.bottom);
  if (!(right>left && bottom>top)) throw new Error('配置确认按钮没有可点击区域');
  const point=req.dom_phase==='hit'?{x:req.point_x,y:req.point_y}:{x:(left+right)/2,y:(top+bottom)/2};
  if (!Number.isFinite(point.x)||!Number.isFinite(point.y)||point.x<=left||point.x>=right||point.y<=top||point.y>=bottom)
    throw new Error('配置确认按钮位置已变化');
  const hit=document.elementFromPoint(point.x,point.y);
  if (!hit || !target.contains(hit) || (hit!==target && /^(A|BUTTON|INPUT|SELECT|TEXTAREA)$/.test(hit.tagName)))
    throw new Error('配置确认按钮被其他控件遮挡');
  if (!matches()) throw new Error('确认前商品网址已变化');
  return JSON.stringify({...identity, needed:true, modalID, targetID:target.id||'scrm-continue',
    label:clean(target.textContent), point});
})
