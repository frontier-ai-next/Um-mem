import {layoutHierarchy,separateOverlaps,zoomAt,fitBounds,mapProjection,graphTransition,sampleGraphTransition} from './graph-layout.mjs';
import {scopeBlocks,scopeCount,createBranchPager,filterBlocks,filterRecords,sortRecords,panelPreferences,readAllRecords} from './branch-records.mjs';
const $ = id => document.getElementById(id);
const state = {snapshot:null, records:[], browseRecords:[], block:'', cursor:'', scope:{scope:'workspace',scopeId:''}, scopeLimited:false, view:'memory', mode:'list',
  selected:'', detail:null, draft:null, query:'', events:[], positions:{}, manualNodes:new Set(),
  treeExpanded:new Set(), treeReady:false, descendants:true, pager:null, browseRequest:0, recordsLoading:false, recordsError:'',
  blockQuery:'', hideEmpty:false, recordStatus:'', recordKind:'', recordSort:'newest', recordLimit:64, recordsScanned:0, preferencesReady:false,
  graphFocus:false, graphOverview:false, focusExpanded:new Set(), contextExpanded:new Set(), drawPositions:{},
  expanded:new Set(), branchRecords:new Map(), graphBusy:new Set(), graphOffset:{x:0,y:0}, layoutAnchor:null, graphReady:false,
  transform:{x:0,y:0,k:1}, source:null, refreshing:false, pending:false, detailRequest:0,
  searchRequest:0, toastTimer:0, selectedTopic:'', fitNeeded:true, stopped:false, viewport:{width:0,height:0}};
const statusNames = {candidate:'Не проверено',accepted:'Подтверждено',active:'Действует',
  retracted:'Отозвано',superseded:'Заменено',deprecated:'Устарело',expired:'Срок истёк',quarantined:'Карантин'};
const kindNames = {observation:'Наблюдение',project_fact:'Факт о проекте',procedure:'Процедура',
  rule:'Правило',lesson:'Вывод',fact:'Факт',memory:'Запись',finding:'Результат исследования',source_card:'Карточка источника',session_checkpoint:'Итог сессии'};
const operationNames = {remember:'Добавлена запись',amend:'Изменена запись',forget:'Запись отозвана',
  create_block:'Создана тема',amend_block:'Изменена тема',move_block:'Перемещена тема',
  archive_block:'Тема архивирована',delete_block:'Тема удалена',set_active_mask:'Выбран рабочий контекст',
  amend_active_mask:'Изменён рабочий контекст',transition_active_mask:'Изменено состояние контекста',
  recall:'Поиск в памяти',build_context:'Сборка контекста',resolve_active_mask:'Разрешение контекста',
  indexing:'Индексы обновлены'};
