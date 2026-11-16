import assert from 'node:assert/strict';
import {scopeBlocks,scopeCount,createBranchPager,filterBlocks,filterRecords,sortRecords,panelPreferences,readAllRecords} from '../internal/localservice/panel_assets/branch-records.mjs';

const leaf=(id,count)=>({block_id:id,canonical_record_count:count});
const research={...leaf('research',0),children:[leaf('findings',2),leaf('sources',2),leaf('empty',0)]};
const tree=[{...leaf('root',0),children:[research,leaf('other',1)]}];
const row=(id,block)=>({record_id:id,logical_block_id:block});
assert.equal(scopeCount(scopeBlocks(tree,'research')),4);
assert.equal(scopeCount(scopeBlocks(tree,'research',false)),0);
assert.equal(scopeCount(scopeBlocks(tree)),5);
assert.deepEqual(scopeBlocks(tree,'missing'),[]);
const calls=[];
const pager=createBranchPager(scopeBlocks(tree,'research'),async(id,cursor)=>{
  calls.push([id,cursor]);
  return {records:[row(id+'-1',id),row(id+'-2',id),row('foreign','other')],next_cursor:''};
});
assert.equal((await pager.next(3)).length,3);
assert.equal(pager.hasMore,true);
assert.deepEqual((await pager.next(3)).map(r=>r.record_id),['sources-2']);
assert.equal(pager.hasMore,false);
assert.deepEqual(calls,[['findings',''],['sources','']]);

// A leaf larger than one API page, duplicates across pages and a following leaf.
const large=createBranchPager([leaf('large',130),leaf('last',1)],async(id,cursor)=>{
  if(id==='last')return {records:[row('last-1',id)]};
  const start=Number(cursor||0),end=Math.min(130,start+64);
  return {records:[...(start?[row('large-0',id)]:[]),...Array.from({length:end-start},(_,i)=>row('large-'+(start+i),id))],next_cursor:end<130?String(end):''};
});
let records=[];while(large.hasMore)records.push(...await large.next());
assert.equal(records.length,131);assert.equal(new Set(records.map(r=>r.record_id)).size,131);
assert.equal(records.at(-1).record_id,'last-1');

// A partial failure must not advance any block cursor or lose earlier records.
let fail=true;
const retry=createBranchPager([leaf('a',1),leaf('b',1)],async id=>{
  if(id==='b'&&fail)throw new Error('offline');
  return {records:[row(id,id)]};
});
await assert.rejects(retry.next(),/offline/);fail=false;
assert.deepEqual((await retry.next()).map(r=>r.record_id),['a','b']);
assert.equal(retry.hasMore,false);
const empty=createBranchPager([leaf('empty',0)],()=>{throw new Error('Must not load empty blocks');});
assert.deepEqual(await empty.next(),[]);assert.equal(empty.hasMore,false);
console.log('Branch records: nested counts, exact scope, 131 records, deduplication and retry — passed.');

const namedTree=[{...leaf('root',0),name:'Memory',children:[{...leaf('parent',0),name:'Research',children:[{...leaf('deep',2),name:'Findings'},{...leaf('zero',0),name:'Empty findings'}]},{...leaf('zero-branch',0),name:'Empty',children:[leaf('zero-child',0)]}]}];
const nonempty=filterBlocks(namedTree,{hideEmpty:true});
assert.deepEqual([...nonempty.visible],['deep','parent','root']);
const found=filterBlocks(namedTree,{query:'  FINDINGS  '});
assert.deepEqual([...found.matches],['deep','zero']);
assert.deepEqual([...found.expanded],['parent','root']);
assert.deepEqual([...filterBlocks(namedTree,{query:'findings',hideEmpty:true}).matches],['deep']);
assert.equal(filterBlocks(namedTree,{query:'missing'}).visible.size,0);
assert.equal(namedTree[0].children.length,2); // Catalog and counts stay intact.

// A rare kind on the third page, including a custom type and combined filters.
const filterPager=createBranchPager([leaf('data',150)],async(id,cursor)=>{
  const start=Number(cursor||0),end=Math.min(150,start+64);
  return {records:Array.from({length:end-start},(_,i)=>({...row('r'+(start+i),id),status:start+i===149?'accepted':'candidate',kind:start+i>=148?'custom_kind':'observation'})),next_cursor:end<150?String(end):''};
});
const progress=[],all=await readAllRecords(filterPager,()=>true,n=>progress.push(n));
assert.deepEqual(progress,[64,128,150]);
assert.deepEqual(filterRecords(all,{kind:'custom_kind',status:'accepted'}).map(r=>r.record_id),['r149']);
assert.equal(filterRecords(all,{kind:'custom_kind'}).length,2);
assert.equal(filterRecords(all,{kind:'observation',status:'accepted'}).length,0);
assert.equal(filterRecords(all,{}).length,150);
let current=true,requests=0;
const canceled=createBranchPager([leaf('data',150)],async id=>{requests++;return {records:[row('one',id)],next_cursor:'next'};});
await readAllRecords({get hasMore(){return canceled.hasMore;},next:()=>canceled.next(1)},()=>current,()=>{current=false;});
assert.equal(requests,1);
console.log('Filters: ancestor paths, nested emptiness, combined filters, late-page matches and cancellation — passed.');

const dated=[{record_id:'missing'},{record_id:'old',recorded_at:'2026-01-01T00:00:00Z'},
  {record_id:'new',recorded_at:'2026-10-23T10:00:00Z'},
  {record_id:'same',recorded_at:'2026-10-23T13:00:00+03:00'},{record_id:'bad',recorded_at:'bad'}];
assert.deepEqual(sortRecords(dated).map(r=>r.record_id),['new','same','old','missing','bad']);
assert.deepEqual(sortRecords(dated,'oldest').map(r=>r.record_id),['old','new','same','missing','bad']);
assert.equal(dated[0].record_id,'missing');
const datedPager=createBranchPager([leaf('first',64),leaf('last',1)],async id=>({records:id==='last'?[{...row('latest',id),recorded_at:'2026-10-23T00:00:00Z'}]:Array.from({length:64},(_,i)=>({...row('old'+i,id),recorded_at:'2026-01-01T00:00:00Z'}))}));
const sortedAll=sortRecords(await readAllRecords(datedPager));
assert.equal(sortedAll.slice(0,64)[0].record_id,'latest');assert.equal(sortedAll.length,65);
const prefs=panelPreferences({recordStatus:'candidate',recordKind:'custom',recordSort:'oldest',blockQuery:'Research',hideEmpty:true,descendants:false,mode:'list',records:dated,draft:'private'});
assert.deepEqual(panelPreferences(JSON.parse(JSON.stringify(prefs))),prefs);
assert.equal('records' in prefs,false);assert.equal('draft' in prefs,false);
assert.deepEqual(panelPreferences({recordStatus:[],recordKind:{},recordSort:'wrong',blockQuery:12,mode:'wrong'}),panelPreferences(null));
console.log('Sort and preferences: global date order, time zones, stable ties, missing dates, reload validation — passed.');
