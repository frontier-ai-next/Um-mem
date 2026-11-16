import assert from 'node:assert/strict';
import {layoutHierarchy,separateOverlaps,graphTransition,sampleGraphTransition} from '../internal/localservice/panel_assets/graph-layout.mjs';

let sequence=0;
function node(children=[],width=230,height=140){return {id:String(sequence++),children,bounds:{left:-width/2,right:width/2,top:-50,bottom:height-50}};}
function assertClear(nodes,positions){
  for(let i=0;i<nodes.length;i++)for(let j=0;j<i;j++){
    const a=nodes[i],b=nodes[j],p=positions?.[a.id]||a,q=positions?.[b.id]||b;
    const overlap=p.x+a.bounds.left<q.x+b.bounds.right&&p.x+a.bounds.right>q.x+b.bounds.left&&p.y+a.bounds.top<q.y+b.bounds.bottom&&p.y+a.bounds.bottom>q.y+b.bounds.top;
    assert.equal(overlap,false,`Labels overlap: ${a.id}, ${b.id}`);
  }
}
const examples=[node(),node([node(),node(),node()]),node([node(Array.from({length:40},(_,i)=>node([],160+i%5*40,90+i%3*40))),node(),node()])];
let deep=node();for(let i=0;i<15;i++)deep=node([deep,node([],330,180)]);examples.push(deep);
for(const tree of examples){
  const nodes=layoutHierarchy(tree);assertClear(nodes);
  const positions=Object.fromEntries(nodes.map(n=>[n.id,{x:n.x,y:n.y}]));
  for(const n of nodes.slice(0,5))positions[n.id]={x:0,y:0};
  separateOverlaps(nodes,positions,nodes.at(-1).id);assertClear(nodes,positions);
  for(const n of nodes)for(const child of n.children){assert.equal(child.depth,n.depth+1);assert.ok(Math.abs(child.x)>Math.abs(n.x));}
}
console.log('Layout: long labels, 40 siblings, 15 levels, dragged overlaps — passed.');

const parent={id:'parent'},child={id:'child',parent:'parent'},leaf={id:'leaf',parent:'child'};
const closed=[parent],open=[parent,child,leaf];
const anchor={x:120,y:75},targets={parent:anchor,child:{x:320,y:25},leaf:{x:520,y:50}};
const opening=graphTransition(closed,open,{parent:anchor},targets);
assert.deepEqual(sampleGraphTransition(opening,0).leaf,{...anchor,opacity:0});
const halfway=sampleGraphTransition(opening,.5);
assert.deepEqual(halfway.parent,{...anchor,opacity:1});
assert.equal(halfway.leaf.opacity,.5);
assert.deepEqual(sampleGraphTransition(opening,1).leaf,{...targets.leaf,opacity:1});
const closing=graphTransition(open,closed,halfway,{parent:anchor});
assert.deepEqual(sampleGraphTransition(closing,0),halfway,'Reversal must start at the displayed positions and opacity');
assert.deepEqual(sampleGraphTransition(closing,1).leaf,{...anchor,opacity:0},'Nested exits return to the nearest surviving ancestor');
const fading=sampleGraphTransition(closing,.5);
const reopened=graphTransition(open,open,fading,targets);
assert.deepEqual(sampleGraphTransition(reopened,0),fading,'Reappearing exits must not flash opaque');
const empty=graphTransition(open,[],halfway,{});
assert.deepEqual(sampleGraphTransition(empty,1).leaf,{...halfway.leaf,opacity:0});
assert.deepEqual(anchor,{x:120,y:75},'Transition must not mutate layout targets');
const overlapping=[node(),node()];const fixed=overlapping[1].id;
const dragged=Object.fromEntries(overlapping.map(n=>[n.id,{x:10,y:20}]));
separateOverlaps(overlapping,dragged,fixed);assert.deepEqual(dragged[fixed],{x:10,y:20});
console.log('Motion: stable anchor, nested entry/exit, interrupted reversal, empty graph — passed.');

// The atlas reserves entire block/record cards, including their footer and toggle.
const cardRoot=node(Array.from({length:6},()=>node(Array.from({length:6},()=>node([],224,88)),200,72)),200,72);
const cards=layoutHierarchy(cardRoot,22,48);assertClear(cards);
assert.equal(cards.length,43);
for(const n of cards)assert.ok(Number.isFinite(n.x)&&Number.isFinite(n.y));
console.log('Atlas cards: 43 mixed block/record cards remain separated — passed.');