function element(tag, className='', text='') {
  const node=document.createElement(tag); if(className) node.className=className;
  node.textContent=text; return node;
}
function button(text, className, action) {
  const node=element('button',className,text); node.type='button'; node.addEventListener('click',action); return node;
}
const reducedMotion=matchMedia('(prefers-reduced-motion: reduce)');
function enterView(node){
  if(reducedMotion.matches)return;
  node.getAnimations().forEach(animation=>animation.cancel());
  node.animate([{opacity:.35,transform:'translateY(5px)'},{opacity:1,transform:'translateY(0)'}],{duration:220,easing:'ease-out'});
}
function badge(status) {
  const node=element('span',`badge ${status}`,statusNames[status]||status||'Без статуса');
  if(status==='candidate')node.title='Запись ещё не проверена и не становится правилом для агента.';
  return node;
}
function date(value) {const d=new Date(value); return isNaN(d)?'Без даты':d.toLocaleString('ru',{day:'numeric',month:'short',hour:'2-digit',minute:'2-digit'});}
function flatten(nodes, out=[]) {for(const n of nodes||[]){out.push(n);flatten(n.children,out);}return out;}
function topic(id) {return flatten(state.snapshot?.tree).find(n=>n.block_id===id);}
// Logical blocks hold the records but are not nodes of the tree, so `topic()`
// cannot name them. Without this map every card fell back to "Общая память",
// which hid the only distinction the reader cares about.
const BLOCK_LABELS={user_global_preferences:'Предпочтения',user_global_core_directives:'Директивы',workspace_core_directives:'Правила',decisions_and_contracts:'Договорённости',stable_project_facts:'Факты',mutable_project_state:'Активная память',task_state:'Задача',execution_working_state:'Ход работы',reflection_candidates:'Выводы',verified_lessons:'Уроки',project_ideas:'Идеи',workspace_episodes:'Эпизоды',durable_reports:'Отчёты',artifact_claims:'Артефакты',code_resources:'Код',project_resources:'Ресурсы',skills_index:'Навыки',transient_logs:'Логи'};
function blockLabel(id){return BLOCK_LABELS[id]||topic(id)?.name||id||'Общая память';}
function blockScopeHint() {
  const block=topic(state.block);if(!block)return '';
  const blockScope=block.scope||'workspace';
  if(blockScope==='project'&&state.scope.scope!=='project')return 'Блок проектного уровня. Выберите проект в фильтре «Память».';
  if(blockScope==='workspace'&&state.scope.scope==='project')return 'Блок общего пространства. Выберите «Общая память» в фильтре «Память».';
  return '';
}
function scopeLabel(){const id=state.scope.scopeId;return state.scope.scope==='project'?`Проект ${id.length>24?id.slice(0,10)+'…'+id.slice(-8):id}`:'Общая память';}
function scopeQuery(){const query=new URLSearchParams({scope:state.scope.scope});if(state.scope.scope==='project')query.set('scope_id',state.scope.scopeId);return query.toString();}
function recordsUrl(block,cursor){const query=new URLSearchParams({scope:state.scope.scope,block:block||'',cursor:cursor||''});if(state.scope.scope==='project')query.set('scope_id',state.scope.scopeId);return `api/records?${query}`;}
function renderMemoryScope(){
  if(!state.snapshot)return;
  const select=$('memory-scope'),projects=new Set([...(state.snapshot.project_ids||[]),...(state.snapshot.sessions||[]).map(s=>s.project_id).filter(Boolean)]);
  if(state.scope.scope==='project')projects.add(state.scope.scopeId);
  select.replaceChildren(new Option('Общая память','workspace'));
  for(const id of [...projects].sort())select.add(new Option(`Проект ${id}`,`project:${id}`));
  select.add(new Option('Другой проект…','manual'));
  select.value=state.scope.scope==='project'?`project:${state.scope.scopeId}`:'workspace';
  $('project-scope-form').hidden=select.value!=='manual';
}
async function changeMemoryScope(scope,scopeId=''){
  if(!await allowLeave()){renderMemoryScope();return;}
  state.scope={scope,scopeId};state.block='';state.records=[];state.browseRecords=[];state.cursor='';state.pager=null;state.browseRequest++;
  state.query='';$('query').value='';$('search-info').hidden=true;state.detailRequest++;state.draft=null;state.selected='';state.selectedTopic='';setDetailOpen(false);
  state.preferencesReady=false;state.expanded.clear();state.branchRecords.clear();await refresh();
}
function toast(text) {clearTimeout(state.toastTimer);$('toast').textContent=text;$('toast').hidden=false;state.toastTimer=setTimeout(()=>$('toast').hidden=true,5500);}
function banner(text='') {$('banner').textContent=text;$('banner').hidden=!text;}
function canWrite(){return Boolean(state.snapshot&&(!state.snapshot.read_only||state.snapshot.editing_available)&&!state.stopped&&!state.snapshot.health.background_queue?.emergency_read_only);}
let detailMotion;
function setDetailOpen(open){
  const pane=$('detail');detailMotion?.cancel();detailMotion=null;
  if(open){const entering=pane.hidden;pane.hidden=false;if(entering&&!reducedMotion.matches)detailMotion=pane.animate([{opacity:0,transform:'translateX(24px)'},{opacity:1,transform:'none'}],{duration:260,easing:'cubic-bezier(.2,.8,.2,1)'});}
  else if(!pane.hidden&&!reducedMotion.matches){
    const motion=pane.animate([{opacity:1,transform:'none'},{opacity:0,transform:'translateX(24px)'}],{duration:180,easing:'ease-in'});detailMotion=motion;
    motion.finished.then(()=>{if(detailMotion===motion){pane.hidden=true;detailMotion=null;}}).catch(()=>{});
  }else pane.hidden=!open;
  renderSelectionTools();
}
function selectedBlock(){return topic(state.selectedTopic||(state.selected&&(state.detail?.record?.logical_block_id||state.detail?.record?.block_membership?.primary_block_id))||state.block);}
function renderSelectionTools(){
  const record=state.selected?state.detail?.record:null,b=selectedBlock(),busy=Boolean(state.draft?.saving);
  const blockEditable=Boolean(b&&!b.protected&&!b.meta&&Number(b.revision)>0&&b.status!=='archived');
  const editable=record?Boolean(state.detail?.is_current):blockEditable;
  $('selection-edit').disabled=!canWrite()||!editable||busy;
  $('selection-edit').title=!canWrite()?'Редактирование доступно в локальной сессии редактора':record&&!state.detail.is_current?'Предыдущая версия доступна для чтения':record?'Редактировать':b?.protected?'Системный блок защищён':b?.meta?'Системный блок: только чтение':'Редактировать';
  $('selection-edit').setAttribute('aria-pressed',String(Boolean(state.draft)));
  $('selection-add').disabled=!canWrite()||busy||!flatten(state.snapshot?.tree).some(n=>n.writable&&!n.meta);
  $('selection-focus').disabled=!(record||b);$('selection-info').disabled=!(record||b);
  const locationLabel=record?'Показать расположение записи в графе':b?'Показать расположение блока в графе':'Показать расположение в графе';
  $('selection-focus').setAttribute('aria-label',locationLabel);$('selection-focus').title=locationLabel;
  $('selection-history').hidden=!record;$('selection-parent').hidden=!b?.parent_id;
  $('selection-more').hidden=!record&&!b?.parent_id;
}
async function api(path,body) {
  const url=new URL(`/panel/${path}`,location.origin),context=new URLSearchParams(location.search);
  for(const name of ['workspace','archived'])if(context.has(name))url.searchParams.set(name,context.get(name));
  const response=await fetch(url,{credentials:'same-origin',headers:body?{'Content-Type':'application/json'}:{},
    method:body?'POST':'GET',body:body?JSON.stringify(body):undefined,cache:'no-store'});
  const data=await response.json();
  if(!response.ok||data.ok===false||data.error) {
    const error=new Error(data.error||'Не удалось выполнить запрос');error.data=data;error.status=response.status;throw error;
  }
  return data;
}
function connected(value) {
  const s=state.snapshot;
  $('connection').textContent=s?.editing_available?'Локальный редактор':s?.read_only?'Только чтение':value?(s?.health.ok?'Синхронизировано':'Нужна проверка'):'Переподключение…';
  $('connection-dot').classList.toggle('online',value&&s?.health.ok);
  $('connection-dot').title=[$('connection').textContent,s?.backend,s?.profile&&`Профиль: ${s.profile}`,
    s?.read_only?`Срез ${date(s.snapshot_at)}`:s&&`Обновлено ${new Date().toLocaleTimeString('ru')}`].filter(Boolean).join(' · ');
}
function renderSummary() {
  const s=state.snapshot, nodes=flatten(s.tree).filter(n=>!n.meta);
  renderMemoryScope();
  $('workspace').textContent=s.workspace_label||s.workspaces?.find(w=>w.key===s.workspace_key)?.label||s.workspace||'Память';
  $('workspace').title=[s.workspace,`Профиль: ${s.profile}`,s.source_path].filter(Boolean).join('\n');
  $('stat-topics').textContent=s.read_only?nodes.length:nodes.filter(n=>n.parent_id).length;
  $('stat-records').textContent=scopeCount(flatten(s.tree));
  $('tree-count').textContent=nodes.length;
  document.body.classList.toggle('read-only',Boolean(s.read_only));
  $('data-controls').hidden=!s.read_only;
  $('stop-panel').hidden=Boolean(s.read_only);
  document.querySelectorAll('[data-view]').forEach(b=>{b.disabled=Boolean(s.read_only&&b.dataset.view!=='memory');});
  if(s.read_only){
    const spaces=s.workspaces||[];
    $('workspace-name').textContent=s.workspace_label||spaces.find(w=>w.key===s.workspace_key)?.label||'Пространства не найдены';
    $('workspace-options').replaceChildren(...spaces.map(w=>{
      const choice=button(w.label,'workspace-choice',()=>selectWorkspace(w.key));choice.dataset.workspace=w.key;
      choice.title=[w.source_path,w.workspace_id,w.profile,w.backend].filter(Boolean).join(' · ');
      if(w.key===s.workspace_key)choice.setAttribute('aria-current','true');return choice;
    }));
    if(!spaces.length)$('workspace-options').append(element('p','workspace-notice','Доступных пространств пока нет.'));
    const issues=s.discovery_issues?.length?s.discovery_issues:s.discovery_notice?[s.discovery_notice]:[];
    $('workspace-issues').replaceChildren(...issues.map(issue=>element('li','',issue)));
    $('workspace-unavailable').hidden=!issues.length;
    $('workspace-notice').textContent=s.selection_notice||'';$('workspace-notice').hidden=!s.selection_notice;
    $('show-archived').checked=s.include_archived;
    $('archive-label').textContent=`Архив (${s.data_stats.archived_blocks})`;
    $('workspace-trigger').title=`Выбрать пространство памяти · ${spaces.length} доступно${issues.length?` · ${issues.length} недоступно`:''}`;
  }
  renderTopics();
  $('new-record').disabled=!canWrite();$('new-record').hidden=true;
  renderLocation();renderSelectionTools();
}
function revealTreePath(id){
  let node=topic(id);const seen=new Set();
  while(node&&!seen.has(node.block_id)){seen.add(node.block_id);if(node.parent_id)state.treeExpanded.add(node.parent_id);node=topic(node.parent_id);}
}
function renderTopics() {
  const list=$('topics'),scroll=list.scrollTop,focused=document.activeElement;
  const oldRows=[...list.querySelectorAll('.topic-row')];
  const previous=new Map(oldRows.map(row=>[row.dataset.rowId,{height:row.getBoundingClientRect().height,opacity:Number(getComputedStyle(row).opacity)}]));
  for(const row of oldRows)row.getAnimations().forEach(animation=>animation.cancel());
  const focusId=focused?.dataset.treeId,focusAction=focused?.dataset.treeAction;
  list.replaceChildren();
  const ancestors=new Set();let ancestor=topic(state.block);
  while(ancestor&&!ancestors.has(ancestor.block_id)){ancestors.add(ancestor.block_id);ancestor=topic(ancestor.parent_id);}
  const roots=state.snapshot?.tree||[];
  const filtered=filterBlocks(roots,{query:state.blockQuery,hideEmpty:state.hideEmpty});
  $('hide-empty').checked=state.hideEmpty;$('clear-block-query').hidden=!state.blockQuery;
  const note=$('block-filter-note');note.hidden=!state.blockQuery;
  note.textContent=state.blockQuery?`Найдено блоков: ${filtered.matches.size}`:'';
  if(!state.treeReady){state.treeReady=true;for(const root of roots)state.treeExpanded.add(root.block_id);}
  function appendNode(n,depth){
    if(n.meta||!filtered.visible.has(n.block_id))return;
    const row=element('div','topic-row');row.dataset.rowId=n.block_id;row.style.setProperty('--depth',depth);
    const children=(n.children||[]).filter(child=>filtered.visible.has(child.block_id));
    const expanded=state.blockQuery?filtered.expanded.has(n.block_id):state.treeExpanded.has(n.block_id);
    if(children.length){
      const toggle=button(expanded?'▾':'▸','topic-toggle',()=>{
        if(expanded)state.treeExpanded.delete(n.block_id);else state.treeExpanded.add(n.block_id);
        renderTopics();storeLayout();
      });
      toggle.disabled=Boolean(state.blockQuery);
      toggle.setAttribute('aria-label',`${expanded?'Свернуть':'Раскрыть'} ${n.name}`);
      toggle.setAttribute('aria-expanded',String(expanded));toggle.dataset.treeId=n.block_id;toggle.dataset.treeAction='toggle';row.append(toggle);
    }else row.append(element('span','topic-spacer'));
    const b=button('',`topic ${state.block===n.block_id?'selected':ancestors.has(n.block_id)?'ancestor':''}`,()=>selectBlock(n.block_id));
    if(state.block===n.block_id)b.setAttribute('aria-current','true');
    b.dataset.treeId=n.block_id;b.dataset.treeAction='select';b.title=`${n.name} · ${counted(blockRecordCount(n),['запись','записи','записей'])} с вложенными блоками`;
    b.style.setProperty('--node-color',colorFor(n.block_id));
    b.classList.toggle('match',filtered.matches.has(n.block_id));
    b.append(element('span','topic-name',n.name),element('small','',String(blockRecordCount(n))));row.append(b);list.append(row);
    if(expanded)for(const child of children)appendNode(child,depth+1);
  }
  const visibleRoots=roots.length===1&&roots[0].children?.length&&!filtered.matches.has(roots[0].block_id)?roots[0].children:roots;
  for(const root of visibleRoots)appendNode(root,0);
  if(!list.childElementCount)list.append(element('p','tree-empty',state.blockQuery?'Блоки не найдены. Измените поиск или отключите скрытие пустых веток.':'Все ветки пусты. Отключите скрытие, чтобы увидеть их.'));
  if(!reducedMotion.matches&&previous.size){
    const rows=new Map([...list.querySelectorAll('.topic-row')].map(row=>[row.dataset.rowId,row]));
    const heights=new Map([...rows].map(([id,row])=>[id,row.getBoundingClientRect().height]));
    function animateRow(row,from,to,remove=false){
      row.style.overflow='hidden';
      row.animate([{height:from.height+'px',opacity:from.opacity},{height:to.height+'px',opacity:to.opacity}],
        {duration:360,easing:'cubic-bezier(.4,0,.2,1)',fill:'both'}).finished.then(()=>{
          if(remove)row.remove();else{row.getAnimations().forEach(animation=>animation.cancel());row.style.overflow='';}
        }).catch(()=>{});
    }
    // Keep outgoing rows in document flow until their height reaches zero.
    let next=null;
    for(const row of [...oldRows].reverse()){
      const id=row.dataset.rowId;
      if(rows.has(id)){next=rows.get(id);continue;}
      row.inert=true;row.setAttribute('aria-hidden','true');list.insertBefore(row,next);next=row;
      animateRow(row,previous.get(id),{height:0,opacity:0},true);
    }
    for(const [id,row]of rows){
      const from=previous.get(id)||{height:0,opacity:0},to={height:heights.get(id),opacity:1};
      if(from.height!==to.height||from.opacity!==1)animateRow(row,from,to);
    }
  }
  list.scrollTop=scroll;
  if(focusId)list.querySelector(`[data-tree-id="${CSS.escape(focusId)}"][data-tree-action="${focusAction}"]`)?.focus({preventScroll:true});
}
function selectedBlocks(){return scopeBlocks(state.snapshot?.tree,state.block,state.descendants);}
function recordsTotal(){return state.scope.scope==='project'?state.browseRecords.length:scopeCount(selectedBlocks());}
function recordFilters(){return {status:state.recordStatus,kind:state.recordKind};}
function hasRecordFilters(){return Boolean(state.recordStatus||state.recordKind);}
function renderRecordFilters(){
  $('record-sort').value=state.recordSort;
  const rows=[...(state.snapshot?.records||[]),...state.browseRecords,...state.records];
  for(const [id,key,names,value,label] of [['record-status','status',statusNames,state.recordStatus,'Все статусы'],['record-kind','kind',kindNames,state.recordKind,'Все типы']]){
    const select=$(id),values=[...new Set([...Object.keys(names),...rows.map(r=>r[key]),value].filter(Boolean))];
    const options=[element('option','',label),...values.map(key=>{const option=element('option','',names[key]||key);option.value=key;return option;})];options[0].value='';
    select.replaceChildren(...options);select.value=value;
  }
  $('reset-record-filters').hidden=!hasRecordFilters();
  const count=Number(Boolean(state.recordStatus))+Number(Boolean(state.recordKind));
  $('filter-count').hidden=!count;$('filter-count').textContent=count;
  $('toggle-filters').classList.toggle('has-filters',Boolean(count));
  const chips=$('active-filters');chips.replaceChildren();
  for(const [id,value,names]of [['record-status',state.recordStatus,statusNames],['record-kind',state.recordKind,kindNames]])if(value){
    const label=names[value]||value,chip=button(`${label} ×`,'filter-chip',()=>{$(id).value='';void changeRecordFilters();});
    chip.setAttribute('aria-label',`Убрать фильтр: ${label}`);chips.append(chip);
  }
}
async function loadRecords({more=false,preserve=false}={}){
  if(more&&(state.recordsLoading||!state.pager?.hasMore))return;
  const request=++state.browseRequest,selected=state.block;
  const pager=more?state.pager:createBranchPager(selectedBlocks(),async(id,cursor)=>{const page=await api(recordsUrl(id,cursor));state.scopeLimited ||=Boolean(page.scope_limited);return page;});
  if(!more&&!preserve)state.recordLimit=64;
  if(!more)state.scopeLimited=false;
  state.recordsLoading=true;state.recordsScanned=0;state.recordsError='';renderRecords();
  try{
    const records=[...(more?state.browseRecords:[]),...await readAllRecords(pager,()=>request===state.browseRequest,count=>{state.recordsScanned=count;$('collection-note').textContent=`Проверено ${count} из ${recordsTotal()}…`;})];
    if(request!==state.browseRequest||selected!==state.block)return;
    if(more)state.recordLimit=records.length;
    state.pager=pager;state.browseRecords=records;state.cursor=pager.hasMore?'more':'';
    if(!state.query)state.records=records;
  }catch(error){if(request===state.browseRequest){state.recordsError=error.message;state.pager=more?pager:null;}}
  finally{if(request===state.browseRequest){state.recordsLoading=false;renderRecords();}}
}
function renderRecords() {
  renderRecordFilters();
  const filtered=hasRecordFilters(),matches=sortRecords(filterRecords(state.records,recordFilters()),state.recordSort),visible=matches.slice(0,state.recordLimit);
  $('collection-title').textContent=state.query?'Результаты поиска':state.block?(state.descendants?'Записи ветки':'Записи блока'):'Все записи';
  $('collection-title').hidden=false;
  $('collection-note').textContent=state.recordsError?'Загрузка не завершена':state.recordsLoading?(filtered?`Проверено ${state.recordsScanned} из ${recordsTotal()}…`:'Загрузка…'):state.scopeLimited?`Показаны последние ${visible.length} записей; сузьте область до блока`:filtered?`${visible.length} из ${matches.length} по фильтрам`:state.query?counted(matches.length,['запись','записи','записей']):`${visible.length} из ${recordsTotal()}`;
  $('collection-note').title=state.query?'Фильтры применены к результатам поиска':filtered?'Показано / всего совпадений в выбранной области':'Загружено / записей в выбранной области по каталогу';
  $('record-scope').value=!state.block||state.descendants?'branch':'block';$('record-scope').disabled=!state.block;
  $('scope-caption').textContent=state.block?`${scopeLabel()} · ${counted(recordsTotal(),['запись','записи','записей'])} ${state.descendants?'в ветке':'в самом блоке'}`:`${counted(recordsTotal(),['запись','записи','записей'])} · ${scopeLabel()}`;
  $('query').placeholder=state.block?(state.descendants?'Поиск в выбранной ветке':'Поиск в выбранном блоке'):'Поиск по памяти';
  const list=$('list'),signature=JSON.stringify([state.block,state.query,state.recordStatus,state.recordKind,state.recordSort,visible.map(r=>r.record_id)]);
  const changed=list.dataset.signature!==signature;list.dataset.signature=signature;list.replaceChildren();
  if(state.recordsError){const error=element('div','empty');error.append(element('strong','','Не удалось загрузить записи'),element('span','',state.recordsError),button('Повторить','secondary',()=>loadRecords({more:Boolean(state.pager)})));list.append(error);}
  else if(!visible.length){const e=element('div','empty'),scopeHint=blockScopeHint();e.append(element('strong','',state.recordsLoading?'Загружаем записи…':state.query||filtered?'Подходящих записей нет':!state.descendants&&state.block?'В самом блоке нет записей':'Здесь пока нет записей'),
    element('span','',state.recordsLoading?'':filtered?'Измените статус или тип, либо сбросьте фильтры.':state.query?'Попробуйте другой запрос.':scopeHint?scopeHint:!state.descendants&&state.block?'Выберите «С вложенными блоками», чтобы посмотреть всю ветку.':'Выберите другой блок.'));list.append(e);}
  for(const r of visible) {
    const card=button('',`record-card ${r.record_id===state.selected?'selected':''}`,()=>openRecord(r.record_id));
    card.style.setProperty('--node-color',colorFor(r.logical_block_id));
    const top=element('div','card-top'),kind=element('div','record-kind');
    kind.append(icon(recordIcon(r.kind)),element('span','',kindNames[r.kind]||r.kind||'Запись'));
    top.append(kind,badge(r.status));
    const bottom=element('div','card-bottom');bottom.append(element('span','',blockLabel(r.logical_block_id)),
      element('span','',date(r.recorded_at)));
    if(r.score!==undefined)card.title=`${r.score_mode==='semantic_cosine'?'Сходство':'Оценка'}: ${Number(r.score).toFixed(3)}. Оценка поиска не подтверждает достоверность записи.`;
    card.append(top,element('h3','',r.title||'Без названия'),element('p','',r.preview||''),bottom);list.append(card);
  }
  if(changed&&!state.recordsLoading)enterView(list);
  $('load-more').hidden=state.mode!=='list'||(visible.length>=matches.length&&(filtered||!state.cursor||Boolean(state.query)));$('load-more').disabled=state.recordsLoading;renderGraph();
}
function renderEvents() {
  const events=[...state.events].reverse();
  for(const [target,filter] of [['activity',e=>e.kind!=='retrieval'],['context-events',e=>e.kind==='retrieval']]) {
    const container=$(target);container.replaceChildren();const selected=events.filter(filter);
    if(!selected.length) container.append(element('div','empty','Событий пока нет'));
    for(const e of selected) {
      const row=element('div','event'),body=element('div');
      body.append(element('strong','',`${operationNames[e.operation]||e.operation}${e.ok?'':' · не выполнено'}`));
      body.append(element('p','',`${e.session_id||'Сервис'}${e.task_id?' / '+e.task_id:''}`));
      if(e.kind==='retrieval') body.append(element('p','',`${e.count} записей найдено${e.rendered?' · контекст сформирован':''}.`));
      if(e.commit_revision) body.append(element('p','',`Сохранено: ${e.commit_revision} · проекция: ${e.projection_revision||'ожидает подтверждения'}`));
      if(e.reason_code) body.append(element('p','',e.reason_code));
      for(const id of e.record_ids||[])body.append(button(`${id.slice(0,24)} ↗`,'',()=>openRecord(id)));
      row.append(element('span','event-icon',e.ok?'↗':'!'),body,element('time','',date(e.at)));container.append(row);
    }
  }
  const sessions=$('sessions');sessions.replaceChildren();
  for(const s of state.snapshot?.sessions||[]) {
    if(s.session_id.startsWith('panel')) continue;
    sessions.append(element('span','session-chip',`${s.session_id}${s.task_id?' / '+s.task_id:''}`));
  }
}
function mergeEvents(events, cursor) {
  const epoch=cursor.split(':')[0], byId=new Map();
  for(const e of [...(events||[]),...state.events])if(e.id.split(':')[0]===epoch)byId.set(e.id,e);
  state.events=[...byId.values()].sort((a,b)=>Number(a.id.split(':')[1])-Number(b.id.split(':')[1])).slice(-256);
}
async function selectBlock(id) {
  if(!await allowLeave())return;
  storeLayout();state.detailRequest++;state.draft=null;state.selected='';state.selectedTopic='';setDetailOpen(false);
  state.records=[];state.browseRecords=[];state.cursor='';state.pager=null;state.browseRequest++;
  state.block=id;state.query='';$('query').value='';$('search-info').hidden=true;
  state.graphFocus=false;state.graphOverview=false;state.contextExpanded.clear();state.focusExpanded=new Set(id?[id]:[]);revealTreePath(id);
  let node=topic(id);const seen=new Set();
  while(node&&!seen.has(node.block_id)){seen.add(node.block_id);state.expanded.add(node.block_id);node=topic(node.parent_id);}
  state.searchRequest++;setView('memory');toggleTopics(false);
  try{
    renderSummary();renderRecords();
    await Promise.all([loadRecords(),...([...seen].map(block=>loadBranch(block)))]);
    if(state.block!==id)return;
    renderSummary();renderRecords();if(state.mode==='graph'){if(id)focusGraph(id);else fitGraph();}else state.fitNeeded=true;storeLayout();
  }catch(error){toast(error.message);}
}
async function refresh() {
  if(state.refreshing){state.pending=true;return;}
  state.refreshing=true;
  try {
    const snapshot=await api(`api/snapshot?${scopeQuery()}`);state.snapshot=snapshot;mergeEvents(snapshot.events,snapshot.cursor);(function walk(list,depth){for(const b of list||[]){if(depth<=1||b.relation_count)state.contextExpanded.add(b.block_id);walk(b.children,depth+1);}})(snapshot.tree||[],0);
    if(snapshot.read_only&&snapshot.workspace_key){
      const url=new URL(location.href);
      if(url.searchParams.get('workspace')!==snapshot.workspace_key){url.searchParams.set('workspace',snapshot.workspace_key);history.replaceState(null,'',url);}
    }
    if(!state.preferencesReady){restoreLayout();state.preferencesReady=true;}
    renderSummary();renderEvents();
    if(state.block&&!topic(state.block)){state.block='';state.focusExpanded.clear();state.records=[];state.browseRecords=[];}
    await loadRecords({preserve:true});
    for(const id of new Set([...state.expanded,...state.focusExpanded]))if(topic(id))await loadBranch(id,{force:true});
    if(state.query)await search(state.query);
    renderRecords();banner();
    if(state.selected) await checkSelected();
    else if(state.selectedTopic){if(state.draft?.type==='block'){if(topic(state.selectedTopic)?.revision!==state.draft.expected_revision)showBlockConflict();}else renderBlockDetail(state.selectedTopic);}
  } catch(error){banner(error.message);}finally {
    state.refreshing=false;if(state.pending){state.pending=false;void refresh();}
  }
}
async function checkSelected() {
  const id=state.selected;
  try {
    const data=await api(`api/record?id=${encodeURIComponent(id)}`);if(state.selected!==id)return;
    state.detail=data;
    if(state.draft) {if(!data.is_current&&!state.draft.isNew) showConflict();}
    else renderDetail(data);
  }catch(error){if(state.selected===id)banner(error.message);}
}
async function search(query) {
  const request=++state.searchRequest;state.query=query.trim();state.cursor='';state.recordLimit=64;
  if(!state.query){$('search-info').hidden=true;return refresh();}
  $('search-info').hidden=false;$('search-info').textContent='Поиск…';$('search-info').title='';
  try {
    if(state.scope.scope==='project'){
      const needle=state.query.toLocaleLowerCase();state.records=state.browseRecords.filter(r=>(r.title+' '+(r.preview||'')).toLocaleLowerCase().includes(needle));
      $('search-info').textContent=`Поиск по загруженным записям · ${scopeLabel()}`;setMode('list');renderRecords();return;
    }
    const arguments_={query:state.query,include_candidates:true};
    if(state.block)arguments_.logical_block_ids=selectedBlocks().filter(n=>n.canonical_record_count!==0&&!n.index_only).map(n=>n.block_id);
    if(state.block&&!arguments_.logical_block_ids.length){state.records=[];$('search-info').textContent='В выбранной области нет записей';setMode('list');renderRecords();return;}
    const data=await api('api/call',{tool:'recall',arguments:arguments_});
    if(request!==state.searchRequest)return;
    state.records=(data.hits||data.items||[]).map(h=>{
      const m=h.metadata||{};return {record_id:h.record_id||m.record_id||m.block_id||h.source,
        title:h.title||m.title||'Найденная запись',preview:h.text||h.concise_text||h.content,
        status:h.status||m.status,kind:h.kind||m.kind,logical_block_id:h.logical_block_id||m.logical_block_id,
        recorded_at:h.recorded_at||m.recorded_at,score:h.score,score_mode:h.score_mode||m.native_score_mode};
    }).filter(r=>r.record_id);
    // The operator can inspect visible candidates even when agent recall
    // excludes them. This local title/preview match grants no agent authority.
    const found=new Set(state.records.map(r=>r.record_id)),needle=state.query.toLocaleLowerCase();
    const inspected=state.browseRecords.filter(r=>!found.has(r.record_id)&&(r.title+' '+(r.preview||'')).toLocaleLowerCase().includes(needle));
    state.records.push(...inspected);
    $('search-info').textContent=data.fallback==='semantic'?'По смыслу':data.fallback==='unicode_ngram'?'По тексту':'Поиск в памяти';
    $('search-info').title=[data.semantic_model,'Оценка поиска не подтверждает достоверность записи.',
      inspected.length?'Включены совпадения в загруженных названиях и описаниях.':''].filter(Boolean).join(' ');
    setMode('list');renderRecords();
  }catch(error){$('search-info').textContent=error.message;}
}
function setView(view) {
  state.view=view;
  for(const name of ['memory','activity','context'])$(`${name}-view`).hidden=name!==view;
  document.querySelectorAll('[data-view]').forEach(b=>b.classList.toggle('active',b.dataset.view===view));
  $('page-title').textContent={memory:topic(state.block)?.name||'Память',activity:'Изменения',context:'Контекст'}[view];
  $('page-title').title='';
  $('breadcrumbs').hidden=true;
  $('new-record').hidden=true;
  if(view==='memory')renderLocation();
}
let discardPending=false;
async function allowLeave() {
  const draft=state.draft;
  if(draft?.saving){toast('Дождитесь результата сохранения.');return false;}
  if(!draft?.dirty)return true;
  if(discardPending)return false;
  discardPending=true;
  const dialog=$('discard-dialog');dialog.returnValue='keep';
  return new Promise(resolve=>{
    dialog.addEventListener('close',()=>{
      discardPending=false;const discard=dialog.returnValue==='discard'&&state.draft===draft&&!draft.saving;
      if(discard)draft.dirty=false;
      resolve(discard);
    },{once:true});
    dialog.showModal();
  });
}
async function openRecord(id) {
  if(!await allowLeave())return;
  const request=++state.detailRequest;state.draft=null;state.detail=null;state.selectedTopic='';state.selected=id;setDetailOpen(true);$('detail-label').textContent='ЗАПИСЬ ПАМЯТИ';
  $('detail-content').replaceChildren(element('p','metadata','Загрузка…'));
  try {const data=await api(`api/record?id=${encodeURIComponent(id)}`);
    if(request!==state.detailRequest)return;state.detail=data;renderDetail(data);renderRecords();
  }catch(error){if(request===state.detailRequest)$('detail-content').replaceChildren(element('p','conflict',error.message));}
}
function renderDetail(data) {
  if(state.draft)return;
  const r=data.record, box=$('detail-content');box.replaceChildren();
  $('detail').style.setProperty('--node-color',colorFor(r.logical_block_id||state.block));
  const kind=element('div','detail-kind');kind.append(icon(recordIcon(r.kind)),element('span','',kindNames[r.kind]||r.kind||'Запись'));box.append(kind);
  box.append(badge(r.status),element('h2','',r.title||'Без названия'),element('p','metadata',
    `${date(r.recorded_at)} · ${kindNames[r.kind]||r.kind}`));
  if(!data.is_current)box.append(element('p','conflict','Предыдущая версия записи'));
  box.append(element('div','record-body',r.content));
  if(data.is_current&&canWrite()){const edit=button(r.status==='candidate'?'Редактировать запись':'Предложить уточнение','text-button detail-edit',()=>editRecord(r));edit.prepend(icon('edit'));box.append(edit);}
  const history=element('details');history.id='record-history';history.append(element('summary','',`История · ${counted(data.history.length,['версия','версии','версий'])}`));
  for(const version of [...data.history].reverse()) {
    const b=button(`${statusNames[version.status]||version.status} ${version.is_current?'· текущая':''}`,'history-row',()=>openRecord(version.record_id));
    b.append(element('small','',date(version.recorded_at)));history.append(b);
  }
  if(data.history_truncated)history.append(element('p','metadata','Показаны последние 128 версий.'));
  const sources=element('details'),refs=r.evidence_refs||[];
  sources.append(element('summary','',`Источники · ${refs.length}`));
  if(!refs.length)sources.append(element('p','metadata','Источники не указаны.'));
  for(const ref of refs){
    const row=element('div','source-row'),label=String(ref.ref_id||ref.source_id||'Без названия');
    row.append(element('small','metadata',ref.ref_type==='document'?'Документ':ref.ref_type==='url'?'Ссылка':ref.ref_type||'Источник'));
    let url;try{url=new URL(label);}catch{/* Local paths remain text. */}
    if(url&&['https:','http:'].includes(url.protocol)){
      const link=element('a','',label);link.href=url.href;link.target='_blank';link.rel='noopener noreferrer';row.append(link);
    }else row.append(element('span','',label));
    sources.append(row);
  }
  box.append(sources,history);
  const details=element('details');details.append(element('summary','','Технические сведения'),element('pre','',JSON.stringify({
    record_id:r.block_id,revision_id:r.revision_id,scope:r.scope,authority:r.authority,evidence_refs:r.evidence_refs,
    origin:r.origin,valid_from:r.valid_from,valid_to:r.valid_to,expires_at:r.expires_at,metadata:r.metadata},null,2)));box.append(details);renderSelectionTools();
}
async function editRecord(record=null,blockId='') {
  if(!canWrite()){toast('Этот просмотр открыт только для чтения. Запустите локальную сессию редактора.');return;}
  if(!await allowLeave())return;
  state.selectedTopic='';$('detail-label').textContent='РЕДАКТОР ЗАПИСИ';
  if(!record){state.selected='';state.detail=null;state.detailRequest++;}
  state.draft={isNew:!record,proposal:record&&record.status!=='candidate',dirty:false,memory_id:record?.block_id,expected_revision:record?.revision_id,
    operation_id:`panel:${crypto.randomUUID()}`,conflict:false};
  const box=$('detail-content');box.replaceChildren();setDetailOpen(true);
  box.append(element('h2','',record?'Изменить запись':'Новая запись'));
  if(record&&record.status!=='candidate')box.append(element('p','metadata','Изменения будут отправлены на проверку.'));
  const conflict=element('div','conflict');conflict.id='edit-conflict';conflict.hidden=true;box.append(conflict);
  const form=element('form','editor');
  function field(label,id,tag,value) {
    const l=element('label','',label);l.htmlFor=id;const input=element(tag);input.id=id;input.value=value||'';input.required=true;
    input.addEventListener('input',()=>{state.draft.dirty=true;});form.append(l,input);return input;
  }
  if(!record) {
    const select=field('Тема','edit-block','select','');
    for(const t of flatten(state.snapshot?.tree).filter(n=>n.writable&&!n.meta)){
      const o=element('option','',t.name);o.value=t.block_id;select.append(o);
    }
    if([...select.options].some(option=>option.value===(blockId||state.block)))select.value=blockId||state.block;
    if(!select.options.length){box.append(element('p','conflict','Сначала создайте тему через агента UM-MEM. В пространстве пока нет доступной для записи темы.'));return;}
  }
  field('Название','edit-title','input',record?.title).maxLength=500;
  field('Содержимое','edit-content','textarea',record?.content).maxLength=100000;
  if(record)field('Почему меняем','edit-reason','input','Уточнение пользователем в панели');
  const actions=element('div','actions'),save=element('button','primary','Сохранить');save.type='submit';save.id='save-record';
  actions.append(save,button('Отмена','secondary',cancelEdit));form.append(actions);
  form.addEventListener('submit',async event=>{
    event.preventDefault();const draft=state.draft;if(state.stopped||draft.conflict||draft.saving)return;
    const arguments_={title:$('edit-title').value,content:$('edit-content').value,operation_id:draft.operation_id};
    if(draft.isNew)arguments_.requested_block_id=$('edit-block').value;
    else Object.assign(arguments_,{memory_id:draft.memory_id,expected_revision:draft.expected_revision,reason:$('edit-reason').value});
    $('editor-error')?.remove();draft.saving=true;form.setAttribute('aria-busy','true');save.textContent='Сохраняем…';for(const control of form.elements)control.disabled=true;renderSelectionTools();
    try {
      const result=await api('api/call',{tool:draft.isNew?'remember':draft.proposal?'correct':'amend',arguments:arguments_});
      state.draft=null;state.selected='';
      toast(result.pending_host_approval?'Предложение сохранено для проверки':'Изменение сохранено в общей памяти');await refresh();
      const id=result.memory_id||result.new_id||result.block_id||result.canonical_reference;if(id)await openRecord(id);
    }catch(error){if(error.status===409||error.data?.reason_code==='canonical_revision_conflict')showConflict();else saveError(error);}
    finally{draft.saving=false;form.setAttribute('aria-busy','false');save.textContent='Сохранить';for(const control of form.elements)control.disabled=false;save.disabled=state.stopped||Boolean(state.draft?.conflict);renderSelectionTools();}
  });box.append(form);$('edit-title').focus({preventScroll:true});
}
function showConflict() {
  if(!state.draft)return;state.draft.conflict=true;
  const box=$('edit-conflict');if(!box)return;box.hidden=false;box.replaceChildren(element('span','','Запись уже изменена другим клиентом. Ваш текст остаётся в редакторе. Сравните изменения, прежде чем сохранять.'));
  const head=state.detail?.history.find(v=>v.is_current);
  if(head)box.append(button('Сравнить с текущей версией','',async()=>{
    const draft=state.draft;try{const latest=await api(`api/record?id=${encodeURIComponent(head.record_id)}`);
      if(state.draft!==draft)return;
      const comparison=element('details');comparison.open=true;comparison.append(element('summary','','Текущий текст в памяти'),element('pre','',latest.record.content));box.append(comparison);
      if(!latest.is_current){toast('Версия снова изменилась. Обновляем сведения.');await checkSelected();return;}
      box.append(button('Продолжить с этой версией, сохранив мой черновик','',()=>{
        if(state.draft!==draft||draft.saving)return;
        draft.memory_id=latest.record.block_id;draft.expected_revision=latest.record.revision_id;
        draft.proposal=latest.record.status!=='candidate';draft.operation_id=`panel:${crypto.randomUUID()}`;draft.conflict=false;
        state.selected=latest.record.block_id;state.detail=latest;box.hidden=true;$('save-record').disabled=false;
      }));
    }catch(error){toast(error.message);}
  }));
  $('save-record').disabled=true;
}
async function cancelEdit(){
  if(!await allowLeave())return;const draft=state.draft;state.draft=null;
  if(draft?.type==='block')renderBlockDetail(draft.block_id);else if(state.detail)renderDetail(state.detail);else setDetailOpen(false);
  renderSelectionTools();
}
function saveError(error){
  let message=$('editor-error');if(!message){message=element('p','conflict');message.id='editor-error';message.setAttribute('role','alert');$('detail-content').insertBefore(message,$('detail-content').querySelector('form'));}
  message.textContent=error.message;
}
async function editBlock(id){
  const block=topic(id);if(!canWrite()||!block||block.protected||block.meta||!block.revision||block.status==='archived')return;
  if(!await allowLeave())return;state.detailRequest++;state.selected='';state.detail=null;state.selectedTopic=id;
  const draft=state.draft={type:'block',block_id:id,expected_revision:block.revision,dirty:false,conflict:false};
  $('detail-label').textContent='РЕДАКТОР БЛОКА';
  const box=$('detail-content');box.replaceChildren(element('h2','','Изменить блок'));
  const conflict=element('div','conflict');conflict.id='edit-conflict';conflict.hidden=true;conflict.setAttribute('role','status');box.append(conflict);
  const form=element('form','editor');
  function field(label,id,tag,value,limit){const l=element('label','',label),input=element(tag);l.htmlFor=id;input.id=id;input.value=value||'';input.required=true;input.maxLength=limit;input.addEventListener('input',()=>{draft.dirty=true;});form.append(l,input);}
  field('Название','edit-block-name','input',block.name,500);
  field('Описание','edit-block-description','textarea',block.description||block.synopsis,10000);
  field('Почему меняем','edit-reason','input','Уточнение пользователем в панели',1000);
  const actions=element('div','actions'),save=element('button','primary','Сохранить');save.type='submit';save.id='save-block';
  actions.append(save,button('Отмена','secondary',cancelEdit));form.append(actions);box.append(form);setDetailOpen(true);
  form.addEventListener('submit',async event=>{
    event.preventDefault();if(state.stopped||draft.conflict||draft.saving)return;
    const arguments_={block_id:id,expected_revision:draft.expected_revision,name:$('edit-block-name').value,synopsis:$('edit-block-description').value,reason:$('edit-reason').value};
    $('editor-error')?.remove();draft.saving=true;form.setAttribute('aria-busy','true');save.textContent='Сохраняем…';for(const control of form.elements)control.disabled=true;renderSelectionTools();
    try{await api('api/call',{tool:'amend_block',arguments:arguments_});state.draft=null;await refresh();renderBlockDetail(id);toast('Блок сохранён');}
    catch(error){if(error.status===409||error.data?.reason_code==='canonical_revision_conflict')showBlockConflict();else saveError(error);}
    finally{draft.saving=false;form.setAttribute('aria-busy','false');save.textContent='Сохранить';for(const control of form.elements)control.disabled=false;save.disabled=state.stopped||Boolean(state.draft?.conflict);renderSelectionTools();}
  });$('edit-block-name').focus({preventScroll:true});
}
function showBlockConflict(){
  const draft=state.draft;if(draft?.type!=='block')return;
  draft.conflict=true;const box=$('edit-conflict');if(!box)return;box.hidden=false;
  box.replaceChildren(element('span','','Блок уже изменён. Ваш черновик сохранён. Сравните его с текущей версией.'));
  box.append(button('Сравнить с текущей версией','',async()=>{
    try{
      const snapshot=await api('api/snapshot');if(state.draft!==draft)return;
      const latest=flatten(snapshot.tree).find(b=>b.block_id===draft.block_id);
      if(!latest||latest.protected||latest.status==='archived'){toast('Этот блок больше недоступен для редактирования. Скопируйте черновик.');return;}
      const comparison=element('details');comparison.open=true;comparison.append(element('summary','','Текущая версия'),element('strong','',latest.name),element('p','record-body',latest.description||latest.synopsis));
      box.replaceChildren(comparison,button('Продолжить с этой версией, сохранив мой черновик','',()=>{
        if(state.draft!==draft||draft.saving)return;draft.expected_revision=latest.revision;draft.conflict=false;box.hidden=true;$('save-block').disabled=false;
      }));
    }catch(error){toast(error.message);}
  }));$('save-block').disabled=true;
}

