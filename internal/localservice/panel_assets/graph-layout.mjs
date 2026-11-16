// Each node reserves its complete circle, title and caption rectangle.
// Separate columns express depth; subtree spans keep sibling labels apart.
export function layoutHierarchy(root, gap=26, columnGap=60) {
  if(!root)return [];
  const nodes=[],widths=[];
  function measure(node,depth,side){
    node.depth=depth;node.side=side;nodes.push(node);
    widths[depth]=Math.max(widths[depth]||0,node.bounds.right-node.bounds.left);
    const children=node.children||[];
    for(const child of children)measure(child,depth+1,side);
    const own=node.bounds.bottom-node.bounds.top;
    node.span=Math.max(own,children.reduce((sum,c)=>sum+c.span,0)+Math.max(0,children.length-1)*gap);
  }
  const children=root.children||[],split=Math.ceil(children.length/2);
  root.depth=0;root.side=0;nodes.push(root);
  widths[0]=root.bounds.right-root.bounds.left;
  children.forEach((child,i)=>measure(child,1,i<split?1:-1));
  const columns=[0];
  for(let i=1;i<widths.length;i++)columns[i]=columns[i-1]+widths[i-1]/2+columnGap+widths[i]/2;
  const boxMiddle=n=>(n.bounds.top+n.bounds.bottom)/2;
  function place(node,top){
    node.x=columns[node.depth]*node.side;
    node.y=top+node.span/2-boxMiddle(node);
    const kids=node.children||[],height=kids.reduce((sum,c)=>sum+c.span,0)+Math.max(0,kids.length-1)*gap;
    let next=top+(node.span-height)/2;
    for(const child of kids){place(child,next);next+=child.span+gap;}
  }
  root.x=0;root.y=0;
  for(const side of [1,-1]){
    const branch=children.filter(n=>n.side===side);
    const height=branch.reduce((sum,c)=>sum+c.span,0)+Math.max(0,branch.length-1)*gap;
    let top=boxMiddle(root)-height/2;
    for(const child of branch){place(child,top);top+=child.span+gap;}
  }
  return nodes;
}

export function separateOverlaps(nodes,positions,priority='') {
  const sorted=[...nodes].sort((a,b)=>(b.id===priority)-(a.id===priority));
  const placed=[];
  for(const node of sorted){
    const p=positions[node.id],b=node.bounds;
    let hit;
    do {
      hit=placed.find(other=>p.x+b.left<other.right+16&&p.x+b.right>other.left-16&&p.y+b.top<other.bottom+16&&p.y+b.bottom>other.top-16);
      if(hit)p.y=hit.bottom+16-b.top;
    }while(hit);
    placed.push({left:p.x+b.left,right:p.x+b.right,top:p.y+b.top,bottom:p.y+b.bottom});
  }
}

export function zoomAt(transform,scale,point) {
  const k=Math.max(.08,Math.min(4,scale));
  return {k,x:point.x-(point.x-transform.x)*k/transform.k,y:point.y-(point.y-transform.y)*k/transform.k};
}

export function fitBounds(bounds,viewport,minScale=.9,maxScale=1) {
  const k=Math.max(minScale,Math.min(maxScale,(viewport.width-64)/Math.max(1,bounds.right-bounds.left),
    (viewport.height-80)/Math.max(1,bounds.bottom-bounds.top)));
  return {k,x:viewport.width/2-(bounds.left+bounds.right)/2*k,y:(viewport.height-40)/2-(bounds.top+bounds.bottom)/2*k};
}

export function mapProjection(bounds,width=160,height=108) {
  const k=Math.min((width-16)/Math.max(1,bounds.right-bounds.left),(height-16)/Math.max(1,bounds.bottom-bounds.top));
  return {k,x:width/2-(bounds.left+bounds.right)/2*k,y:height/2-(bounds.top+bounds.bottom)/2*k};
}

// Preserve the displayed frame when a disclosure is reversed mid-transition.
// Entering/exiting descendants travel from/to their nearest surviving ancestor.
export function graphTransition(before,after,displayed,targets){
  const oldNodes=new Map(before.map(n=>[n.id,n])),newNodes=new Map(after.map(n=>[n.id,n]));
  function ancestor(node,nodes,positions){
    const seen=new Set();
    while(node?.parent&&!seen.has(node.parent)){
      seen.add(node.parent);if(positions[node.parent])return positions[node.parent];node=nodes.get(node.parent);
    }
    return null;
  }
  const tracks={};
  for(const n of after){
    const to={...targets[n.id],opacity:1},start=ancestor(n,newNodes,displayed)||to;
    tracks[n.id]={from:displayed[n.id]?{opacity:1,...displayed[n.id]}:{...start,opacity:0},to};
  }
  const surviving=Object.fromEntries(after.map(n=>[n.id,targets[n.id]]));
  for(const n of before)if(!newNodes.has(n.id)&&displayed[n.id]){
    const from={opacity:1,...displayed[n.id]};
    tracks[n.id]={from,to:{...(ancestor(n,oldNodes,surviving)||from),opacity:0}};
  }
  return tracks;
}
export function sampleGraphTransition(tracks,progress){
  const t=Math.max(0,Math.min(1,progress)),e=t*t*(3-2*t);
  return Object.fromEntries(Object.entries(tracks).map(([id,{from:a,to:b}])=>[id,{
    x:a.x+(b.x-a.x)*e,y:a.y+(b.y-a.y)*e,opacity:a.opacity+(b.opacity-a.opacity)*e
  }]));
}
