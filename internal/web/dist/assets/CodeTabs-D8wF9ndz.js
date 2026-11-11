import{D as e,T as t,c as n,t as r,u as i}from"./button-2a_hdyxA.js";import{t as a}from"./check-D97_OaY4.js";import{t as o}from"./copy-CNlgyjvN.js";import{i as s,n as c,r as l,t as u}from"./tabs-BH3ghiq3.js";import{p as d}from"./index-DH4lNBeH.js";var f=e(t(),1),p=i();async function m(e){if(navigator.clipboard?.writeText)return navigator.clipboard.writeText(e);let t=document.createElement(`textarea`);t.value=e,t.style.position=`fixed`,t.style.opacity=`0`,document.body.appendChild(t),t.select();let n=document.execCommand(`copy`);if(t.remove(),!n)throw Error(`copy failed`)}function h({text:e,label:t=`Copy`,className:i}){let[s,c]=(0,f.useState)(!1);return(0,p.jsx)(r,{type:`button`,variant:`ghost`,size:`icon-xs`,"aria-label":t,title:t,className:n(`text-muted-foreground hover:text-foreground`,i),onClick:()=>m(e).then(()=>{c(!0),setTimeout(()=>c(!1),1500)},()=>d(`Could not copy. Select the text instead.`,{error:!0})),children:s?(0,p.jsx)(a,{}):(0,p.jsx)(o,{})})}function g({code:e,className:t}){return(0,p.jsxs)(`div`,{className:n(`relative rounded-lg border bg-muted/50`,t),children:[(0,p.jsx)(h,{text:e,label:`Copy code`,className:`absolute top-2 right-2 bg-muted`}),(0,p.jsx)(`pre`,{className:`overflow-x-auto p-4 pr-12 font-mono text-[13px] leading-relaxed`,children:(0,p.jsx)(`code`,{children:e})})]})}function _({samples:e,className:t}){let[r,i]=(0,f.useState)(e[0]?.label??``);return(0,p.jsxs)(u,{value:r,onValueChange:i,className:n(`min-w-0`,t),children:[(0,p.jsx)(l,{children:e.map(e=>(0,p.jsx)(s,{value:e.label,children:e.label},e.label))}),e.map(e=>(0,p.jsx)(c,{value:e.label,className:`min-w-0`,children:(0,p.jsx)(g,{code:e.code})},e.label))]})}function v(e,t,n=`chat`){return n===`embedding`?[{label:`curl`,code:`curl ${e}/embeddings \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${t}",
    "input": "The quick brown fox"
  }'`},{label:`Python`,code:`import os
from openai import OpenAI

client = OpenAI(base_url="${e}", api_key=os.environ["GATEWAY_API_KEY"])

res = client.embeddings.create(model="${t}", input="The quick brown fox")
print(len(res.data[0].embedding))`},{label:`TypeScript`,code:`import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${e}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.embeddings.create({ model: "${t}", input: "The quick brown fox" });
console.log(res.data[0].embedding.length);`}]:[{label:`curl`,code:`curl ${e}/chat/completions \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${t}",
    "messages": [{"role": "user", "content": "Hello"}]
  }'`},{label:`Python`,code:`import os
from openai import OpenAI

client = OpenAI(base_url="${e}", api_key=os.environ["GATEWAY_API_KEY"])

res = client.chat.completions.create(
    model="${t}",
    messages=[{"role": "user", "content": "Hello"}],
)
print(res.choices[0].message.content)`},{label:`TypeScript`,code:`import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${e}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.chat.completions.create({
  model: "${t}",
  messages: [{ role: "user", content: "Hello" }],
});
console.log(res.choices[0].message.content);`}]}var y=()=>`${location.origin}/v1`;export{v as a,y as i,_ as n,h as r,g as t};