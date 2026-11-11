import{D as e,c as t,k as n,s as r,t as i,u as a}from"./button-ONpRB-1k.js";import{t as o}from"./check-BLHkpCjp.js";import{t as s}from"./copy-CAUbZVr0.js";import{i as c,n as l,r as u,t as d}from"./tabs-CwU8MuDy.js";import{u as f}from"./index-BeYEOXio.js";var p=n(e(),1),m=a();function h({text:e,label:n=`Copy`,className:a}){let[c,l]=(0,p.useState)(!1);return(0,m.jsx)(i,{type:`button`,variant:`ghost`,size:`icon-xs`,"aria-label":n,title:n,className:r(`text-muted-foreground hover:text-foreground`,a),onClick:()=>t(e).then(()=>{l(!0),setTimeout(()=>l(!1),1500)},()=>f(`Could not copy. Select the text instead.`,{error:!0})),children:c?(0,m.jsx)(o,{}):(0,m.jsx)(s,{})})}function g({code:e,className:t}){return(0,m.jsxs)(`div`,{className:r(`relative rounded-lg border bg-muted/50`,t),children:[(0,m.jsx)(h,{text:e,label:`Copy code`,className:`absolute top-2 right-2 bg-muted`}),(0,m.jsx)(`pre`,{className:`overflow-x-auto p-4 pr-12 font-mono text-[13px] leading-relaxed`,children:(0,m.jsx)(`code`,{children:e})})]})}function _({samples:e,className:t}){let[n,i]=(0,p.useState)(e[0]?.label??``);return(0,m.jsxs)(d,{value:n,onValueChange:i,className:r(`min-w-0`,t),children:[(0,m.jsx)(u,{children:e.map(e=>(0,m.jsx)(c,{value:e.label,children:e.label},e.label))}),e.map(e=>(0,m.jsx)(l,{value:e.label,className:`min-w-0 rounded-lg focus-visible:ring-[3px] focus-visible:ring-ring/50`,children:(0,m.jsx)(g,{code:e.code})},e.label))]})}function v(e,t,n=`chat`){return n===`speech`?[{label:`curl`,code:`curl ${e}/audio/speech \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${t}",
    "input": "Hello from the gateway.",
    "voice": "alloy",
    "response_format": "mp3"
  }' \\
  --output speech.mp3`},{label:`Python`,code:`import os
from openai import OpenAI

client = OpenAI(base_url="${e}", api_key=os.environ["GATEWAY_API_KEY"])

res = client.audio.speech.create(
    model="${t}",
    voice="alloy",
    input="Hello from the gateway.",
    response_format="mp3",
)
res.write_to_file("speech.mp3")`},{label:`TypeScript`,code:`import fs from "node:fs";
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${e}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.audio.speech.create({
  model: "${t}",
  voice: "alloy",
  input: "Hello from the gateway.",
  response_format: "mp3",
});
fs.writeFileSync("speech.mp3", Buffer.from(await res.arrayBuffer()));`}]:n===`transcription`?[{label:`curl`,code:`curl ${e}/audio/transcriptions \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -F model=${t} \\
  -F file=@audio.mp3`},{label:`Python`,code:`import os
from openai import OpenAI

client = OpenAI(base_url="${e}", api_key=os.environ["GATEWAY_API_KEY"])

with open("audio.mp3", "rb") as f:
    res = client.audio.transcriptions.create(model="${t}", file=f)
print(res.text)`},{label:`TypeScript`,code:`import fs from "node:fs";
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${e}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.audio.transcriptions.create({
  model: "${t}",
  file: fs.createReadStream("audio.mp3"),
});
console.log(res.text);`}]:n===`embedding`?[{label:`curl`,code:`curl ${e}/embeddings \\
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