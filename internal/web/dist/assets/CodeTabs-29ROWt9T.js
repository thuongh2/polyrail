import{L as e,c as t,s as n,t as r,u as i,z as a}from"./button-DUFa2via.js";import{t as o}from"./check-BZ-lcaD2.js";import{t as s}from"./copy-DvUOQQR0.js";import{i as c,n as l,r as u,t as d}from"./tabs-CCUdxDA2.js";import{u as f}from"./index-BQ3m6Nez.js";var p=a(e(),1),m=i();function h({text:e,label:i=`Copy`,className:a}){let[c,l]=(0,p.useState)(!1);return(0,m.jsx)(r,{type:`button`,variant:`ghost`,size:`icon-xs`,"aria-label":i,title:i,className:n(`text-muted-foreground hover:text-foreground`,a),onClick:()=>t(e).then(()=>{l(!0),setTimeout(()=>l(!1),1500)},()=>f(`Could not copy. Select the text instead.`,{error:!0})),children:c?(0,m.jsx)(o,{}):(0,m.jsx)(s,{})})}function g({code:e,className:t}){return(0,m.jsxs)(`div`,{className:n(`relative rounded-lg border bg-muted/50`,t),children:[(0,m.jsx)(h,{text:e,label:`Copy code`,className:`absolute top-2 right-2 bg-muted`}),(0,m.jsx)(`pre`,{className:`overflow-x-auto p-4 pr-12 font-mono text-[13px] leading-relaxed`,children:(0,m.jsx)(`code`,{children:e})})]})}function _({samples:e,className:t}){let[r,i]=(0,p.useState)(e[0]?.label??``);return(0,m.jsxs)(d,{value:r,onValueChange:i,className:n(`min-w-0`,t),children:[(0,m.jsx)(u,{children:e.map(e=>(0,m.jsx)(c,{value:e.label,children:e.label},e.label))}),e.map(e=>(0,m.jsx)(l,{value:e.label,className:`min-w-0 rounded-lg focus-visible:ring-[3px] focus-visible:ring-ring/50`,children:(0,m.jsx)(g,{code:e.code})},e.label))]})}function v(e,t,n=`chat`){return n===`speech`?[{label:`curl`,code:`curl ${e}/audio/speech \\
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
console.log(res.text);`}]:n===`image`?[{label:`curl`,code:`curl ${e}/images/generations \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${t}",
    "prompt": "A lighthouse at dusk, watercolor",
    "size": "1024x1024",
    "n": 1
  }'`},{label:`Python`,code:`import base64, os
from openai import OpenAI

client = OpenAI(base_url="${e}", api_key=os.environ["GATEWAY_API_KEY"])

res = client.images.generate(model="${t}", prompt="A lighthouse at dusk, watercolor", size="1024x1024")
img = res.data[0]
if img.b64_json:
    open("image.png", "wb").write(base64.b64decode(img.b64_json))
else:
    print(img.url)`},{label:`TypeScript`,code:`import fs from "node:fs";
import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${e}", apiKey: process.env.GATEWAY_API_KEY });

const res = await client.images.generate({ model: "${t}", prompt: "A lighthouse at dusk, watercolor", size: "1024x1024" });
const img = res.data?.[0];
if (img?.b64_json) fs.writeFileSync("image.png", Buffer.from(img.b64_json, "base64"));
else console.log(img?.url);`}]:n===`rerank`?[{label:`curl`,code:`curl ${e}/rerank \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${t}",
    "query": "How do I rotate an API key?",
    "documents": ["Keys can be revoked in the console.", "Create a new key, deploy it, then revoke the old one."],
    "top_n": 2
  }'`},{label:`Python`,code:`import os, requests

res = requests.post(
    "${e}/rerank",
    headers={"Authorization": f"Bearer {os.environ['GATEWAY_API_KEY']}"},
    json={
        "model": "${t}",
        "query": "How do I rotate an API key?",
        "documents": ["Keys can be revoked in the console.", "Create a new key, deploy it, then revoke the old one."],
    },
)
for r in res.json()["results"]:
    print(r["index"], r["relevance_score"])`}]:n===`ocr`?[{label:`curl`,code:`curl ${e}/ocr \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${t}",
    "document": {"type": "document_url", "document_url": "https://example.com/invoice.pdf"}
  }'`},{label:`Python`,code:`import os, requests

res = requests.post(
    "${e}/ocr",
    headers={"Authorization": f"Bearer {os.environ['GATEWAY_API_KEY']}"},
    json={"model": "${t}", "document": {"type": "image_url", "image_url": "https://example.com/receipt.png"}},
)
for page in res.json()["pages"]:
    print(page["markdown"])`}]:n===`video`?[{label:`curl`,code:`# start the job
curl ${e}/videos \\
  -H "Authorization: Bearer $GATEWAY_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "${t}", "prompt": "A paper boat on a rainy street", "seconds": "4"}'

# poll until "status": "completed", then download
curl ${e}/videos/video-gw-… -H "Authorization: Bearer $GATEWAY_API_KEY"
curl ${e}/videos/video-gw-…/content -H "Authorization: Bearer $GATEWAY_API_KEY" --output video.mp4`},{label:`Python`,code:`import os, time
from openai import OpenAI

client = OpenAI(base_url="${e}", api_key=os.environ["GATEWAY_API_KEY"])

video = client.videos.create(model="${t}", prompt="A paper boat on a rainy street", seconds="4")
while video.status in ("queued", "in_progress"):
    time.sleep(5)
    video = client.videos.retrieve(video.id)
client.videos.download_content(video.id).write_to_file("video.mp4")`}]:n===`embedding`?[{label:`curl`,code:`curl ${e}/embeddings \\
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
console.log(res.choices[0].message.content);`}]}function y(e,t){return[{label:`OpenAI`,code:`from openai import OpenAI

# base_url ${e}/v1 or ${e}/openai/v1
client = OpenAI(base_url="${e}/v1", api_key="sk-gw-…")
client.chat.completions.create(model="${t}", messages=[{"role": "user", "content": "Hello"}])`},{label:`Anthropic`,code:`from anthropic import Anthropic

client = Anthropic(base_url="${e}/anthropic", api_key="sk-gw-…")
client.messages.create(model="${t}", max_tokens=256, messages=[{"role": "user", "content": "Hello"}])`},{label:`Google GenAI`,code:`from google import genai

# sent as x-goog-api-key: use your gateway key
client = genai.Client(api_key="sk-gw-…", http_options={"base_url": "${e}/genai"})
client.models.generate_content(model="${t}", contents="Hello")`}]}var b=()=>`${location.origin}/v1`;export{v as a,b as i,_ as n,y as o,h as r,g as t};