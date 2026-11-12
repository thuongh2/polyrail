import{d as e,m as t,s as n,u as r}from"./button-CIyBVDPq.js";import{F as i,V as a,a as o,c as s,d as c,j as l,m as u,w as d,y as f}from"./index-CJO-C1Jq.js";import{t as p}from"./check-CksC8o8Z.js";import{t as m}from"./key-round-C4U_IL9A.js";import{t as h}from"./plus-CF3dq2Qf.js";import{r as g,t as _}from"./CodeTabs-D0Xg5z9o.js";import{c as v,i as y,n as b,o as x,s as S,t as C}from"./select-DximZ3hv.js";import{t as w}from"./hooks-C-ICaP_k.js";var T=t(e(),1),E=r(),D=[{id:`codex`,name:`Codex`,where:`Add a provider to ~/.codex/config.toml and export the key, then run codex.`,api:`openai`,snippet:({origin:e,key:t,model:n})=>`# ~/.codex/config.toml
model = "${n}"
model_provider = "polyrail"

[model_providers.polyrail]
name = "Polyrail"
base_url = "${e}/v1"
env_key = "GATEWAY_API_KEY"
wire_api = "responses"

# shell
export GATEWAY_API_KEY="${t}"
codex`},{id:`claude-code`,name:`Claude Code`,where:`Claude Code speaks the Anthropic API: point it at the gateway's /anthropic endpoint.`,api:`anthropic`,snippet:({origin:e,key:t,model:n})=>`export ANTHROPIC_BASE_URL="${e}/anthropic"
export ANTHROPIC_AUTH_TOKEN="${t}"
export ANTHROPIC_MODEL="${n}"
export ANTHROPIC_DEFAULT_HAIKU_MODEL="${n}"
claude`},{id:`opencode`,name:`OpenCode`,where:`Add a provider to opencode.json (project) or ~/.config/opencode/opencode.json.`,api:`openai`,snippet:({origin:e,model:t})=>`{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "polyrail": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Polyrail",
      "options": { "baseURL": "${e}/v1", "apiKey": "{env:GATEWAY_API_KEY}" },
      "models": { "${t}": { "name": "${t}" } }
    }
  },
  "model": "polyrail/${t}"
}`},{id:`gemini-cli`,name:`Gemini CLI`,where:`Gemini CLI speaks the Gemini API: point it at the gateway's /genai endpoint.`,api:`gemini`,snippet:({origin:e,key:t,model:n})=>`export GOOGLE_GEMINI_BASE_URL="${e}/genai"
export GEMINI_API_KEY="${t}"
gemini -m "${n}"`},{id:`cline`,name:`Cline`,where:`In Cline settings choose API Provider "OpenAI Compatible" and fill in these fields.`,api:`openai`,snippet:({origin:e,key:t,model:n})=>`API Provider:  OpenAI Compatible
Base URL:      ${e}/v1
API Key:       ${t}
Model ID:      ${n}`},{id:`open-webui`,name:`Open WebUI`,where:`Add an OpenAI API in Admin Panel → Settings → Connections with this URL and key.`,api:`openai`,snippet:({origin:e,key:t})=>`URL:  ${e}/v1
Key:  ${t}`},{id:`curl`,name:`curl`,where:`Any OpenAI-compatible client works with the same endpoint and key.`,api:`openai`,snippet:({origin:e,key:t,model:n})=>`curl ${e}/v1/chat/completions \\
  -H "Authorization: Bearer ${t}" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "${n}", "messages": [{"role": "user", "content": "Hello"}]}'`},{id:`python`,name:`Python`,where:`The official openai package with the gateway as base_url.`,api:`openai`,snippet:({origin:e,key:t,model:n})=>`from openai import OpenAI

client = OpenAI(base_url="${e}/v1", api_key="${t}")
r = client.chat.completions.create(model="${n}", messages=[{"role": "user", "content": "Hello"}])
print(r.choices[0].message.content)`},{id:`typescript`,name:`TypeScript`,where:`The official openai package with the gateway as baseURL.`,api:`openai`,snippet:({origin:e,key:t,model:n})=>`import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${e}/v1", apiKey: "${t}" });
const r = await client.chat.completions.create({ model: "${n}", messages: [{ role: "user", content: "Hello" }] });
console.log(r.choices[0].message.content);`}],O=`__new__`,k=`YOUR_API_KEY`;function A(){let{org:e,wsList:t,wsBody:r,wsCan:g}=u(),A=e?l(e.id):``,M=w(()=>A?d(t(`${A}/keys`)):Promise.resolve([]),[A]),N=w(()=>i.models(),[]),[P,F]=(0,T.useState)(D[0].id),[I,L]=(0,T.useState)(``),[R,z]=(0,T.useState)({}),[B,V]=(0,T.useState)(!1),H=(0,T.useMemo)(()=>(M.data??[]).filter(e=>!e.revoked),[M.data]),U=H.find(e=>e.id===I)??H[0],W=U?R[U.id]:void 0,G=D.find(e=>e.id===P)??D[0],K=location.origin,q=K+(G.api===`openai`?`/v1`:G.api===`anthropic`?`/anthropic`:`/genai`),J=N.data?.find(e=>e.kind===`chat`)?.id??`openai/gpt-4o-mini`,Y=W??k,X=g(`member`),Z=async()=>{V(!0);try{let e=await f(`POST`,`${A}/keys`,{...r,name:`${G.name} (${new Date().toLocaleDateString()})`});z(t=>({...t,[e.key.id]:e.secret})),L(e.key.id),M.reload(),c(`Created “${e.key.name}”. Its secret is shown on this page until you leave it.`)}catch(e){c(e instanceof Error?e.message:`Could not create the key`,{error:!0})}finally{V(!1)}};return e?(0,E.jsxs)(E.Fragment,{children:[(0,E.jsx)(s,{title:`Connect`,description:`Use the gateway from coding agents, chat apps and SDKs: pick a tool and a key, then copy the settings.`}),(0,E.jsxs)(`div`,{className:`grid gap-6 md:grid-cols-[12rem_minmax(0,1fr)]`,children:[(0,E.jsx)(`nav`,{"aria-label":`Tools`,className:`no-scrollbar -mx-4 flex gap-1 overflow-x-auto px-4 md:mx-0 md:flex-col md:overflow-visible md:px-0`,children:D.map(e=>(0,E.jsx)(`button`,{type:`button`,onClick:()=>F(e.id),"aria-current":e.id===G.id?`true`:void 0,className:n(`h-9 shrink-0 rounded-md px-3 text-left text-sm whitespace-nowrap text-muted-foreground hover:bg-muted hover:text-foreground`,`focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring`,e.id===G.id&&`bg-muted font-medium text-foreground`),children:e.name},e.id))}),(0,E.jsxs)(`section`,{"aria-label":G.name,className:`grid min-w-0 content-start gap-5`,children:[(0,E.jsxs)(`div`,{className:`grid gap-2`,children:[(0,E.jsx)(`label`,{htmlFor:`connect-key`,className:`text-sm font-medium`,children:`Access key`}),(0,E.jsxs)(C,{value:U?.id??``,onValueChange:e=>e===O?void Z():L(e),disabled:M.loading||B,children:[(0,E.jsxs)(S,{id:`connect-key`,className:`w-full justify-start [&>span]:flex-1 [&>span]:text-left`,children:[(0,E.jsx)(m,{className:`text-muted-foreground`,"aria-hidden":!0}),(0,E.jsx)(v,{placeholder:M.loading?`Loading keys…`:`No API key yet`})]}),(0,E.jsxs)(b,{children:[H.map(e=>(0,E.jsxs)(y,{value:e.id,children:[e.name,(0,E.jsxs)(`span`,{className:`ml-2 font-mono text-xs text-muted-foreground`,children:[e.prefix,`…`]})]},e.id)),X&&(0,E.jsxs)(E.Fragment,{children:[H.length>0&&(0,E.jsx)(x,{}),(0,E.jsxs)(y,{value:O,children:[(0,E.jsx)(h,{"aria-hidden":!0}),` Create a new key for `,G.name]})]})]})]}),!W&&(0,E.jsxs)(`p`,{className:`text-xs text-muted-foreground`,children:[`Keys are stored as hashes, so an existing key's secret can't be shown again: put the one you saved where the configuration says YOUR_API_KEY, or create a new key here to fill it in.`,` `,(0,E.jsx)(a,{href:`/keys`,className:`underline underline-offset-4`,children:`Manage keys`})]})]}),(0,E.jsxs)(`div`,{className:`grid gap-3 rounded-lg border p-4`,children:[(0,E.jsx)(`p`,{className:`text-sm text-muted-foreground`,children:G.where}),(0,E.jsx)(j,{label:`Endpoint`,value:q}),(0,E.jsx)(j,{label:`API key`,value:W??(U?`${U.prefix}…`:`Create a key first`),copy:W,hint:W?(0,E.jsxs)(`span`,{className:`inline-flex items-center gap-1 text-xs text-emerald-700 dark:text-emerald-400`,children:[(0,E.jsx)(p,{className:`size-3.5`,"aria-hidden":!0}),` New key: copy it now, it won't be shown again after you leave this page.`]}):void 0}),(0,E.jsx)(j,{label:`Model`,value:J})]}),(0,E.jsxs)(`div`,{className:`grid gap-2`,children:[(0,E.jsx)(`h2`,{className:`text-sm font-medium`,children:`Configuration`}),(0,E.jsx)(_,{code:G.snippet({origin:K,key:Y,model:J})}),(0,E.jsxs)(`p`,{className:`text-xs text-muted-foreground`,children:[`Any model id from `,(0,E.jsx)(a,{href:`/models`,className:`underline underline-offset-4`,children:`Models`}),` works in place of `,J,`.`]})]})]})]})]}):(0,E.jsx)(o,{title:`You are not in an organization yet`,children:`Ask an admin to add you, then sign in again.`})}function j({label:e,value:t,copy:n,hint:r}){return(0,E.jsxs)(`div`,{className:`grid gap-1 sm:grid-cols-[6rem_minmax(0,1fr)] sm:items-center`,children:[(0,E.jsx)(`span`,{className:`text-sm text-muted-foreground`,children:e}),(0,E.jsxs)(`div`,{className:`min-w-0`,children:[(0,E.jsxs)(`div`,{className:`flex min-w-0 items-center gap-1 rounded-md border bg-muted/40 px-3 py-1.5`,children:[(0,E.jsx)(`code`,{className:`min-w-0 flex-1 truncate font-mono text-sm`,title:t,children:t}),(n??(e===`API key`?void 0:t))&&(0,E.jsx)(g,{text:n??t,label:`Copy ${e.toLowerCase()}`})]}),r]})]})}export{A as default};