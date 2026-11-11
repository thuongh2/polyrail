#!/usr/bin/env python3
"""Generate internal/catalog/catalog.json from a models.dev checkout (MIT, github.com/sst/models.dev).

    python3 scripts/gencatalog.py /path/to/models.dev [commit] > internal/catalog/catalog.json

Build-time only (needs Python 3.11+ for tomllib); the gateway embeds the JSON. Rules (docs/DECISIONS.md D19):
- one gateway model per canonical models.dev id "<author>/<model>" (`base_model`, or the file id for a
  first-party provider file without one); every provider offering it becomes a target, first-party
  first, then cheapest input price; the model price is the first-party price, else the cheapest;
- chat models carry `cost.text.input.cache` read/write prices of the same target (write: the 5-minute
  TTL price when per TTL; 0/absent = unset, D32);
- deprecated models, models without a positive price, realtime/live and router models are skipped;
- OpenRouter only adds targets to models another provider already offers.
"""
import glob, json, os, re, sys, tomllib

# our provider type -> models.dev provider id. Skipped: per-deployment types (azure, bedrock, vertex,
# sagemaker, cloudflare, databricks, ollama, vllm, sgl) and types models.dev does not list
# (parasail, replicate, byteplus, typesafe, runway, runware).
PROVIDERS = {"openai": "openai", "anthropic": "anthropic", "gemini": "google", "mistral": "mistral",
             "groq": "groq", "deepseek": "deepseek", "xai": "xai", "cohere": "cohere", "cerebras": "cerebras",
             "perplexity": "perplexity", "fireworks": "fireworks-ai", "nebius": "nebius",
             "huggingface": "huggingface", "elevenlabs": "elevenlabs", "minimax": "minimax", "sarvam": "sarvam",
             "wafer": "wafer.ai", "opencode-go": "opencode-go", "opencode-zen": "opencode", "openrouter": "openrouter"}
# canonical author -> our type, for first-party ordering and ids of files without base_model.
FIRST_PARTY = {"openai": "openai", "anthropic": "anthropic", "google": "gemini", "mistral": "mistral",
               "xai": "xai", "deepseek": "deepseek", "cohere": "cohere", "minimax": "minimax",
               "perplexity": "perplexity", "elevenlabs": "elevenlabs", "sarvam": "sarvam"}
SKIP_ID = re.compile(r"realtime|deep-research|(^|[-/])live([-/]|$)")


def merge(a, b):
    out = dict(a)
    for k, v in b.items():
        out[k] = merge(out[k], v) if isinstance(v, dict) and isinstance(out.get(k), dict) else v
    return out


def price(cost, kind, unit=None):
    """USD price of cost.<kind>.<input|output> -> micro-USD, or 0 when absent / other unit."""
    v = cost or {}
    for k in kind.split("."):
        v = v.get(k) if isinstance(v, dict) else None
    if not isinstance(v, dict) or v.get("unit") != unit or not isinstance(v.get("price"), (int, float)):
        return 0
    return round(v["price"] * 1e6)


TYPES = {"chat": "chat", "embedding": "embedding", "reranking": "rerank", "image": "image", "speech": "speech",
         "transcription": "transcription", "video": "video"}  # models.dev `type` -> our kind; others skipped


def kind_of(mid, m):
    if "type" in m:
        return None if SKIP_ID.search(mid) else TYPES.get(m["type"])
    mods = m.get("modalities") or {}
    i = {x.split("/")[0] for x in mods.get("input", [])}
    o = {x.split("/")[0] for x in mods.get("output", [])}
    if "embedding" in o:
        return "embedding"
    if "reranking" in o:
        return "rerank"
    if "decision" in o or SKIP_ID.search(mid):
        return None
    if "video" in o:
        return "video"
    if "image" in o:
        return "image"
    if "audio" in o:
        return None if "text" in o else "speech"
    if "text" not in i or re.search(r"transcri|whisper|scribe|stt", mid):
        return "transcription" if "audio" in i else None
    return "chat"


def cache_price(cost, which):
    """cost.text.input.cache.<read|write> -> micro-USD per 1M; write may be per TTL: the 5-minute price."""
    v = (((cost or {}).get("text") or {}).get("input") or {}).get("cache")
    v = v.get(which) if isinstance(v, dict) else None
    if isinstance(v, dict):
        v = v.get("5m")
    return round(v * 1e6) if isinstance(v, (int, float)) and v > 0 else 0  # 0 = unset (billed at input price)