// Coordinates are a view preference; topology always comes from the canonical catalog.
const ns='http://www.w3.org/2000/svg';
const palette=['#b49aff','#72cebb','#ffbe7c','#83afff','#f395b4','#c4d97b','#73d4e0'];
const branchStyles=[[/research|исследован/i,'#b49aff','spark'],[/finding|вывод|результат/i,'#ffbe7c','bulb'],[/source|источник/i,'#73d4e0','book'],[/session|сесси/i,'#83afff','activity'],[/decision|решени/i,'#f395b4','diamond'],[/plan|план/i,'#e7ce80','list'],[/knowledge|знани/i,'#72cebb','book'],[/execution|исполн/i,'#f6a28e','arrow']];
const iconPaths={
  atlas:'M12 3v18M3 12h18M5.6 5.6l12.8 12.8M5.6 18.4 18.4 5.6',
  map:'M8 7l8-3 5 3v13l-5-3-8 3-5-3V4l5 3Zm0 0v13m8-16v13',
  layers:'m12 3 9 5-9 5-9-5 9-5Zm-9 9 9 5 9-5M3 16l9 5 9-5',
  activity:'M3 12h4l3-8 4 16 3-8h4',
  search:'M16 16l5 5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0',
  locate:'M12 2v3m0 14v3M2 12h3m14 0h3M19 12a7 7 0 1 1-14 0 7 7 0 0 1 14 0ZM14 12a2 2 0 1 1-4 0 2 2 0 0 1 4 0Z',
  filter:'M4 7h16M4 17h16M8 4v6m8 4v6',
  list:'M9 5h12M9 12h12M9 19h12M3 5h1m-1 7h1m-1 7h1',
  info:'M12 11v6m0-10h.01M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0',
  plus:'M12 5v14M5 12h14',minus:'M5 12h14',close:'m6 6 12 12M6 18 18 6',
  edit:'m15 4 5 5M4 20l5-1L21 7l-5-5L4 14v6Z',
  menu:'M4 6h16M4 12h16M4 18h16',
  book:'M12 6c-3-3-7-3-10-2v15c4-1 7 0 10 2m0-15c3-3 7-3 10-2v15c-4-1-7 0-10 2V6',
  spark:'m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5L12 3Z',
  bulb:'M9 18h6m-5 3h4M8 15c-5-4-2-12 4-12s9 8 4 12l-1 3H9l-1-3Z',
  diamond:'m12 3 9 9-9 9-9-9 9-9Z',arrow:'M5 12h14m-6-6 6 6-6 6',
  expand:'M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5',
  arrange:'M4 3h6v6H4V3Zm10 12h6v6h-6v-6ZM7 9v9h7M14 3h6v6h-6V3Z',
  keyboard:'M3 5h18v14H3V5Zm3 4h.01M10 9h.01M14 9h.01M18 9h.01M6 12h.01M10 12h.01M14 12h.01M18 12h.01M7 16h10',
  file:'M14 2H5v20h14V7l-5-5Zm0 0v5h5M8 12h8m-8 4h6'
};
function icon(name){const node=svg('svg',{class:'ui-icon',viewBox:'0 0 24 24',fill:'none',stroke:'currentColor','stroke-width':1.6,'stroke-linecap':'round','stroke-linejoin':'round','aria-hidden':'true'});node.append(svg('path',{d:iconPaths[name]||iconPaths.file}));return node;}
function recordIcon(kind){return {source_card:'book',finding:'bulb',procedure:'arrow',rule:'diamond',session_checkpoint:'activity'}[kind]||'file';}
function blockStyle(id){const name=topic(id)?.name||'';return branchStyles.find(([match])=>match.test(name));}
function blockRecordCount(block){return Number(block.canonical_record_count||0)+(block.children||[]).reduce((total,child)=>total+blockRecordCount(child),0);}
function colorFor(id) {const style=blockStyle(id);if(style)return style[1];let hash=0;for(const c of id||'')hash=(hash*31+c.charCodeAt(0))>>>0;return palette[hash%palette.length];}
function svg(tag,attributes={},text=''){const node=document.createElementNS(ns,tag);for(const [k,v]of Object.entries(attributes))node.setAttribute(k,v);node.textContent=text;return node;}
function layoutKey(){return 'um-mem-panel-refine:3:'+(state.snapshot?.read_only&&state.snapshot.workspace_key?state.snapshot.workspace_key:state.snapshot?.workspace+':'+state.snapshot?.profile)+(state.snapshot?.read_only?(state.snapshot.include_archived?':archive':':active'):'');}
function storeLayout(){if(!state.snapshot)return;try{localStorage.setItem(layoutKey(),JSON.stringify({positions:state.positions,manualNodes:[...state.manualNodes],expanded:[...state.expanded],offset:state.graphOffset,transform:state.transform,block:state.block,treeExpanded:[...state.treeExpanded],descendants:state.descendants,...panelPreferences(state)}));}catch{/* Presentation preferences are optional. */}}
function restoreLayout(){
  state.positions={};state.manualNodes=new Set();state.transform={x:0,y:0,k:1};state.graphOffset={x:0,y:0};state.fitNeeded=true;
  const roots=state.snapshot?.tree||[];state.expanded=new Set(roots.length>1?['__overview']:roots.map(n=>n.block_id));
  try{const saved=JSON.parse(localStorage.getItem(layoutKey())||'null');if(saved){
    const finitePoint=p=>Number.isFinite(p?.x)&&Number.isFinite(p?.y);
    state.positions=Object.fromEntries(Object.entries(saved.positions||{}).filter(([,p])=>finitePoint(p)));
    state.manualNodes=new Set(saved.manualNodes||[]);state.expanded=new Set(saved.expanded||[...state.expanded]);
    if(finitePoint(saved.offset))state.graphOffset=saved.offset;
    if(topic(saved.block))state.block=saved.block;
    if(Array.isArray(saved.treeExpanded))state.treeExpanded=new Set(saved.treeExpanded);
    Object.assign(state,panelPreferences(saved));state.graphFocus=false;
    $('block-query').value=state.blockQuery;
    if(finitePoint(saved.transform)&&saved.transform.k>=.7&&saved.transform.k<=4){state.transform=saved.transform;state.fitNeeded=false;}
  }}catch{/* Ignore unavailable or invalid local preferences. */}
  state.focusExpanded=new Set(state.block?[state.block]:[]);revealTreePath(state.block);
  if(state.block)state.fitNeeded=true;state.graphReady=true;
}
function renderLocation(){
  if(state.view!=='memory')return;
  const current=topic(state.block), path=[],seen=new Set();let node=current;
  while(node&&!seen.has(node.block_id)){seen.add(node.block_id);path.unshift(node);node=topic(node.parent_id);}
  const crumbs=$('breadcrumbs');crumbs.replaceChildren();crumbs.hidden=!current?.parent_id;
  if(current?.parent_id){
    crumbs.append(button('← Вся память','',()=>selectBlock('')));
    for(const part of path.slice(0,-1).filter(part=>part.parent_id))crumbs.append(element('span','','/'),button(part.name,'',()=>selectBlock(part.block_id)));
  }
  $('page-title').textContent=current?.name||'Карта знаний';
  $('page-title').title=current?.description||current?.synopsis||'';
  document.querySelector('main').style.setProperty('--scope-color',current?colorFor(current.block_id):'#b49aff');
}
async function openBlock(id){
  if(!await allowLeave())return;state.detailRequest++;state.draft=null;state.detail=null;state.selected='';state.selectedTopic=id;
  renderBlockDetail(id);for(const node of $('graph-scene').querySelectorAll('.graph-node'))node.classList.toggle('selected',node.dataset.id===id);updateGraphGeometry();renderMinimap();
  void loadBranch(id).then(()=>{if(state.selectedTopic===id)renderBlockDetail(id);}).catch(error=>toast(error.message));
}
function renderBlockDetail(id){
  if(state.draft)return;
  const b=topic(id);if(!b){state.selectedTopic='';setDetailOpen(false);return;}
  setDetailOpen(true);$('detail-label').textContent='БЛОК ПАМЯТИ';
  $('detail').style.setProperty('--node-color',colorFor(id));
  const box=$('detail-content');box.replaceChildren();
  const symbol=element('div','block-symbol');symbol.append(icon(blockStyle(id)?.[2]||'layers'));symbol.style.setProperty('--node-color',colorFor(id));box.append(symbol,element('h2','',b.name));
  const tags=element('div','tags');for(const tag of b.tags||[])tags.append(element('span','',tag));box.append(tags);
  if(b.description||b.synopsis)box.append(element('p','record-body',b.description||b.synopsis));
  if(canWrite()&&!b.protected&&!b.meta&&b.revision&&b.status!=='archived'){const edit=button('Редактировать блок','text-button detail-edit',()=>editBlock(id));edit.prepend(icon('edit'));box.append(edit);}
  if(b.children?.length){box.append(element('h3','','Внутри блока'));for(const child of b.children){
    const item=button('','block-child',()=>selectBlock(child.block_id));item.style.setProperty('--node-color',colorFor(child.block_id));
    item.append(element('span','legend-dot'),element('span','',child.name),element('small','',String(blockRecordCount(child))));box.append(item);
  }}
  const records=sortRecords(filterRecords(state.branchRecords.get(id)?.records||[],recordFilters()),state.recordSort);
  if(records.length){
    box.append(element('h3','',`Записи в блоке · ${records.length}${hasRecordFilters()?' по фильтрам':''}`));
    for(const record of records.slice(0,6)){
      const item=button('','block-record',()=>openRecord(record.record_id));
      item.append(element('strong','',record.title||'Без названия'),element('small','',`${kindNames[record.kind]||record.kind} · ${date(record.recorded_at)}`));box.append(item);
    }
    if(records.length>6)box.append(button('Все записи блока →','block-child',async()=>{await selectBlock(id);setMode('list');}));
  }
  const related=(state.snapshot.edges||[]).filter(e=>e.src_id===id||e.dst_id===id);
  if(related.length){box.append(element('h3','','Связанные блоки'));for(const edge of related){const target=topic(edge.src_id===id?edge.dst_id:edge.src_id);if(target)box.append(button(target.name+' ↗','block-child',()=>openBlock(target.block_id)));}}
  if(b.parent_id)box.append(button('← '+(topic(b.parent_id)?.name||'Родительский блок'),'block-child',()=>selectBlock(b.parent_id)));
  renderSelectionTools();
}
let graphNodes=[],graphLinks=[],motionNodes=[];
function counted(value,forms){const n=value%100;return value+' '+forms[n>=11&&n<=14?2:value%10===1?0:value%10>=2&&value%10<=4?1:2];}
async function loadBranch(id,{more=false,force=false}={}){
  const cached=state.branchRecords.get(id);
  if(topic(id)?.canonical_record_count===0){const empty={records:[],visible:0,cursor:''};state.branchRecords.set(id,empty);return empty;}
  if(cached&&!more&&!force)return cached;
  if(more&&cached&&cached.visible<cached.records.length){cached.visible=Math.min(cached.records.length,cached.visible+6);return cached;}
  if(more&&cached&&!cached.cursor)return cached;
  state.graphBusy.add(id);
  try{
    const pager=createBranchPager([topic(id)],(block,cursor)=>api(recordsUrl(block,cursor)));
    const records=await readAllRecords(pager);
    const result={records,cursor:'',visible:Math.min(records.length,cached?.visible||6)};
    state.branchRecords.set(id,result);return result;
  }finally{state.graphBusy.delete(id);}
}
async function toggleGraphBlock(id){
  if(!graphNodes.find(n=>n.id===id)?.expandable||state.graphBusy.has(id))return;
  const block=state.block,focused=state.graphFocus;
  const expanded=state.graphFocus&&state.block?state.focusExpanded:state.expanded;
  const wasExpanded=graphNodes.find(n=>n.id===id)?.expanded;
  if(!wasExpanded){
    const node=$('graph-scene').querySelector(`[data-id="${CSS.escape(id)}"]`);
    node?.setAttribute('aria-busy','true');
    try{if(id!=='__overview')await loadBranch(id);}catch(error){toast(error.message);return;}
    finally{node?.setAttribute('aria-busy','false');}
    if(block!==state.block||focused!==state.graphFocus||expanded!==(state.graphFocus&&state.block?state.focusExpanded:state.expanded)||!graphNodes.some(n=>n.id===id))return;
  }
  if(wasExpanded)expanded.delete(id);else{expanded.add(id);state.contextExpanded.add(id);}
  // A disclosure changes the layout, never the user's camera or open reader.
  cancelAnimationFrame(cameraMotion);state.fitNeeded=false;
  state.layoutAnchor=state.drawPositions[id]?{id,...state.drawPositions[id]}:null;
  renderGraph();storeLayout();
}
async function moreGraphRecords(id){
  if(state.graphBusy.has(id))return;
  try{await loadBranch(id,{more:true});state.layoutAnchor=state.drawPositions[id]?{id,...state.drawPositions[id]}:null;renderGraph();storeLayout();}
  catch(error){toast(error.message);}
}
function labelLines(title,max=24,maxLines=3){
  const words=title.split(/\s+/),lines=[''];
  for(const word of words){let i=lines.length-1;
    if(lines[i].length+word.length+(lines[i]?1:0)>max&&lines[i]){
      if(lines.length===maxLines){lines[i]=lines[i].slice(0,max-1)+'…';break;}
      lines.push('');i++;
    }
    lines[i]+=(lines[i]?' ':'')+word;
  }
  return lines.map(line=>line.length>max?line.slice(0,max-1)+'…':line);
}
function sizeGraphNode(n){
  n.lines=labelLines(n.title,n.type==='record'?28:19,2);
  const width=n.type==='more'?170:n.type==='record'?224:200,height=n.type==='more'?44:n.type==='record'?88:72;
  n.bounds={left:-width/2,right:width/2,top:-height/2,bottom:height/2};
  return n;
}
function graphModel(){
  const links=[],seen=new Set();
  const nonempty=filterBlocks(state.snapshot.tree,{hideEmpty:state.hideEmpty}).visible;
  const context=new Set(scopeBlocks(state.snapshot.tree,state.block).map(b=>b.block_id));let ancestor=topic(state.block);
  while(ancestor?.parent_id&&!context.has(ancestor.parent_id)){context.add(ancestor.parent_id);ancestor=topic(ancestor.parent_id);}
  function branch(b,parent='',color=colorFor(b.block_id),depth=0){
    if(seen.has(b.block_id))return null;seen.add(b.block_id);
    const cached=state.branchRecords.get(b.block_id),expanded=((state.graphFocus&&state.block?state.focusExpanded:state.expanded).has(b.block_id)&&
      (!state.block||state.graphFocus||state.graphOverview||context.has(b.block_id)||state.contextExpanded.has(b.block_id)||b.block_id==='__overview'))||(depth<=2&&!state.block)||b.relation_count>0;
    const children=(b.children||[]).filter(child=>nonempty.has(child.block_id));
    const recordsAllowed=!state.block||state.descendants||b.block_id===state.block;
    const records=recordsAllowed?cached?.records||[]:[];
    const matches=sortRecords(filterRecords(records,recordFilters()),state.recordSort);
    const expandable=children.length>0||recordsAllowed&&(cached?matches.length>0:Number(b.canonical_record_count)>0);
    const childCount=children.length===(b.children||[]).length?counted(children.length,['блок','блока','блоков']):`${children.length} из ${b.children.length} блоков`;
    const n=sizeGraphNode({id:b.block_id,parent,title:b.name,type:'block',central:!parent,color,r:parent?27:37,archived:b.status==='archived',recordCount:blockRecordCount(b),
      expandable,expanded:expanded&&expandable,busy:state.graphBusy.has(b.block_id),subtitle:state.graphBusy.has(b.block_id)?'Загрузка…':
        (b.status==='archived'?'Архив · ':'')+(b.children?.length?childCount+' · ':'')+counted(blockRecordCount(b),['запись','записи','записей'])+(b.relation_count?' · '+b.relation_count+' связ.':'')+(b.label_share?', '+Math.round(b.label_share*100)+'% меток':''),children:[]});
    if(expanded){
      for(const child of children){
        const next=branch(child,n.id,colorFor(child.block_id),depth+1);
        if(next){n.children.push(next);links.push({source:n.id,target:next.id,type:'tree',color:next.color});}
      }
      for(const r of matches.slice(0,cached?.visible||6)){
        const next=sizeGraphNode({id:r.record_id,parent:n.id,recordId:r.record_id,title:r.title||'Без названия',type:'record',color,r:9,kind:r.kind,status:r.status,recordedAt:r.recorded_at,children:[]});
        n.children.push(next);links.push({source:n.id,target:next.id,type:'membership',color});
      }
      if(cached&&cached.visible<matches.length){
        const next=sizeGraphNode({id:'more:'+n.id,parent:n.id,blockId:n.id,title:'Ещё записи',type:'more',color,r:13,
          subtitle:`${cached.visible} из ${matches.length}`,children:[]});
        n.children.push(next);links.push({source:n.id,target:next.id,type:'membership',color});
      }
    }
    return n;
  }
  const roots=state.graphFocus&&topic(state.block)?[topic(state.block)]:(state.snapshot.tree||[]).filter(b=>nonempty.has(b.block_id)&&blockRecordCount(b)>0);
  let root=roots.length===1?branch(roots[0]):roots.length?branch({block_id:'__overview',name:'Вся память',children:roots}):null;
  const nodes=layoutHierarchy(root,22,48);
  const visible=new Set(nodes.map(n=>n.id));
  for(const edge of state.snapshot.edges||[]){if(visible.has(edge.src_id)&&visible.has(edge.dst_id))links.push({source:edge.src_id,target:edge.dst_id,type:'related',label:edge.relation||edge.edge_type||'Связь блоков'});}
  return {nodes,links};
}
function activateGraphNode(n){
  if(n.type==='record')void openRecord(n.recordId);
  else if(n.type==='more')void moreGraphRecords(n.blockId);
  else if(n.id==='__overview')void selectBlock('');else openBlock(n.id);
}
function renderGraph(){
  if(!state.snapshot||!state.graphReady||state.mode!=='graph')return;
  $('graph-scope-caption').textContent=(state.graphFocus&&state.block?`Только ветка · ${topic(state.block)?.name||''}`:state.block?'Блок в общем графе':'Обзор памяти')+(state.hideEmpty?' · Пустые ветки скрыты':'');
  $('graph-focus').setAttribute('aria-pressed',String(state.graphFocus));
  $('graph-focus').disabled=!state.block;
  const viewport=$('graph').getBoundingClientRect();if(viewport.width&&viewport.height){
    if(state.viewport.width){state.transform.x+=(viewport.width-state.viewport.width)/2;state.transform.y+=(viewport.height-state.viewport.height)/2;}
    state.viewport={width:viewport.width,height:viewport.height};$('graph').setAttribute('viewBox','0 0 '+viewport.width+' '+viewport.height);
  }
  const focused=document.activeElement?.closest('.graph-node')?.dataset.id;
  cancelAnimationFrame(graphMotion);
  const previous=state.drawPositions,scene=$('graph-scene'),oldNodes=[...scene.querySelectorAll('.graph-node,.graph-exit')];
  const oldLinks=[...scene.querySelectorAll('.graph-link')],before=motionNodes.length?motionNodes:graphNodes;
  scene.replaceChildren();const model=graphModel();graphNodes=model.nodes;graphLinks=model.links;
  $('graph-scope-caption').textContent=`${state.graphFocus?'ФОКУС НА ВЕТКЕ':state.block?'ВЕТКА В КОНТЕКСТЕ':'КАРТА ПАМЯТИ'} · ${counted(graphNodes.filter(n=>n.type==='block').length,['блок','блока','блоков'])} · ${counted(graphNodes.filter(n=>n.type==='record').length,['запись','записи','записей'])}${hasRecordFilters()?' по фильтрам':''}`;
  $('graph-empty').hidden=Boolean(graphNodes.length);$('graph-empty').textContent=state.snapshot.read_only&&!state.snapshot.workspaces?.length?'Пространства памяти UM-MEM не найдены. Проверьте настройки хранилища.':state.hideEmpty?'Нет веток с записями. Отключите скрытие пустых веток.':'Память пока пуста. Создайте первый блок через агента UM-MEM.';
  const anchor=state.layoutAnchor,anchorId=anchor?.id;
  if(state.layoutAnchor){const target=graphNodes.find(n=>n.id===anchorId);
    if(target)state.graphOffset={x:state.layoutAnchor.x-target.x,y:state.layoutAnchor.y-target.y};state.layoutAnchor=null;}
  for(const n of graphNodes)if(!state.manualNodes.has(n.id)||!state.positions[n.id])state.positions[n.id]={x:n.x+state.graphOffset.x,y:n.y+state.graphOffset.y};
  if(anchor&&graphNodes.some(n=>n.id===anchorId))state.positions[anchorId]={x:anchor.x,y:anchor.y};
  separateOverlaps([...graphNodes].sort((a,b)=>Number(state.manualNodes.has(b.id))-Number(state.manualNodes.has(a.id))),state.positions,anchorId);
  for(const link of graphLinks){const path=svg('path',{class:'graph-link '+link.type,'data-source':link.source,'data-target':link.target,'data-type':link.type});path.style.setProperty('--node-color',link.color||'#b3a2e6');if(link.label)path.append(svg('title',{},link.label));scene.append(path);}
  for(const n of graphNodes){
    const selected=n.type==='record'?state.selected===n.recordId:(state.selectedTopic||state.block)===n.id;
    const g=svg('g',{class:'graph-node '+n.type+(n.archived?' archived':'')+(selected?' selected':''),'data-id':n.id,'data-parent':n.parent||'','data-depth':n.depth,'data-type':n.type,tabindex:'0',role:'button','aria-label':n.title});
    if(n.type==='block'&&n.expandable){g.setAttribute('aria-expanded',String(n.expanded));g.setAttribute('aria-busy',String(n.busy));}
    g.style.setProperty('--node-color',n.color);g.append(svg('title',{},[n.title,n.subtitle,n.type==='block'?(n.expandable?'Сведения · Пробел: раскрыть / свернуть':'Сведения'):''].filter(Boolean).join(' · ')));
    if(n.subtitle)g.setAttribute('aria-description',n.subtitle);
    const b=n.bounds;
    g.append(svg('rect',{class:'node-surface',x:b.left,y:b.top,width:b.right-b.left,height:b.bottom-b.top,rx:8}));
    if(n.type==='block'){
      n.lines.forEach((line,i)=>g.append(svg('text',{class:'node-title',x:-82,y:-8+i*17},line)));
      g.append(svg('text',{class:'node-caption',x:-82,y:24},(n.archived?'Архив · ':'')+counted(n.recordCount,['запись','записи','записей'])));
      if(n.expandable)g.append(svg('circle',{class:'node-toggle','data-toggle':n.id,cx:83,cy:21,r:10}),svg('text',{class:'node-toggle-label',x:83,y:21},n.busy?'…':n.expanded?'−':'+'));
    }else if(n.type==='record'){
      g.setAttribute('data-status',n.status||'');
      g.append(svg('text',{class:'node-kind',x:-94,y:-26},kindNames[n.kind]||n.kind||'Запись'),svg('circle',{class:'node-status',cx:94,cy:-29,r:2.5}));
      n.lines.forEach((line,i)=>g.append(svg('text',{class:'node-title',x:-94,y:-3+i*17},line)));
      g.append(svg('text',{class:'node-date',x:-94,y:33},date(n.recordedAt)));
      g.setAttribute('aria-description',`${kindNames[n.kind]||n.kind} · ${statusNames[n.status]||n.status} · ${date(n.recordedAt)}`);
      g.querySelector('title').textContent=`${n.title} · ${kindNames[n.kind]||n.kind} · ${statusNames[n.status]||n.status} · ${date(n.recordedAt)}`;
    }else{
      g.append(svg('text',{class:'node-title',x:-64,y:5},'+ Ещё записи'),svg('text',{class:'node-caption',x:32,y:5},n.subtitle));
    }
    g.addEventListener('keydown',e=>{
      if(e.key==='Enter'||e.key===' '){e.preventDefault();e.stopPropagation();
        if(e.altKey&&n.type==='block')openBlock(n.id);else if(e.key===' '&&n.type==='block')void toggleGraphBlock(n.id);else activateGraphNode(n);
      }
      if(['ArrowLeft','ArrowRight','ArrowUp','ArrowDown','Home'].includes(e.key)){e.preventDefault();e.stopPropagation();navigateGraph(n,e.key);}
      if(e.key==='+'||e.key==='-')e.stopPropagation();
    });scene.append(g);if(focused===n.id)g.focus({preventScroll:true});
  }
  animateGraph(previous,before,oldNodes,oldLinks);applyTransform();if(state.fitNeeded){state.fitNeeded=false;requestAnimationFrame(()=>{fitGraph();fitGraph(true);});}
}
let graphMotion=0,cameraMotion=0;
function animateGraph(previous,before,oldNodes,oldLinks){
  if(reducedMotion.matches||!Object.keys(previous).length){motionNodes=graphNodes;updateGraphGeometry();renderMinimap();return;}
  const tracks=graphTransition(before,graphNodes,previous,state.positions),ids=new Set(graphNodes.map(n=>n.id));
  motionNodes=[...graphNodes,...before.filter(n=>!ids.has(n.id)&&tracks[n.id])];
  const scene=$('graph-scene');
  for(const node of oldNodes)if(!ids.has(node.dataset.id)){
    node.classList.add('graph-exit');node.removeAttribute('tabindex');node.setAttribute('aria-hidden','true');$('graph-scene').append(node);
  }
  const linkKey=node=>JSON.stringify([node.dataset.source,node.dataset.target,node.dataset.type]);
  const oldByKey=new Map(oldLinks.map(node=>[linkKey(node),node])),nextKeys=new Set(),linkTracks=[];
  for(const node of scene.querySelectorAll('.graph-link')){
    const key=linkKey(node);nextKeys.add(key);const old=oldByKey.get(key);
    linkTracks.push({node,from:old?Number(old.getAttribute('opacity')??1):0,to:1});
  }
  for(const node of oldLinks)if(!nextKeys.has(linkKey(node))){
    node.classList.add('graph-exit-link');scene.prepend(node);linkTracks.push({node,from:Number(node.getAttribute('opacity')??1),to:0});
  }
  const fromMap=miniProjection,toMap=graphNodes.length?mapProjection(graphBounds()):fromMap;
  const started=performance.now();
  function frame(now){const t=Math.min(1,(now-started)/420),e=t*t*(3-2*t),positions=sampleGraphTransition(tracks,t);
    updateGraphGeometry(positions,motionNodes);
    for(const {node,from,to}of linkTracks)node.setAttribute('opacity',from+(to-from)*e);
    const projection=fromMap&&toMap?Object.fromEntries(['x','y','k'].map(key=>[key,fromMap[key]+(toMap[key]-fromMap[key])*e])):toMap;
    renderMinimap(positions,motionNodes,projection);
    if(t<1)graphMotion=requestAnimationFrame(frame);
    else{
      scene.querySelectorAll('.graph-exit,.graph-exit-link').forEach(node=>node.remove());
      motionNodes=graphNodes;updateGraphGeometry();renderMinimap();graphMotion=0;
    }
  }
  frame(started);
}
function moveCamera(target){
  cancelAnimationFrame(cameraMotion);
  if(reducedMotion.matches){state.transform=target;applyTransform();storeLayout();return;}
  const from={...state.transform},started=performance.now();
  function frame(now){const t=Math.min(1,(now-started)/320),e=1-(1-t)**3;
    state.transform={x:from.x+(target.x-from.x)*e,y:from.y+(target.y-from.y)*e,k:from.k+(target.k-from.k)*e};applyTransform();
    if(t<1)cameraMotion=requestAnimationFrame(frame);else storeLayout();
  }
  cameraMotion=requestAnimationFrame(frame);
}
function activeGraphId(){return state.selected||state.selectedTopic||state.block||graphNodes[0]?.id;}
function graphPath(){const ids=new Set(),byId=new Map(graphNodes.map(n=>[n.id,n]));let n=byId.get(activeGraphId());
  while(n&&!ids.has(n.id)){ids.add(n.id);n=byId.get(n.parent);}return ids;
}
function navigateGraph(n,key){
  const siblings=graphNodes.filter(other=>other.parent===n.parent),index=siblings.indexOf(n);
  let target=key==='Home'?graphNodes[0]:key==='ArrowLeft'?graphNodes.find(other=>other.id===n.parent):
    key==='ArrowUp'?siblings[index-1]:key==='ArrowDown'?siblings[index+1]:graphNodes.find(other=>other.parent===n.id);
  if(key==='ArrowRight'&&n.type==='block'&&!n.expanded){void toggleGraphBlock(n.id);return;}
  if(!target)return;
  if(target.type==='block')void selectBlock(target.id==='__overview'?'':target.id).then(()=>$('graph-scene').querySelector(`[data-id="${CSS.escape(target.id)}"]`)?.focus({preventScroll:true}));
  else{focusGraph(target.id);$('graph-scene').querySelector(`[data-id="${CSS.escape(target.id)}"]`)?.focus({preventScroll:true});}
}
function updateGraphGeometry(positions=state.positions,nodes=graphNodes){
  state.drawPositions=Object.fromEntries(nodes.map(n=>[n.id,{...positions[n.id]}]));
  const byId=new Map(nodes.map(n=>[n.id,n])),pathIds=graphPath();
  for(const node of $('graph-scene').querySelectorAll('.graph-node,.graph-exit')){const p=positions[node.dataset.id];if(!p)continue;node.setAttribute('transform','translate('+p.x+','+p.y+')');node.setAttribute('opacity',p.opacity??1);node.classList.toggle('in-path',pathIds.has(node.dataset.id));}
  for(const path of $('graph-scene').querySelectorAll('.graph-link')){
    const a=positions[path.dataset.source],b=positions[path.dataset.target],source=byId.get(path.dataset.source),target=byId.get(path.dataset.target);if(!a||!b||!source||!target)continue;
    const direction=b.x>=a.x?1:-1,sx=a.x+(direction>0?source.bounds.right:source.bounds.left),tx=b.x+(direction>0?target.bounds.left:target.bounds.right);
    if(path.classList.contains('related')){
      const lane=Math.min(a.y+source.bounds.top,b.y+target.bounds.top)-35;
      path.setAttribute('d',`M ${sx} ${a.y} C ${sx+45*direction} ${lane},${tx-45*direction} ${lane},${tx} ${b.y}`);
    }else{
      const bend=Math.max(32,Math.abs(tx-sx)*.5);path.setAttribute('d',`M ${sx} ${a.y} C ${sx+bend*direction} ${a.y},${tx-bend*direction} ${b.y},${tx} ${b.y}`);
    }
    path.classList.toggle('lit',[path.dataset.source,path.dataset.target].some(id=>id===state.selected||id===state.block||id===state.selectedTopic));
    path.classList.toggle('in-path',pathIds.has(path.dataset.source)&&pathIds.has(path.dataset.target));
  }
}
function applyTransform(){const t=state.transform;$('graph-scene').setAttribute('transform','translate('+t.x+','+t.y+') scale('+t.k+')');$('zoom-level').textContent=Math.round(t.k*100)+'%';
  $('graph-wrap').classList.toggle('is-overview',t.k<.7);updateMinimapViewport();
}
function graphBounds(nodes=graphNodes){
  if(!nodes.length)return null;
  return {left:Math.min(...nodes.map(n=>state.positions[n.id].x+n.bounds.left)),right:Math.max(...nodes.map(n=>state.positions[n.id].x+n.bounds.right)),
    top:Math.min(...nodes.map(n=>state.positions[n.id].y+n.bounds.top)),bottom:Math.max(...nodes.map(n=>state.positions[n.id].y+n.bounds.bottom))};
}
function fitGraph(readable=false){
  if(readable){focusGraph();return;}
  state.graphFocus=false;state.graphOverview=true;renderGraph();
  const bounds=graphBounds();if(!bounds)return;
  moveCamera(fitBounds(bounds,state.viewport,.02,1));
}
function focusGraph(id=activeGraphId(),exact=false){
  const n=graphNodes.find(n=>n.id===id)||graphNodes[0];if(!n)return;
  if(!state.graphFocus&&!exact){
    const compact=state.viewport.width<600;
    const context=graphNodes.filter(other=>other.parent===n.id||other.id===n.id||(!compact&&other.type==='block'&&(other.id===n.parent||other.parent===n.parent)));
    moveCamera(fitBounds(graphBounds(context.length?context:[n]),state.viewport,.08,1));return;
  }
  let bounds=graphBounds(graphNodes.filter(other=>other.id===n.id||other.parent===n.id));
  if(exact||(bounds.right-bounds.left)*.9>state.viewport.width-64||(bounds.bottom-bounds.top)*.9>state.viewport.height-80)bounds=graphBounds([n]);
  moveCamera(fitBounds(bounds,state.viewport,exact?1:.9,1));
}
let miniProjection=null;
function renderMinimap(positions=state.positions,nodes=graphNodes,projection=null){
  const bounds=graphBounds(),scene=$('minimap-scene');scene.replaceChildren();$('minimap-wrap').hidden=!$('show-minimap').checked||nodes.length<8;
  if(!bounds&&!projection)return;miniProjection=projection||mapProjection(bounds);const t=miniProjection,byId=new Map(nodes.map(n=>[n.id,n])),path=graphPath();
  for(const n of nodes){const a=positions[n.parent],b=positions[n.id];if(!a||!b)continue;
    scene.append(svg('path',{class:'minimap-link'+(path.has(n.parent)&&path.has(n.id)?' in-path':''),opacity:`${.5*(b.opacity??1)}`,d:`M ${a.x*t.k+t.x} ${a.y*t.k+t.y} L ${b.x*t.k+t.x} ${b.y*t.k+t.y}`}));
  }
  for(const n of byId.values()){const p=positions[n.id],dot=svg('circle',{class:'minimap-node'+(path.has(n.id)?' in-path':''),'data-map-id':n.id,cx:p.x*t.k+t.x,cy:p.y*t.k+t.y,r:n.type==='block'?2.2:1.3,fill:n.color,opacity:p.opacity??1});dot.append(svg('title',{},n.title));scene.append(dot);}
  updateMinimapViewport();
}
function updateMinimapViewport(){
  if(!miniProjection)return;const t=state.transform,m=miniProjection,rect=$('minimap-viewport');
  for(const [key,value]of Object.entries({x:-t.x/t.k*m.k+m.x,y:-t.y/t.k*m.k+m.y,width:state.viewport.width/t.k*m.k,height:state.viewport.height/t.k*m.k}))rect.setAttribute(key,value);
}
let drag=null;
function point(event){const p=$('graph').createSVGPoint();p.x=event.clientX;p.y=event.clientY;return p.matrixTransform($('graph').getScreenCTM().inverse());}
$('graph').addEventListener('pointerdown',e=>{if(e.button!==0)return;cancelAnimationFrame(cameraMotion);const p=point(e),target=e.target.closest('.graph-node:not(.graph-exit)');drag={start:p,last:p,id:target?.dataset.id,toggle:Boolean(e.target.closest('[data-toggle]')),moved:false};$('graph').setPointerCapture(e.pointerId);if(!target)$('graph').focus({preventScroll:true});});
$('graph').addEventListener('pointermove',e=>{if(!drag)return;const p=point(e),dx=p.x-drag.last.x,dy=p.y-drag.last.y;drag.moved ||=Math.hypot(p.x-drag.start.x,p.y-drag.start.y)>4;
  if(drag.moved){if(drag.id){
    cancelAnimationFrame(graphMotion);graphMotion=0;
    for(const n of graphNodes){const p=state.drawPositions[n.id];if(p)state.positions[n.id]={x:p.x,y:p.y};}
    $('graph-scene').querySelectorAll('.graph-exit,.graph-exit-link').forEach(node=>node.remove());motionNodes=graphNodes;
    $('graph-scene').querySelectorAll('.graph-link').forEach(node=>node.setAttribute('opacity',1));
    state.manualNodes.add(drag.id);state.positions[drag.id].x+=dx/state.transform.k;state.positions[drag.id].y+=dy/state.transform.k;updateGraphGeometry();
  }else{state.transform.x+=dx;state.transform.y+=dy;applyTransform();}}drag.last=p;});
