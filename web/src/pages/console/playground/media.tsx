import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { AudioLines, Download, FileAudio, ImageIcon, ListOrdered, Mic, Send, Square } from "lucide-react";
import { CopyButton, Field, ms } from "@/components/console/bits";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { compact, fmt, type ImageResult, type RerankResult } from "@/lib/api";
import { Meta, bytes, failure, useModelChoice, useObjectURL, type PanelProps } from "./shared";

const voices = ["alloy", "echo", "fable", "onyx", "nova", "shimmer"];
const formats = ["mp3", "opus", "aac", "flac", "wav", "pcm"];
const CUSTOM = "custom";

type Speech = { blob: Blob; format: string; provider?: string; latency: number };

export function SpeechPanel({ models, apiKey, keyField }: PanelProps) {
  const [chosen, modelField] = useModelChoice(models, "speech");
  const [preset, setPreset] = useState("alloy");
  const [customVoice, setCustomVoice] = useState("");
  const [format, setFormat] = useState("mp3");
  const [speed, setSpeed] = useState("1");
  const [instructions, setInstructions] = useState("");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Speech | null>(null);
  const url = useObjectURL(result?.blob ?? null);
  const abort = useRef<AbortController | null>(null);
  useEffect(() => () => abort.current?.abort(), []);

  const voice = preset === CUSTOM ? customVoice.trim() : preset;

  async function generate(e: FormEvent) {
    e.preventDefault();
    if (busy || !chosen || !apiKey || !text.trim() || !voice) return;
    const sp = Number(speed);
    if (speed && !(sp >= 0.25 && sp <= 4)) {
      setError("Speed must be between 0.25 and 4.");
      return;
    }
    setBusy(true);
    setError("");
    const ctl = new AbortController();
    abort.current = ctl;
    const started = performance.now();
    try {
      const res = await fetch("/v1/audio/speech", {
        method: "POST",
        signal: ctl.signal,
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey.trim()}` },
        body: JSON.stringify({
          model: chosen,
          input: text,
          voice,
          response_format: format,
          ...(speed ? { speed: sp } : {}),
          ...(instructions.trim() ? { instructions: instructions.trim() } : {}),
        }),
      });
      if (!res.ok) throw await failure(res);
      const blob = await res.blob();
      setResult({ blob, format, provider: res.headers.get("x-gateway-provider") ?? undefined, latency: performance.now() - started });
    } catch (err) {
      if (!ctl.signal.aborted) setError((err as Error).message);
    } finally {
      setBusy(false);
      abort.current = null;
    }
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,18rem)_minmax(0,1fr)]">
      <div className="grid content-start gap-4">
        {modelField}
        {keyField}
        <Field id="pg-voice" label="Voice" help={preset === CUSTOM ? undefined : "Common OpenAI voices; other providers may name theirs differently."}>
          <Select value={preset} onValueChange={setPreset}>
            <SelectTrigger id="pg-voice" className="w-full" aria-describedby={preset === CUSTOM ? undefined : "pg-voice-help"}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {voices.map((v) => (
                <SelectItem key={v} value={v}>
                  {v}
                </SelectItem>
              ))}
              <SelectItem value={CUSTOM}>Other voice…</SelectItem>
            </SelectContent>
          </Select>
        </Field>
        {preset === CUSTOM && (
          <Field id="pg-voice-custom" label="Voice name">
            <Input id="pg-voice-custom" value={customVoice} onChange={(e) => setCustomVoice(e.target.value)} placeholder="The provider's voice id" spellCheck={false} />
          </Field>
        )}
        <div className="grid grid-cols-2 gap-4">
          <Field id="pg-format" label="Format">
            <Select value={format} onValueChange={setFormat}>
              <SelectTrigger id="pg-format" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {formats.map((f) => (
                  <SelectItem key={f} value={f}>
                    {f}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field id="pg-speed" label="Speed">
            <Input id="pg-speed" type="number" min={0.25} max={4} step={0.05} value={speed} onChange={(e) => setSpeed(e.target.value)} />
          </Field>
        </div>
        <Field id="pg-instr" label="Instructions (optional)">
          <Textarea id="pg-instr" rows={3} value={instructions} onChange={(e) => setInstructions(e.target.value)} placeholder="Speak calmly and slowly." />
        </Field>
      </div>

      <div className="grid content-start gap-4">
        <form onSubmit={generate} className="grid gap-3 rounded-lg border p-4">
          <Field id="pg-tts-text" label="Text">
            <Textarea id="pg-tts-text" rows={6} value={text} onChange={(e) => setText(e.target.value)} placeholder="Type the text to speak." />
          </Field>
          <div className="flex flex-wrap items-center gap-3">
            {busy ? (
              <Button type="button" variant="outline" onClick={() => abort.current?.abort()}>
                <Square aria-hidden />
                Stop
              </Button>
            ) : (
              <Button type="submit" disabled={!text.trim() || !apiKey || !chosen || !voice}>
                <AudioLines aria-hidden />
                Generate speech
              </Button>
            )}
            {!apiKey && <p className="text-sm text-muted-foreground">Paste an API key on the left first.</p>}
            <p className="text-xs text-muted-foreground tabular-nums">{fmt(text.length)} characters</p>
          </div>
        </form>
        <section aria-label="Result" aria-live="polite" className="rounded-lg border p-4">
          {error ? (
            <p role="alert" className="text-sm text-destructive">{error}</p>
          ) : busy ? (
            <p className="text-sm text-muted-foreground">Generating audio…</p>
          ) : result && url ? (
            <div className="grid gap-3">
              {result.format === "pcm" ? (
                <p className="text-sm text-muted-foreground">Raw PCM can't be played in the browser. Download it, or pick another format.</p>
              ) : (
                <audio controls src={url} className="w-full">
                  <a href={url} download={`speech.${result.format}`}>Download the audio</a>
                </audio>
              )}
              <div className="flex flex-wrap items-center justify-between gap-3">
                <Meta
                  items={[
                    ...(result.provider ? ([["Provider", result.provider]] as [string, ReactNode][]) : []),
                    ["Latency", ms(result.latency)],
                    ["Size", bytes(result.blob.size)],
                    ["Type", result.blob.type || result.format],
                  ]}
                />
                <Button asChild variant="outline" size="sm">
                  <a href={url} download={`speech.${result.format}`}>
                    <Download aria-hidden />
                    Download
                  </a>
                </Button>
              </div>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">The generated audio plays here.</p>
          )}
        </section>
      </div>
    </div>
  );
}

type Transcript = { text: string; language?: string; duration?: number; provider?: string; latency: number };

const canRecord = () => typeof MediaRecorder !== "undefined" && !!navigator.mediaDevices?.getUserMedia;

export function TranscribePanel({ models, apiKey, keyField }: PanelProps) {
  const [chosen, modelField] = useModelChoice(models, "transcription");
  const [audio, setAudio] = useState<File | null>(null);
  const [language, setLanguage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Transcript | null>(null);
  const [recording, setRecording] = useState(false);
  const [elapsed, setElapsed] = useState(0);
  const [recError, setRecError] = useState("");
  const recorder = useRef<MediaRecorder | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const abort = useRef<AbortController | null>(null);
  const preview = useObjectURL(audio);

  useEffect(
    () => () => {
      abort.current?.abort();
      const r = recorder.current;
      if (r && r.state !== "inactive") r.stop();
      r?.stream.getTracks().forEach((t) => t.stop());
    },
    [],
  );
  useEffect(() => {
    if (!recording) return;
    const t0 = Date.now();
    const id = setInterval(() => setElapsed(Math.floor((Date.now() - t0) / 1000)), 500);
    return () => clearInterval(id);
  }, [recording]);

  async function startRecording() {
    setRecError("");
    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: true });
    } catch (err) {
      const name = (err as DOMException).name;
      setRecError(
        name === "NotAllowedError" || name === "SecurityError"
          ? "Microphone access was denied. Allow it in the browser's site settings, or choose a file instead."
          : name === "NotFoundError"
            ? "No microphone was found. Choose a file instead."
            : "Could not start recording. Choose a file instead.",
      );
      return;
    }
    const chunks: Blob[] = [];
    const r = new MediaRecorder(stream);
    r.ondataavailable = (e) => e.data.size && chunks.push(e.data);
    r.onstop = () => {
      stream.getTracks().forEach((t) => t.stop());
      const type = r.mimeType || "audio/webm";
      const ext = type.includes("ogg") ? "ogg" : type.includes("mp4") ? "m4a" : "webm";
      if (chunks.length) {
        setAudio(new File(chunks, `recording.${ext}`, { type }));
        if (fileInput.current) fileInput.current.value = "";
      }
      setRecording(false);
    };
    recorder.current = r;
    r.start();
    setElapsed(0);
    setRecording(true);
  }

  async function transcribe(e: FormEvent) {
    e.preventDefault();
    if (busy || !chosen || !apiKey || !audio) return;
    setBusy(true);
    setError("");
    const ctl = new AbortController();
    abort.current = ctl;
    const started = performance.now();
    try {
      const form = new FormData();
      form.append("file", audio, audio.name);
      form.append("model", chosen);
      if (language.trim()) form.append("language", language.trim());
      const res = await fetch("/v1/audio/transcriptions", {
        method: "POST",
        signal: ctl.signal,
        headers: { Authorization: `Bearer ${apiKey.trim()}` }, // the browser sets the multipart boundary
        body: form,
      });
      if (!res.ok) throw await failure(res);
      const data = (await res.json()) as { text?: string; language?: string; duration?: number };
      setResult({
        text: data.text ?? "",
        language: data.language,
        duration: data.duration,
        provider: res.headers.get("x-gateway-provider") ?? undefined,
        latency: performance.now() - started,
      });
    } catch (err) {
      if (!ctl.signal.aborted) setError((err as Error).message);
    } finally {
      setBusy(false);
      abort.current = null;
    }
  }

  const clock = `${Math.floor(elapsed / 60)}:${String(elapsed % 60).padStart(2, "0")}`;

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,18rem)_minmax(0,1fr)]">
      <div className="grid content-start gap-4">
        {modelField}
        {keyField}
        <Field id="pg-lang" label="Language (optional)" help="ISO-639-1 code such as en or de. Leave empty to detect it.">
          <Input id="pg-lang" value={language} onChange={(e) => setLanguage(e.target.value)} placeholder="en" spellCheck={false} aria-describedby="pg-lang-help" />
        </Field>
      </div>

      <div className="grid content-start gap-4">
        <form onSubmit={transcribe} className="grid gap-4 rounded-lg border p-4">
          <Field id="pg-file" label="Audio file">
            <Input
              id="pg-file"
              ref={fileInput}
              type="file"
              accept="audio/*"
              disabled={recording}
              onChange={(e) => setAudio(e.target.files?.[0] ?? null)}
            />
          </Field>
          <div className="grid gap-2">
            <p className="text-sm font-medium">Or record from your microphone</p>
            {canRecord() ? (
              <div className="flex flex-wrap items-center gap-3">
                {recording ? (
                  <Button type="button" variant="outline" onClick={() => recorder.current?.stop()}>
                    <Square aria-hidden />
                    Stop recording
                  </Button>
                ) : (
                  <Button type="button" variant="outline" onClick={startRecording} disabled={busy}>
                    <Mic aria-hidden />
                    Record
                  </Button>
                )}
                <p className="text-sm text-muted-foreground tabular-nums" aria-live="polite">
                  {recording ? `Recording, ${clock}` : ""}
                </p>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">This browser can't record audio here (it needs a secure page and MediaRecorder). Choose a file instead.</p>
            )}
            {recError && <p role="alert" className="text-sm text-destructive">{recError}</p>}
          </div>
          {audio && preview && (
            <div className="grid gap-2 rounded-md border bg-muted/40 p-3">
              <p className="flex items-center gap-2 text-sm">
                <FileAudio aria-hidden className="size-4 text-muted-foreground" />
                <span className="truncate">{audio.name}</span>
                <span className="text-muted-foreground tabular-nums">{bytes(audio.size)}</span>
              </p>
              <audio controls src={preview} className="w-full" />
            </div>
          )}
          <div className="flex flex-wrap items-center gap-3">
            {busy ? (
              <Button type="button" variant="outline" onClick={() => abort.current?.abort()}>
                <Square aria-hidden />
                Stop
              </Button>
            ) : (
              <Button type="submit" disabled={!audio || !apiKey || !chosen || recording}>
                <Send aria-hidden />
                Transcribe
              </Button>
            )}
            {!apiKey && <p className="text-sm text-muted-foreground">Paste an API key on the left first.</p>}
          </div>
        </form>
        <section aria-label="Transcript" aria-live="polite" className="rounded-lg border p-4">
          {error ? (
            <p role="alert" className="text-sm text-destructive">{error}</p>
          ) : busy ? (
            <p className="text-sm text-muted-foreground">Transcribing…</p>
          ) : result ? (
            <div className="grid gap-3">
              <p className="text-sm break-words whitespace-pre-wrap">{result.text || <span className="text-muted-foreground">No speech found.</span>}</p>
              <div className="flex flex-wrap items-center justify-between gap-3">
                <Meta
                  items={[
                    ...(result.provider ? ([["Provider", result.provider]] as [string, ReactNode][]) : []),
                    ["Latency", ms(result.latency)],
                    ...(result.duration != null ? ([["Audio", `${compact(result.duration)} s`]] as [string, ReactNode][]) : []),
                    ...(result.language ? ([["Language", result.language]] as [string, ReactNode][]) : []),
                  ]}
                />
                {result.text && <CopyButton text={result.text} label="Copy text" />}
              </div>
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">The transcript appears here.</p>
          )}
        </section>
      </div>
    </div>
  );
}

const sizes = ["1024x1024", "1536x1024", "1024x1536", "512x512", "256x256"];

type Images = { items: { src: string; revised?: string }[]; provider?: string; latency: number };

/** Image src from one generations item: a data URL for b64_json, else the provider's URL. */
const imageSrc = (d: ImageResult["data"][number]) => (d.b64_json ? `data:image/png;base64,${d.b64_json}` : d.url ?? "");

export function ImagePanel({ models, apiKey, keyField }: PanelProps) {
  const [chosen, modelField] = useModelChoice(models, "image");
  const [prompt, setPrompt] = useState("");
  const [size, setSize] = useState("1024x1024");
  const [n, setN] = useState("1");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Images | null>(null);
  const abort = useRef<AbortController | null>(null);
  useEffect(() => () => abort.current?.abort(), []);

  async function generate(e: FormEvent) {
    e.preventDefault();
    if (busy || !chosen || !apiKey || !prompt.trim()) return;
    const count = Number(n);
    if (!(Number.isInteger(count) && count >= 1 && count <= 4)) {
      setError("Number of images must be 1 to 4.");
      return;
    }
    setBusy(true);
    setError("");
    const ctl = new AbortController();
    abort.current = ctl;
    const started = performance.now();
    try {
      const res = await fetch("/v1/images/generations", {
        method: "POST",
        signal: ctl.signal,
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey.trim()}` },
        body: JSON.stringify({ model: chosen, prompt: prompt.trim(), size, n: count }),
      });
      if (!res.ok) throw await failure(res);
      const data = (await res.json()) as ImageResult;
      setResult({
        items: (data.data ?? []).map((d) => ({ src: imageSrc(d), revised: d.revised_prompt })).filter((x) => x.src),
        provider: res.headers.get("x-gateway-provider") ?? undefined,
        latency: performance.now() - started,
      });
    } catch (err) {
      if (!ctl.signal.aborted) setError((err as Error).message);
    } finally {
      setBusy(false);
      abort.current = null;
    }
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,18rem)_minmax(0,1fr)]">
      <div className="grid content-start gap-4">
        {modelField}
        {keyField}
        <div className="grid grid-cols-2 gap-4">
          <Field id="pg-size" label="Size">
            <Select value={size} onValueChange={setSize}>
              <SelectTrigger id="pg-size" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {sizes.map((v) => (
                  <SelectItem key={v} value={v}>
                    {v}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field id="pg-n" label="Images">
            <Input id="pg-n" type="number" min={1} max={4} step={1} value={n} onChange={(e) => setN(e.target.value)} />
          </Field>
        </div>
      </div>

      <div className="grid content-start gap-4">
        <form onSubmit={generate} className="grid gap-3 rounded-lg border p-4">
          <Field id="pg-img-prompt" label="Prompt">
            <Textarea id="pg-img-prompt" rows={4} value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder="A lighthouse on a cliff at dusk, watercolor." />
          </Field>
          <div className="flex flex-wrap items-center gap-3">
            {busy ? (
              <Button type="button" variant="outline" onClick={() => abort.current?.abort()}>
                <Square aria-hidden />
                Stop
              </Button>
            ) : (
              <Button type="submit" disabled={!prompt.trim() || !apiKey || !chosen}>
                <ImageIcon aria-hidden />
                Generate
              </Button>
            )}
            {!apiKey && <p className="text-sm text-muted-foreground">Paste an API key on the left first.</p>}
          </div>
        </form>
        <section aria-label="Images" aria-live="polite" className="rounded-lg border p-4">
          {error ? (
            <p role="alert" className="text-sm text-destructive">{error}</p>
          ) : busy ? (
            <p className="text-sm text-muted-foreground">Generating images…</p>
          ) : result ? (
            <div className="grid gap-3">
              {result.items.length === 0 ? (
                <p className="text-sm text-muted-foreground">The provider returned no images.</p>
              ) : (
                <ul className="grid gap-3 sm:grid-cols-2">
                  {result.items.map((img, i) => (
                    <li key={i} className="grid gap-2">
                      <img src={img.src} alt={img.revised || prompt} className="aspect-square w-full rounded-md border bg-muted object-contain" />
                      <div className="flex items-start justify-between gap-2">
                        <p className="line-clamp-2 text-xs text-muted-foreground">{img.revised ?? `Image ${i + 1}`}</p>
                        <Button asChild variant="outline" size="sm">
                          <a href={img.src} download={`image-${i + 1}.png`} target="_blank" rel="noreferrer">
                            <Download aria-hidden />
                            Download<span className="sr-only"> image {i + 1}</span>
                          </a>
                        </Button>
                      </div>
                    </li>
                  ))}
                </ul>
              )}
              <Meta
                items={[
                  ...(result.provider ? ([["Provider", result.provider]] as [string, ReactNode][]) : []),
                  ["Latency", ms(result.latency)],
                  ["Images", fmt(result.items.length)],
                ]}
              />
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">Generated images appear here.</p>
          )}
        </section>
      </div>
    </div>
  );
}

type Ranked = { rows: { index: number; score: number; text: string }[]; provider?: string; latency: number };

export function RerankPanel({ models, apiKey, keyField }: PanelProps) {
  const [chosen, modelField] = useModelChoice(models, "rerank");
  const [query, setQuery] = useState("");
  const [docs, setDocs] = useState("");
  const [topN, setTopN] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<Ranked | null>(null);
  const abort = useRef<AbortController | null>(null);
  useEffect(() => () => abort.current?.abort(), []);

  const documents = docs.split("\n").map((d) => d.trim()).filter(Boolean);

  async function rank(e: FormEvent) {
    e.preventDefault();
    if (busy || !chosen || !apiKey || !query.trim() || documents.length === 0) return;
    const top = topN.trim() ? Number(topN) : undefined;
    if (top !== undefined && !(Number.isInteger(top) && top >= 1)) {
      setError("Top N must be a whole number of 1 or more.");
      return;
    }
    setBusy(true);
    setError("");
    const ctl = new AbortController();
    abort.current = ctl;
    const started = performance.now();
    try {
      const res = await fetch("/v1/rerank", {
        method: "POST",
        signal: ctl.signal,
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${apiKey.trim()}` },
        body: JSON.stringify({ model: chosen, query: query.trim(), documents, ...(top ? { top_n: top } : {}) }),
      });
      if (!res.ok) throw await failure(res);
      const data = (await res.json()) as RerankResult;
      setResult({
        rows: (data.results ?? []).map((r) => ({ index: r.index, score: r.relevance_score, text: documents[r.index] ?? "" })),
        provider: res.headers.get("x-gateway-provider") ?? undefined,
        latency: performance.now() - started,
      });
    } catch (err) {
      if (!ctl.signal.aborted) setError((err as Error).message);
    } finally {
      setBusy(false);
      abort.current = null;
    }
  }

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,18rem)_minmax(0,1fr)]">
      <div className="grid content-start gap-4">
        {modelField}
        {keyField}
        <Field id="pg-topn" label="Top N (optional)" help="Return only the best N documents.">
          <Input id="pg-topn" type="number" min={1} step={1} value={topN} onChange={(e) => setTopN(e.target.value)} aria-describedby="pg-topn-help" />
        </Field>
      </div>

      <div className="grid content-start gap-4">
        <form onSubmit={rank} className="grid gap-3 rounded-lg border p-4">
          <Field id="pg-query" label="Query">
            <Input id="pg-query" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="How do I rotate an API key?" />
          </Field>
          <Field id="pg-docs" label="Documents, one per line">
            <Textarea id="pg-docs" rows={6} value={docs} onChange={(e) => setDocs(e.target.value)} placeholder={"Keys can be revoked from the console.\nThe gateway routes by weight.\nCreate a new key, deploy it, then revoke the old one."} />
          </Field>
          <div className="flex flex-wrap items-center gap-3">
            {busy ? (
              <Button type="button" variant="outline" onClick={() => abort.current?.abort()}>
                <Square aria-hidden />
                Stop
              </Button>
            ) : (
              <Button type="submit" disabled={!query.trim() || documents.length === 0 || !apiKey || !chosen}>
                <ListOrdered aria-hidden />
                Rerank
              </Button>
            )}
            {!apiKey && <p className="text-sm text-muted-foreground">Paste an API key on the left first.</p>}
            <p className="text-xs text-muted-foreground tabular-nums">{fmt(documents.length)} documents</p>
          </div>
        </form>
        <section aria-label="Ranking" aria-live="polite" className="rounded-lg border p-4">
          {error ? (
            <p role="alert" className="text-sm text-destructive">{error}</p>
          ) : busy ? (
            <p className="text-sm text-muted-foreground">Ranking…</p>
          ) : result ? (
            <div className="grid gap-3">
              {result.rows.length === 0 ? (
                <p className="text-sm text-muted-foreground">The provider returned no results.</p>
              ) : (
                <ol className="divide-y rounded-md border">
                  {result.rows.map((r, i) => (
                    <li key={r.index} className="grid grid-cols-[2rem_minmax(0,1fr)_auto] items-start gap-3 px-3 py-2 text-sm">
                      <span className="text-muted-foreground tabular-nums">{i + 1}.</span>
                      <span className="break-words">
                        {r.text}
                        <span className="ml-2 text-xs text-muted-foreground">document {r.index + 1}</span>
                      </span>
                      <span className="font-medium tabular-nums">{r.score.toFixed(4)}</span>
                    </li>
                  ))}
                </ol>
              )}
              <Meta
                items={[
                  ...(result.provider ? ([["Provider", result.provider]] as [string, ReactNode][]) : []),
                  ["Latency", ms(result.latency)],
                ]}
              />
            </div>
          ) : (
            <p className="text-sm text-muted-foreground">Documents appear here, most relevant first, with their scores.</p>
          )}
        </section>
      </div>
    </div>
  );
}