def prices(kind, cost):
    p = {"in": price(cost, "text.input") or price(cost, "audio.input" if kind == "transcription" else "_"),
         "out": price(cost, "text.output")}
    if kind == "chat":
        p["cache_read"], p["cache_write"] = cache_price(cost, "read"), cache_price(cost, "write")
    if kind == "image":
        p["out"] = price(cost, "image.output") or p["out"]
        p["image"] = price(cost, "image.output", "image")
    if kind == "speech":
        p["out"] = price(cost, "audio.output") or p["out"]
    if kind == "transcription":
        p["minute"] = price(cost, "audio.input", "minute") or price(cost, "audio.input", "second") * 60
    if kind == "video":
        p["second"] = price(cost, "video.output", "second")
    return p


def first_sentence(s, limit=200):
    s = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", (s or "").split("\n")[0]).strip()
    m = re.match(r"(.+?[.!?])(\s|$)", s)
    s = m.group(1) if m else s
    return s if len(s) <= limit else s[: limit - 1].rsplit(" ", 1)[0] + "…"


def main():
    root, commit = sys.argv[1], (sys.argv[2] if len(sys.argv) > 2 else "")
    orjs = {m["id"]: m for m in json.load(open(os.path.join(root, "models.json")))["data"]}
    models, providers = {}, []
    for typ in sorted(PROVIDERS, key=lambda t: t == "openrouter"):  # openrouter last: it only merges
        pdir = os.path.join(root, "providers", PROVIDERS[typ])
        if not os.path.isdir(pdir):
            continue
        meta = tomllib.load(open(os.path.join(pdir, "provider.toml"), "rb"))
        added = 0
        for f in sorted(glob.glob(os.path.join(pdir, "models", "**", "*.toml"), recursive=True)):
            upstream = os.path.relpath(f, os.path.join(pdir, "models"))[:-5]
            d = tomllib.load(open(f, "rb"))
            cid = d.get("base_model")
            if not cid:
                author = next((a for a, t in FIRST_PARTY.items() if t == typ), None)
                if not author:
                    continue  # aggregator-only model without a canonical id
                cid = f"{author}/{upstream}"
            bf = os.path.join(root, "models", cid + ".toml")
            m = merge(tomllib.load(open(bf, "rb")), d) if os.path.exists(bf) else d
            if m.get("status") == "deprecated" or (typ == "openrouter" and cid not in models):
                continue
            kind = kind_of(cid, m)
            if not kind:
                continue
            p = prices(kind, m.get("cost"))
            if not any(v > 0 for v in p.values()):
                continue
            first = FIRST_PARTY.get(cid.split("/")[0]) == typ
            e = models.setdefault(cid, {"id": cid, "kind": kind, "targets": []})
            if any(t["type"] == typ for t in e["targets"]):
                continue  # a provider listing one canonical model twice (aliases): keep the first
            e["targets"].append({"type": typ, "model": upstream, "first": first, "p": p})
            if "name" not in e or first:
                lim = m.get("limit") or {}
                mods = (m.get("modalities") or {}).get("input", [])
                e.update(name=m.get("name") or cid, desc=m.get("description", ""), ctx=lim.get("context", 0),
                         out=lim.get("output", 0), kind=kind,
                         mods=sorted({{"application": "file"}.get(x.split("/")[0], x.split("/")[0]) for x in mods}))
            added += 1
        if added:
            providers.append({"type": typ, "name": meta.get("name", typ), "docs": meta.get("doc", "")})
    out = []
    for cid in sorted(models):
        e = models[cid]
        ts = sorted(e["targets"], key=lambda t: (not t["first"], t["type"] == "openrouter", t["p"]["in"] or 1 << 60))
        if all(t["type"] == "openrouter" for t in ts):
            continue
        p = ts[0]["p"] if ts[0]["first"] else min((t["p"] for t in ts), key=lambda p: (p["in"], p["out"]))
        oj = orjs.get(cid) or next((orjs[t["model"]] for t in ts if t["type"] == "openrouter" and t["model"] in orjs), None)
        desc = first_sentence(oj["description"]) if oj else first_sentence(e["desc"])
        row = {"id": cid, "name": e["name"], "kind": e["kind"]}
        if desc:
            row["description"] = desc
        row["context_length"] = e["ctx"] or (oj or {}).get("context_length") or 0
        if e["out"]:
            row["max_output_tokens"] = e["out"]
        if e["kind"] == "chat" and e["mods"]:
            row["modalities"] = e["mods"]
        for k, f in (("in", "input_price_micros"), ("out", "output_price_micros"), ("minute", "audio_minute_price_micros"),
                     ("image", "image_price_micros"), ("second", "video_second_price_micros"),
                     ("cache_read", "cache_read_price_micros"), ("cache_write", "cache_write_price_micros")):
            if p.get(k):
                row[f] = p[k]
        row["targets"] = [{"type": t["type"], "model": t["model"]} for t in ts]
        out.append(row)
    used = {t["type"] for m in out for t in m["targets"]}
    json.dump({"source": f"models.dev {commit}".strip(), "providers": [p for p in providers if p["type"] in used],
               "models": out}, sys.stdout, ensure_ascii=False, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