$('graph').addEventListener('pointerup',()=>{if(!drag)return;const d=drag;drag=null;
  if(!d.moved&&d.id){
    const node=graphNodes.find(n=>n.id===d.id);if(node){if(d.toggle&&node.type==='block')void toggleGraphBlock(node.id);else activateGraphNode(node);}
  }else if(d.moved&&d.id){
    separateOverlaps(graphNodes,state.positions,d.id);updateGraphGeometry();renderMinimap();
  }storeLayout();});
$('graph').addEventListener('pointercancel',()=>{drag=null;});
function zoom(factor,p=null,smooth=false){cancelAnimationFrame(cameraMotion);p||={x:state.viewport.width/2,y:state.viewport.height/2};const target=zoomAt(state.transform,state.transform.k*factor,p);if(smooth)moveCamera(target);else{state.transform=target;applyTransform();storeLayout();}}
function pan(x,y){cancelAnimationFrame(cameraMotion);state.transform.x+=x;state.transform.y+=y;applyTransform();storeLayout();}
$('graph').addEventListener('wheel',e=>{e.preventDefault();
  if(e.ctrlKey||e.metaKey)zoom(Math.exp(-e.deltaY*.002),point(e));else pan(e.shiftKey?-e.deltaY:-e.deltaX,e.shiftKey?0:-e.deltaY);
},{passive:false});
$('zoom-in').onclick=()=>zoom(1.2,null,true);$('zoom-out').onclick=()=>zoom(1/1.2,null,true);$('graph-reset').onclick=()=>fitGraph();
$('arrange-graph').onclick=()=>{
  state.manualNodes.clear();const id=state.block||graphNodes[0]?.id;
  state.layoutAnchor=state.drawPositions[id]?{id,...state.drawPositions[id]}:null;
  renderGraph();storeLayout();toast('Расположение блоков восстановлено');
};
$('zoom-level').onclick=()=>focusGraph(activeGraphId(),true);$('graph-focus').onclick=()=>{
  state.graphFocus=true;if(state.block)state.focusExpanded.add(state.block);renderGraph();focusGraph();storeLayout();
};
let miniDrag=null;
function miniPoint(e){const rect=$('minimap').getBoundingClientRect();return {x:(e.clientX-rect.left)*160/rect.width,y:(e.clientY-rect.top)*108/rect.height};}
function centerFromMinimap(p){cancelAnimationFrame(cameraMotion);if(!miniProjection)return;const m=miniProjection,t=state.transform;
  t.k=Math.max(.9,t.k);t.x=state.viewport.width/2-(p.x-m.x)/m.k*t.k;t.y=state.viewport.height/2-(p.y-m.y)/m.k*t.k;applyTransform();storeLayout();
}
$('minimap').addEventListener('pointerdown',e=>{if(e.button!==0)return;const p=miniPoint(e);miniDrag={start:p,id:e.target.dataset.mapId,moved:false};$('minimap').setPointerCapture(e.pointerId);});
$('minimap').addEventListener('pointermove',e=>{if(!miniDrag)return;const p=miniPoint(e);miniDrag.moved ||=Math.hypot(p.x-miniDrag.start.x,p.y-miniDrag.start.y)>3;if(miniDrag.moved)centerFromMinimap(p);});
$('minimap').addEventListener('pointerup',e=>{if(!miniDrag)return;const d=miniDrag;miniDrag=null;const n=graphNodes.find(n=>n.id===d.id);
  if(!d.moved&&n)activateGraphNode(n);else centerFromMinimap(miniPoint(e));
});
$('minimap').addEventListener('pointercancel',()=>{miniDrag=null;});
function panKey(e){const offsets={ArrowLeft:[100,0],ArrowRight:[-100,0],ArrowUp:[0,100],ArrowDown:[0,-100]};if(offsets[e.key]){e.preventDefault();pan(...offsets[e.key]);}}
$('minimap').addEventListener('keydown',panKey);$('graph').addEventListener('keydown',panKey);
$('expand-all').onclick=async()=>{
  $('expand-all').disabled=true;
  const selected=state.block,focused=state.graphFocus;
  try{const blocks=scopeBlocks(state.snapshot.tree,focused?selected:''),expanded=focused&&selected?state.focusExpanded:state.expanded;
    await Promise.all(blocks.filter(b=>b.canonical_record_count>0).map(b=>loadBranch(b.block_id)));
    if(selected!==state.block||focused!==state.graphFocus)return;
    for(const block of blocks)expanded.add(block.block_id);if(!focused)state.graphOverview=true;
    cancelAnimationFrame(cameraMotion);state.fitNeeded=false;
    const id=activeGraphId();state.layoutAnchor=state.drawPositions[id]?{id,...state.drawPositions[id]}:null;
    renderGraph();storeLayout();
  }catch(error){toast(error.message);}finally{$('expand-all').disabled=false;}
};
$('collapse-all').onclick=()=>{
  if(state.graphFocus&&state.block)state.focusExpanded.clear();
  else{
    state.expanded=new Set(state.snapshot.tree.length>1?['__overview']:state.snapshot.tree.map(n=>n.block_id));
    let parent=topic(state.block)?.parent_id;const seen=new Set();
    while(parent&&!seen.has(parent)){seen.add(parent);state.expanded.add(parent);parent=topic(parent)?.parent_id;}
  }
  cancelAnimationFrame(cameraMotion);state.fitNeeded=false;
  const id=state.block||graphNodes[0]?.id;state.layoutAnchor=state.drawPositions[id]?{id,...state.drawPositions[id]}:null;
  renderGraph();storeLayout();
};
function setMode(mode){const changed=state.mode!==mode;state.mode=mode;$('graph-wrap').hidden=mode!=='graph';$('list-area').hidden=mode!=='list';$('graph-mode').classList.toggle('selected',mode==='graph');$('list-mode').classList.toggle('selected',mode==='list');renderRecords();if(changed)enterView($(mode==='list'?'list-area':'graph-wrap'));storeLayout();}

