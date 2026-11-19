// Compose existing exact-block pages; never broaden the server's workspace scope.
const blockNames = {
  memory_root_index:'Вся память', directive_memory_index:'Правила и установки',
  working_memory_index:'Текущая работа', durable_knowledge_index:'Долгосрочные знания',
  semantic_memory_index:'Знания проекта', archive_memory_index:'Архив',
  user_global_core_directives:'Правила пользователя', workspace_core_directives:'Правила пространства',
  user_global_preferences:'Предпочтения', task_state:'Текущая задача', execution_working_state:'Ход работы',
  verified_lessons:'Проверенные уроки', durable_reports:'Исследования и отчёты', skills_index:'Навыки',
  project_resources:'Ресурсы', code_resources:'Сведения о коде', stable_project_facts:'Факты проекта',
  mutable_project_state:'Состояние проекта', decisions_and_contracts:'Решения и договорённости',
  artifact_claims:'Сведения об артефактах', project_ideas:'Идеи проекта', reflection_candidates:'Выводы на проверке',
  transient_logs:'Журнал событий', workspace_episodes:'История работы',
  project_scope_stable_project_facts:'Общие факты проекта',
  project_scope_decisions_and_contracts:'Решения всего проекта',
  project_scope_artifact_claims:'Артефакты всего проекта',
  project_scope_project_ideas:'Идеи всего проекта',
  project_scope_reflection_candidates:'Выводы всего проекта',
};

export function blockName({block_id:id='',name=''}={}) {
  if(name&&name!==id)return name;
  const label=blockNames[id]||blockNames[id.replace(/^project_scope_/,'')];
  if(label)return label;
  const readable=id.replaceAll('_',' ');
  return readable?readable[0].toLocaleUpperCase()+readable.slice(1):'Общая память';
}

export function scopeBlocks(tree, blockId='', descendants=true) {
  const nodes=[],seen=new Set();
  function visit(node) {
    if(seen.has(node.block_id))return;
    seen.add(node.block_id);nodes.push(node);
    for(const child of node.children||[])visit(child);
  }
  for(const root of tree||[])visit(root);
  if(!blockId)return nodes;
  const root=nodes.find(node=>node.block_id===blockId);
  if(!root)return [];
  if(!descendants)return [root];
  nodes.length=0;seen.clear();visit(root);return nodes;
}

export function scopeCount(blocks) {
  return blocks.reduce((sum,node)=>sum+Number(node.canonical_record_count||0),0);
}

// Preserve ancestors of matches without changing the catalog or saved folds.
export function filterBlocks(tree, {query='',hideEmpty=false}={}) {
  const visible=new Set(),expanded=new Set(),matches=new Set(),needle=query.trim().toLocaleLowerCase();
  function visit(node) {
    let total=Number(node.canonical_record_count||0),childVisible=false;
    for(const child of node.children||[]){const result=visit(child);total+=result.total;childVisible ||= result.visible;}
    const match=Boolean(needle&&[blockName(node),node.block_id].some(value=>value.toLocaleLowerCase().includes(needle)));
    const show=(!hideEmpty||total>0)&&(!needle||match||childVisible);
    if(show){visible.add(node.block_id);if(match)matches.add(node.block_id);if(needle&&childVisible)expanded.add(node.block_id);}
    return {total,visible:show};
  }
  for(const root of tree||[])visit(root);
  return {visible,expanded,matches};
}

// Fold routing-only steps in the overview. The catalog and its block IDs stay intact.
export function compactTopicTree(tree, visible, selected=[]) {
  const retained=new Set(selected);
  function visit(node, root=false) {
    if(!visible.has(node.block_id))return [];
    const children=(node.children||[]).flatMap(child=>visit(child));
    const scopeRoot=node.block_type==='workspace_profile_root'||node.block_id==='memory_root_index';
    const routingOnly=(node.index_only||scopeRoot)&&Number(node.canonical_record_count)===0&&
      !node.relation_count&&!retained.has(node.block_id)&&node.status!=='archived';
    if(routingOnly&&(children.length===1||(root&&scopeRoot&&children.length>0)))return children;
    return [{...node,children}];
  }
  return tree.flatMap(node=>visit(node,true));
}

export function filterRecords(records, {status='',kind=''}={}) {
  const matches=[], sources=new Map();
  for(const record of records){
    if(status&&record.status!==status||kind&&record.kind!==kind)continue;
    if(record.kind!=='source_excerpt'||!record.duplicate_cluster_id){matches.push(record);continue;}
    const key=JSON.stringify([record.logical_block_id,record.status,record.duplicate_cluster_id]);
    const group=sources.get(key);
    if(!group){sources.set(key,{record,count:1});continue;}
    group.count++;
    if(record.recorded_at>group.record.recorded_at)group.record=record;
  }
  for(const {record,count} of sources.values())matches.push(count===1?record:{...record,source_copies:count});
  return matches;
}

export function sortRecords(records, order='newest') {
  return [...records].sort((a,b)=>{
    const left=Date.parse(a.recorded_at),right=Date.parse(b.recorded_at);
    if(!Number.isFinite(left)||!Number.isFinite(right))return Number.isFinite(left)?-1:Number.isFinite(right)?1:0;
    return order==='oldest'?left-right:right-left;
  });
}

// Only view preferences cross reloads. Never persist record data or drafts.
export function panelPreferences(saved={}) {
  const text=value=>typeof value==='string'?value:'';
  return {recordStatus:text(saved?.recordStatus),recordKind:text(saved?.recordKind),
    recordSort:saved?.recordSort==='oldest'?'oldest':'newest',blockQuery:text(saved?.blockQuery),
    hideEmpty:saved?.hideEmpty!==false,descendants:saved?.descendants!==false,mode:saved?.mode==='list'?'list':'graph'};
}

// Filtering needs the complete scope, not just the first unfiltered page.
export async function readAllRecords(pager, isCurrent=()=>true, onProgress=()=>{}) {
  const records=[];
  while(pager.hasMore&&isCurrent()){
    records.push(...await pager.next());
    if(isCurrent())onProgress(records.length);
  }
  return records;
}

export function createBranchPager(blocks, readPage) {
  const ids=[...new Set(blocks.filter(node=>node.canonical_record_count!==0).map(node=>node.block_id))];
  let position=0,cursor='',buffer=[],seen=new Set(),busy=false;
  return {
    get hasMore(){return buffer.length>0||position<ids.length;},
    async next(limit=64) {
      if(busy)throw new Error('Загрузка уже выполняется');
      busy=true;
      // Commit the cursor only after a complete page, so failures can be retried.
      let nextPosition=position,nextCursor=cursor,nextBuffer=[...buffer];
      const nextSeen=new Set(seen),records=[];
      try {
        while(records.length<limit&&(nextBuffer.length||nextPosition<ids.length)) {
          if(!nextBuffer.length) {
            const id=ids[nextPosition],page=await readPage(id,nextCursor);
            if(page.next_cursor&&page.next_cursor===nextCursor)throw new Error('Курсор записей не изменился');
            nextBuffer=page.records.filter(record=>record.logical_block_id===id);
            nextCursor=page.next_cursor||'';
            if(!nextCursor)nextPosition++;
          }
          while(nextBuffer.length&&records.length<limit) {
            const record=nextBuffer.shift();
            if(!nextSeen.has(record.record_id)){nextSeen.add(record.record_id);records.push(record);}
          }
        }
        position=nextPosition;cursor=nextCursor;buffer=nextBuffer;seen=nextSeen;
        return records;
      } finally {busy=false;}
    },
  };
}
