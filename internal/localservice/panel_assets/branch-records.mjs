// Compose existing exact-block pages; never broaden the server's workspace scope.
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
    const match=Boolean(needle&&(node.name||'').toLocaleLowerCase().includes(needle));
    const show=(!hideEmpty||total>0)&&(!needle||match||childVisible);
    if(show){visible.add(node.block_id);if(match)matches.add(node.block_id);if(needle&&childVisible)expanded.add(node.block_id);}
    return {total,visible:show};
  }
  for(const root of tree||[])visit(root);
  return {visible,expanded,matches};
}

export function filterRecords(records, {status='',kind=''}={}) {
  return records.filter(record=>(!status||record.status===status)&&(!kind||record.kind===kind));
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
    hideEmpty:saved?.hideEmpty===true,descendants:saved?.descendants!==false,mode:saved?.mode==='list'?'list':'graph'};
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