// Keep the graph in the space left by the reader rather than underneath it.
let resizeFrame;
new ResizeObserver(()=>{cancelAnimationFrame(resizeFrame);resizeFrame=requestAnimationFrame(()=>{
  if(!state.graphReady||state.mode!=='graph'||state.view!=='memory')return;
  renderGraph();
});}).observe($('graph-wrap'));

function toggleTopics(open){$('sidebar').classList.toggle('is-open',open);$('toggle-topics').setAttribute('aria-expanded',String(open));}
$('toggle-topics').onclick=()=>toggleTopics($('toggle-topics').getAttribute('aria-expanded')!=='true');
document.addEventListener('pointerdown',e=>{
  if(!e.target.closest('#sidebar,#toggle-topics'))toggleTopics(false);
  if(!e.target.closest('#keyboard-help,#help-button')){$('keyboard-help').hidden=true;$('help-button').setAttribute('aria-expanded','false');}
  if(!e.target.closest('#graph-more'))$('graph-more').open=false;
  if(!e.target.closest('#selection-more'))$('selection-more').open=false;
  if(!e.target.closest('#workspace-menu'))$('workspace-menu').open=false;
});

document.querySelectorAll('[data-view]').forEach(b=>b.onclick=()=>setView(b.dataset.view));
$('list-mode').onclick=()=>setMode('list');$('graph-mode').onclick=()=>{
  if(state.query){state.searchRequest++;state.query='';state.records=[];$('query').value='';$('search-info').hidden=true;void refresh();}
  setMode('graph');
};
$('all-topics').onclick=()=>selectBlock('');$('new-record').onclick=()=>editRecord();
$('selection-info').onclick=()=>{if(state.selected)void openRecord(state.selected);else if(selectedBlock())openBlock(selectedBlock().block_id);};
$('selection-edit').onclick=()=>{if(state.selected&&state.detail?.is_current)editRecord(state.detail.record);else if(selectedBlock())editBlock(selectedBlock().block_id);};
$('selection-add').onclick=()=>editRecord(null,selectedBlock()?.block_id||'');
$('selection-focus').onclick=async()=>{
  const b=selectedBlock(),selected=state.selected;if(!b)return;
  try{
    if(state.selected){
      const cached=await loadBranch(b.block_id);if(state.selected!==selected)return;const matches=sortRecords(filterRecords(cached.records,recordFilters()),state.recordSort);
      cached.visible=Math.max(cached.visible,matches.findIndex(r=>r.record_id===state.selected)+1);
      let parent=b;while(parent){state.expanded.add(parent.block_id);state.focusExpanded.add(parent.block_id);state.contextExpanded.add(parent.block_id);parent=topic(parent.parent_id);}
      renderGraph();
    }
    const record=state.selected&&graphNodes.find(n=>n.recordId===state.selected);focusGraph(record?.id||b.block_id);storeLayout();
    if(state.selected&&!record)toast('Показан блок записи. Сама запись скрыта текущими фильтрами или относится к истории.');
  }catch(error){toast(error.message);}
};
$('selection-history').onclick=async()=>{const id=state.selected;$('selection-more').open=false;if(!id)return;await openRecord(id);const history=$('record-history');if(history){history.open=true;history.scrollIntoView({block:'nearest',behavior:reducedMotion.matches?'instant':'smooth'});}};
$('selection-parent').onclick=()=>{const b=selectedBlock();$('selection-more').open=false;if(b?.parent_id)openBlock(b.parent_id);};
$('selection-tools').addEventListener('keydown',event=>{
  if(!['ArrowUp','ArrowDown','Home','End'].includes(event.key))return;
  const controls=[...$('selection-tools').querySelectorAll(':scope > button:not(:disabled),:scope > details:not([hidden]) > summary')];
  const index=controls.indexOf(document.activeElement);if(index<0)return;event.preventDefault();
  controls[event.key==='Home'?0:event.key==='End'?controls.length-1:(index+(event.key==='ArrowUp'?-1:1)+controls.length)%controls.length]?.focus();
});
$('toggle-filters').onclick=()=>{const open=$('record-filters').hidden;$('record-filters').hidden=!open;$('toggle-filters').setAttribute('aria-expanded',String(open));if(open)enterView($('record-filters'));};
$('help-button').onclick=()=>{const open=$('keyboard-help').hidden;$('keyboard-help').hidden=!open;$('help-button').setAttribute('aria-expanded',String(open));};
$('show-minimap').onchange=()=>renderMinimap();
document.querySelector('.graph-menu').addEventListener('click',e=>{if(e.target.closest('button'))$('graph-more').open=false;});
$('close-detail').onclick=async()=>{if(await allowLeave()){state.detailRequest++;state.draft=null;state.selected='';state.selectedTopic='';setDetailOpen(false);renderRecords();if(state.stopped)showStopped();}};
$('discard-dialog').addEventListener('cancel',event=>{event.preventDefault();$('discard-dialog').close('keep');});
$('search-form').onsubmit=e=>{e.preventDefault();void search($('query').value);};
$('query').addEventListener('input',()=>{if(!$('query').value&&state.query){state.query='';state.searchRequest++;$('search-info').hidden=true;void refresh();}});
$('block-query').oninput=()=>{state.blockQuery=$('block-query').value.trim();renderTopics();storeLayout();};
$('clear-block-query').onclick=()=>{state.blockQuery='';$('block-query').value='';renderTopics();storeLayout();$('block-query').focus();};
$('hide-empty').onchange=()=>{
  state.hideEmpty=$('hide-empty').checked;const id=activeGraphId();
  state.layoutAnchor=state.drawPositions[id]?{id,...state.drawPositions[id]}:null;
  renderTopics();renderGraph();storeLayout();
};
async function changeRecordFilters(reset=false){
  anchorCurrentBlock();
  state.recordStatus=reset?'':$('record-status').value;state.recordKind=reset?'':$('record-kind').value;
  state.searchRequest++;state.recordLimit=64;storeLayout();
  // A completed scope can be filtered immediately, including later-page types.
  if(!state.recordsLoading&&state.pager&&!state.pager.hasMore&&!state.recordsError){if(state.query)await search(state.query);else renderRecords();return;}
  state.records=[];state.browseRecords=[];state.cursor='';state.pager=null;
  await loadRecords();if(state.query)await search(state.query);
}
$('record-status').onchange=()=>changeRecordFilters();$('record-kind').onchange=()=>changeRecordFilters();
$('record-sort').onchange=()=>{anchorCurrentBlock();state.recordSort=$('record-sort').value;state.recordLimit=64;renderRecords();storeLayout();};
$('reset-record-filters').onclick=()=>changeRecordFilters(true);
$('load-more').onclick=()=>{if(state.recordLimit<filterRecords(state.records,recordFilters()).length){state.recordLimit+=64;renderRecords();}else void loadRecords({more:true});};
$('record-scope').onchange=async()=>{
  anchorCurrentBlock();
  state.descendants=$('record-scope').value==='branch';state.searchRequest++;state.records=[];state.browseRecords=[];state.cursor='';
  await loadRecords();if(state.query)await search(state.query);storeLayout();
};
$('memory-scope').onchange=()=>{const value=$('memory-scope').value;$('project-scope-form').hidden=value!=='manual';if(value==='workspace')void changeMemoryScope('workspace');else if(value.startsWith('project:'))void changeMemoryScope('project',value.slice(8));};
$('project-scope-form').onsubmit=event=>{event.preventDefault();const id=$('project-scope-id').value;if(!id||id!==id.trim()){toast('Укажите точный ID проекта без пробелов по краям.');return;}void changeMemoryScope('project',id);};
function anchorCurrentBlock(){const id=state.block||graphNodes[0]?.id;state.layoutAnchor=state.drawPositions[id]?{id,...state.drawPositions[id]}:null;}
window.addEventListener('beforeunload',e=>{if(state.draft?.dirty){e.preventDefault();e.returnValue='';}});
document.addEventListener('keydown',e=>{if($('discard-dialog').open)return;if(e.key==='Escape'){
    if($('workspace-menu').open){$('workspace-menu').open=false;$('workspace-trigger').focus({preventScroll:true});e.preventDefault();return;}
    toggleTopics(false);$('graph-more').open=false;$('selection-more').open=false;$('keyboard-help').hidden=true;$('help-button').setAttribute('aria-expanded','false');if(!$('detail').hidden)$('close-detail').click();}
  if(e.target.closest('input,textarea,select'))return;
  if(e.ctrlKey||e.metaKey||e.altKey)return;
  if(e.key==='/'){e.preventDefault();$('query').focus();}
  if(state.mode==='graph'&&state.view==='memory'){
    if(e.key==='0'){e.preventDefault();focusGraph(activeGraphId(),true);}
    if(e.key.toLowerCase()==='f'){e.preventDefault();$('graph-focus').click();}
    if(e.key==='+'||e.key==='='){e.preventDefault();zoom(1.2);}
    if(e.key==='-'){e.preventDefault();zoom(1/1.2);}
  }
  if(e.key==='Backspace'&&state.block){e.preventDefault();void selectBlock(topic(state.block)?.parent_id||'');}
});
function showStopped(){state.stopped=true;state.source?.close();connected(false);$('connection').textContent='Панель остановлена';
  if(state.draft){$('stopped').hidden=true;banner('Панель остановлена. Черновик сохранён в этом редакторе — скопируйте его перед закрытием вкладки. Повторный запуск: um-memory dashboard.');
    if($('save-record'))$('save-record').disabled=true;if($('save-block'))$('save-block').disabled=true;
  }else{$('stopped').hidden=false;banner();}
  $('new-record').disabled=true;$('stop-panel').disabled=true;renderSelectionTools();
}
$('stop-panel').onclick=async()=>{if(state.draft?.saving){toast('Дождитесь результата сохранения.');return;}$('stop-panel').disabled=true;
  try{await api('api/stop',{});showStopped();}catch(error){$('stop-panel').disabled=false;toast(error.message);}};

function treeHasBranches(snapshot){return (snapshot?.tree||[]).some(node=>(node.children||[]).length>0);}
// Records are filed into logical blocks, which are not nodes of the block tree.
// A store full of records can therefore render as an empty graph on first open.
function preferredMode(){if(!state.snapshot)return state.mode;const count=(state.snapshot.records||[]).length;if(count>0&&!treeHasBranches(state.snapshot))return 'list';return state.mode;}

async function start() {
  try {
    const token=location.hash.slice(1);if(token){history.replaceState(null,'',location.pathname+location.search);await api('auth',{token});}
    await refresh();if(!state.snapshot)throw new Error('Не удалось прочитать память. Откройте панель командой um-memory dashboard.');
    renderSummary();setMode(preferredMode());if(state.mode==='graph'&&state.block)focusGraph();
    if(state.snapshot.read_only){connected(true);return;}
    state.source=new EventSource(`/panel/api/events?since=${encodeURIComponent(state.snapshot.cursor)}`);
    state.source.onopen=()=>{connected(true);banner();};state.source.onerror=()=>{if(state.stopped)return;connected(false);banner('Связь потеряна. Черновик остаётся в редакторе. Если панель была остановлена, откройте её снова: um-memory dashboard.');};
    state.source.addEventListener('stopped',()=>showStopped());
    state.source.addEventListener('reset',()=>void refresh());
    let refreshTimer;
    state.source.addEventListener('change',e=>{const event=JSON.parse(e.data);if(!state.events.some(x=>x.id===event.id)){state.events.push(event);state.events=state.events.slice(-256);renderEvents();}
      if(event.kind!=='retrieval'){clearTimeout(refreshTimer);refreshTimer=setTimeout(()=>void refresh(),120);}
    });
  }catch(error){connected(false);banner(error.message);}
}
for(const node of document.querySelectorAll('[data-icon]'))node.prepend(icon(node.dataset.icon));
void start();
async function selectWorkspace(key){
  if(key===state.snapshot?.workspace_key){$('workspace-menu').open=false;return;}
  if(!await allowLeave())return;const url=new URL(location.href);url.searchParams.set('workspace',key);location.assign(url);
}
$('workspace-menu').addEventListener('keydown',event=>{
  if(!['ArrowUp','ArrowDown','Home','End'].includes(event.key))return;
  const choices=[...$('workspace-options').querySelectorAll('button')];if(!choices.length)return;event.preventDefault();
  const index=choices.indexOf(document.activeElement);
  choices[event.key==='Home'?0:event.key==='End'?choices.length-1:index<0?0:(index+(event.key==='ArrowUp'?-1:1)+choices.length)%choices.length].focus();
});
$('show-archived').onchange=async()=>{if(!await allowLeave()){$('show-archived').checked=state.snapshot.include_archived;return;}const url=new URL(location.href);url.searchParams.set('archived',$('show-archived').checked?'1':'0');location.assign(url);};
